# Flotilla — Egress firewall (proxy model)

**Date:** 2026-06-15
**Status:** Draft for review
**Scope:** "Next plan" from [the backlog](../backlog.md) — default-deny egress for each agent so a
runaway or prompt-injected agent cannot reach hosts outside a curated allowlist. Builds on the
merged devcontainer + injection slice (see
[that spec](2026-06-15-flotilla-devcontainer-injection-design.md)).

## 1. Summary

Each spawned agent is confined to an **allowlist of egress destinations**. Enforcement is **outside
the agent container** — the agent runs on a Docker **`--internal`** network with no route to the
internet, and its only way out is a small **per-agent proxy sidecar** (squid) that allows
HTTP(S) `CONNECT`/requests **only to allowlisted hostnames**. Because the control plane (the proxy
and the network topology) lives outside the blast radius, the agent — even if it gained root in its
own container — cannot reconfigure it. The agent container gets **no extra capabilities**
(no `NET_ADMIN`, no in-container iptables).

This **revises design-spec §4.6** (which proposed an in-container iptables `init-firewall.sh`): the
proxy model is escape-resistant without host root, filters by hostname (no CDN IP-churn), needs no
container capabilities, and travels to a remote Docker backend — at the cost of HTTP(S)-only egress
(acceptable for coding agents) and one extra small container + network per agent.

## 2. Decisions locked (from brainstorming, 2026-06-15)

| # | Area | Decision |
|---|---|---|
| 1 | Enforcement | **Out-of-container.** Agent on a Docker `--internal` network; egress only via a proxy sidecar. No `NET_ADMIN`, no in-container iptables. |
| 2 | Proxy | **Per-agent squid sidecar** (`ubuntu/squid`, pinned by digest; ~25 MB): a maintained, trusted-publisher, battle-tested image — preferred over a third-party tinyproxy image or our own proxy code for a security boundary. Precise per-profile allowlist, clean lifecycle, footprint still <1% of the devcontainer. |
| 3 | Allowlist sources | **Baked default set** + `profile.EgressAllow` (agent API) + `Fleet.EgressAllow` (engine-wide override). Per-repo `.flotilla.toml` deferred to the config-file plan. |
| 4 | Failure mode | **Fail-closed** — any proxy/network setup error aborts the spawn (remove agent + clone + proxy + network). `Fleet.EgressFirewall bool` (default `true`) opts out (plain bridge, no proxy). |
| 5 | Protocol | **HTTP(S) only** (a CONNECT/forward proxy). Non-HTTP egress (raw TCP, git-over-SSH) is not supported — and largely undesired, since the engine does all git ops. |

## 3. Architecture

### 3.1 Topology

```
        ┌─────────────────────────────┐         ┌──────────────────────┐
        │ agent container (devcontainer)│        │ proxy sidecar         │
        │  no NET_ADMIN, no route out   │        │  squid (ubuntu/squid) │
        │  HTTP(S)_PROXY=proxy:3128 ────┼──┐  ┌──┤  dstdomain allowlist  │
        └─────────────────────────────┘  │  │  └───────────┬──────────┘
                                          ▼  ▼              │ (egress net, NAT)
                                  flotilla-net-<agent>      ▼
                                   (--internal, no NAT)   the internet (allowlisted hosts only)
```

- **`flotilla-net-<agent>`** — a per-agent `--internal` user-defined bridge network (no gateway to
  the outside). The agent container and the proxy are both attached to it.
- The **proxy** is additionally attached to a normal (NAT'd) network so *it* can reach the internet.
- The agent's **only** outbound path is `→ proxy → internet`, and the proxy only forwards
  allowlisted hostnames. Unsetting `HTTP_PROXY` inside the agent does not help it — the internal
  network has no other route out (fail-closed by topology).

### 3.2 Allowlist composition (engine-side)

`allowlist = bakedDefault ∪ profile.EgressAllow ∪ Fleet.EgressAllow` (deduped, sorted). Baked
default (the essentials a coding agent needs at runtime):

- **GitHub:** `github.com`, `api.github.com`, `codeload.github.com`, `objects.githubusercontent.com`,
  `raw.githubusercontent.com` (read-only fetches; no creds in the box means it still cannot push)
- **Package registries:** `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `ghcr.io`,
  `*.docker.io`/`*.docker.com` (pulls), `deb.nodesource.com`, mise's host, `crates.io` /
  `static.crates.io`, `proxy.golang.org`/`sum.golang.org`

`profile.EgressAllow` adds the agent API (`api.anthropic.com` / `api.openai.com`).
`Fleet.EgressAllow` is the engine-wide override knob. (The MCP host allow is deferred — no MCP yet.)

The engine renders the allowlist into the proxy's config as **domain suffixes** (so `anthropic.com`
matches `api.anthropic.com`, `statsig.anthropic.com`, etc., which the agent API needs).

### 3.3 The proxy image

**`ubuntu/squid`, pinned by digest** — a maintained, trusted-publisher image (no `docker build`
infra in the engine, no third-party-community-image risk). squid's `acl dstdomain` + `http_access`
do hostname allowlist + default-deny, including HTTPS `CONNECT` (restricted to port 443). The engine
generates a per-agent `squid.conf` (bind-mounted over the image's default) of the form:
```
http_port 3128
acl SSL_ports port 443
acl CONNECT method CONNECT
http_access deny CONNECT !SSL_ports
acl allowed dstdomain .anthropic.com .github.com .npmjs.org …   # the rendered allowlist
http_access allow allowed
http_access deny all
cache deny all                                                  # no caching, minimal logs
```
The image digest is pinned in code (a `const`); refreshing it is a deliberate change.

### 3.4 Flow (extends Spawn)

```
… Up (bridge) → secrets → config → install (open network: Feature build + agent CLI)
  → [firewall, if Fleet.EgressFirewall]:
      a. render allowlist → squid.conf
      b. NetworkCreate flotilla-net-<agent> (--internal)
      c. start proxy sidecar (ubuntu/squid, squid.conf mounted), attach to internal + egress nets
      d. network-swap the agent: NetworkConnect internal; NetworkDisconnect bridge
      e. set HTTP_PROXY/HTTPS_PROXY/NO_PROXY in the agent (added to the env-file)
  → launch (agent now confined; only egress path is the proxy)
```

- The Feature build (during `Up`) and the agent-CLI install run **before** the swap, on the open
  bridge, so they have full network. The firewall clamps down **after** install, **before** launch.
- Engine control (`docker exec`/`cp`) is unaffected by the swap (exec is not network-routed).
- `NO_PROXY` includes `localhost,127.0.0.1` and the workspace-local services if any.

### 3.5 Backend additions

The `Backend` interface gains **network primitives** so the orchestration stays behind the seam and
the `Fake` can record them (keeping `Spawn` unit-testable):

- `NetworkCreate(ctx, name string, internal bool) error`
- `NetworkRemove(ctx, name string) error`
- `NetworkConnect(ctx, network, containerID string) error`
- `NetworkDisconnect(ctx, network, containerID string) error`

The proxy sidecar is launched via the existing `Create`+`Start` (the pinned `ubuntu/squid` image,
the generated `squid.conf` bind-mounted, labels `flotilla.proxy=<agent>` + `flotilla.repo`), then
attached to the egress network via `NetworkConnect`. The Docker impl shells `docker network …` /
`docker run`; the `Fake` records calls for assertions. No image build is needed — the engine pulls
the pinned `ubuntu/squid` digest on first use.

### 3.6 Lifecycle & cleanup

The proxy container (`flotilla.proxy=<agent>`) and the network (`flotilla-net-<agent>`) are torn
down with their agent:

- `Fleet.Stop(name)` stops the agent **and** its proxy.
- `Fleet.Remove(name)` removes the agent, the proxy, **and** the network.
- `List` continues to show only agents (filtered by `flotilla.agent`); proxies are hidden.
- Fail-closed cleanup (the `fail` closure) removes the agent container, clone, proxy, and network.

## 4. Fail-closed & opt-out

- Any error in steps (a)–(e) routes through the existing `fail` closure → remove agent container +
  clone + proxy + network, return the error. **The agent never launches without a confirmed
  firewall.**
- `Fleet.EgressFirewall bool` (default **true**): when `false`, the whole firewall block is skipped
  and the agent runs on the normal bridge (trusted/dev runs). Surfaced as a `--no-egress-firewall`
  spawn flag (or global config) — exact CLI surface settled in the plan.

## 5. Security model

**What it protects:** the realistic threat — an agent that goes off the rails or is
**prompt-injected** by malicious repo/dependency content and tries to exfiltrate secrets or phone
home. The agent has **no route out except the proxy**, the proxy only forwards allowlisted hosts,
and the agent **cannot reconfigure** either (the proxy is a separate container; the network topology
is set by the engine; the agent has no `NET_ADMIN`). Even root-in-the-agent-container cannot reach a
non-allowlisted host.

**Residual risks (documented, accepted for a local single-user fleet):**
- **HTTP(S)-only.** Non-HTTP egress is unsupported; an agent needing raw TCP to an allowlisted
  service would need a future explicit TCP-allow. Coding-agent egress is ~all HTTPS.
- **Proxy-unaware tools** that ignore `HTTP_PROXY` simply fail to connect (no other route) —
  fail-closed, but could surprise a tool. Most tools (curl, git-https, npm, pip, node, claude)
  honor proxy env.
- **The host runs the proxy** and holds the real egress path; a Docker daemon / kernel escape from
  the agent container still defeats any in-Docker control (the container-not-microVM residual from
  design-spec §7/§9). The proxy model removes the *easy* in-container bypass; it does not turn a
  container into a VM.
- **DNS:** with `HTTP_PROXY` set, the agent sends hostnames to the proxy (the proxy resolves), so
  the agent needs no outbound DNS. Direct DNS from the agent on the internal net fails closed.

## 6. Testing strategy

- **Unit (no Docker), against `Fake`:** allowlist composition
  (`baked ∪ profile.EgressAllow ∪ Fleet.EgressAllow`, deduped/sorted); `squid.conf` rendering
  (default-deny + the allowlist `dstdomain` entries); the Spawn step-ordering (network create → proxy start →
  swap → proxy env → launch, all between install and launch); **skipped entirely when
  `EgressFirewall=false`**; **fail-closed** (a backend whose proxy/network step errors → agent +
  clone + proxy + network all removed). The credential-isolation test still holds.
- **Integration (skip without docker+devcontainer):** real spawn with the firewall on, then from
  inside the agent container assert (a) an allowlisted host (`https://api.anthropic.com`) connects
  via the proxy, (b) a denied host (`https://example.com`) is **refused** by the proxy, and (c) the
  agent has **no direct internet route** (a direct, proxy-bypassing connection to a public IP
  fails). Plus proxy/network are removed on `rm`.
- **Spike (plan task 1):** confirm the **network-swap** (`disconnect bridge` + `connect internal`)
  on a running devcontainer leaves engine `exec` working and the agent reachable only via the proxy;
  confirm squid `acl dstdomain` + `http_access deny all` allows/denies by hostname over HTTPS CONNECT; confirm
  embedded DNS / proxy resolution works on an `--internal` network.

## 7. Out of scope (later)

- Per-repo `.flotilla.toml` egress overrides (the config-file plan).
- The MCP host allow (no MCP yet; §4.7).
- Non-HTTP / raw-TCP egress allows.
- **docker-compose-based devcontainers**: the network-swap assumes an image/Dockerfile devcontainer
  on the default bridge; a compose devcontainer (multiple services on a compose network) is not
  handled in this slice — detect and either skip the firewall (with a warning) or error. Settled in
  the plan.
- A shared proxy with per-source ACLs (efficiency option) — per-agent is chosen for v1.
- Periodic allowlist refresh / wildcard-by-org GitHub ranges.

## 8. Spec §4.6 revision note

Design-spec §4.6 proposed an in-container iptables `init-firewall.sh` (lifted from ClaudeBox /
Anthropic) shipped in the toolchain Feature. This spec **supersedes** that with the out-of-container
proxy model for stronger, escape-resistant enforcement without host root or container capabilities.
The design spec will be annotated to point here.
