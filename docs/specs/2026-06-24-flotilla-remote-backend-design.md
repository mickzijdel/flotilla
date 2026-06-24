# Flotilla — Remote backend (federated SSH client)

**Date:** 2026-06-24
**Status:** Draft for review
**Scope:** Lets one laptop drive a fleet of flotilla agents spread across multiple remote machines —
each remote host runs its own full flotilla engine, and the laptop is a stateless **client
multiplexer** that dispatches commands over SSH and merges the results. No new `Backend`
implementation, no distributed state, no credentials on the client.

## 1. Goal

Today flotilla runs the whole fleet on one machine: the CLI, the daemon, engine-side clones, the
log/session tree, and the Docker workload all share one host's filesystem. That's a hard constraint
the rest of the system leans on deliberately (see §2). But the natural next want is *more compute
than one laptop has* — run agents on a beefy Linux box or a cloud VM, and ideally drive **several**
such hosts at once from the laptop, including from the VS Code extension.

This slice delivers that with the cheapest correct shape: **run the existing engine where the
compute is, once per host, and add a thin SSH client layer above the CLI that talks to one or many
hosts in parallel.** The remote engines are unchanged; the laptop never owns agent state and never
bridges a filesystem.

## 2. Why this shape (and what it is *not*)

The `Backend` interface abstracts *container operations within one engine*. It does **not** abstract
the deeper assumption woven through the rest of flotilla: **the engine and the agent's filesystem
share a disk.** Three subsystems rely on it:

- **gitops** clones to a local path, then `devcontainer up --workspace-folder <that path>` and every
  bind mount resolve on the **Docker daemon's** host.
- **logs / transcript** — the engine *reads* `~/.flotilla/logs/.../container.log` and the live
  transcript directly (`flotilla logs -f`, daemon `status` polling).
- **on-demand fetch + the Q/A channel** — `flotilla answer` writes a response file *directly into
  the agent's session dir*, and `flotilla questions` derives state by reading `requests/`/`responses/`
  files. Both assume the engine and the session dir are on one filesystem.

That shared-filesystem assumption is what makes "remote" hard. Two tempting designs fight it:

- **Local engine → remote `DOCKER_HOST`** (the README's literal framing). Broken on its own: the
  local clone doesn't exist on the remote daemon, and the engine can't read/write the log + session +
  Q/A files that now live on the far side. Making it work means cloning remotely *and* giving the
  engine remote read/write to the session tree — i.e. rebuilding the next option anyway.
- **SSH-as-Backend / laptop-as-single-control-plane.** The laptop owns state and bridges every
  filesystem assumption above across SSH/sftp (remote clone, remote mounts, live cross-gap reads of
  the log + Q/A tree, or continuous sync-back with its partial-failure and race surface). A major
  build; only justified if the laptop *must* be the single aggregating owner of state.

**This spec rejects both.** Instead: each host runs the complete engine locally (every assumption
holds, untouched), and the laptop is a **federated client** that fans commands out over SSH and
merges JSON. The hard coupling stays *inside* each host where it already works. This is also the
natural stepping stone to a future pool/scheduler (§13) without committing to distributed state now.

**Correction to prior docs:** "remote backend" is therefore **not** a new `Backend`. It is a new
*client transport layer above the CLI*. The README's "`DOCKER_HOST` over TLS/SSH" line is corrected
accordingly (§12). Docker Sandboxes / `sbx`, when it lands on Linux, remains the one genuine future
*Backend* addition — orthogonal to this work.

## 3. Decisions locked (from brainstorming, 2026-06-24)

| # | Area | Decision |
|---|------|----------|
| 1 | Topology | **Federated client over per-host engines.** Each remote runs the full flotilla engine; the laptop is a stateless SSH multiplexer. No new `Backend`, no distributed state. |
| 2 | Transport | **Run the remote binary over SSH** (`ssh <target> flotilla <args> --json`), reusing the user's SSH config/keys. **Not** a remote Docker socket (rejected Option 1). |
| 3 | Host lifecycle | **Talk-only + doctor preflight.** v1 assumes flotilla/docker/devcontainer are already installed on each host (documented manual setup). No install/provision in v1 (§13). |
| 4 | Default scope | Aggregate commands (`list`/`agents`/`inbox`/`questions`) with no `--host` **fan out to all registered hosts in parallel** and merge; a failing host is a **warning row, never fatal**. |
| 5 | Version skew | **Warn on minor, block on contract break.** Same JSON-contract major → proceed (warn if version string differs); different contract → block that host with an actionable error, others keep working. |
| 6 | Addressing | Agents are `host:agent`; `--host <name>` flag + `FLOTILLA_HOST` env select a host. Single-agent commands resolve the owning host (explicit wins; else fan-out lookup). |
| 7 | State ownership | **Each host owns its own state** (daemon, inbox, questions, logs). The client holds only the host registry — no agent state. |
| 8 | Secrets | **No secrets transit the client.** Each host's engine keeps its own git/gh/Claude credentials locally. SSH is the only trust boundary. |

## 4. Architecture

```
  laptop (stateless client)                       remote hosts (full engines, unchanged)
  ┌─────────────────────────────┐
  │ host registry (hosts.toml)  │      ssh user@beefy   ┌────────────────────────────────┐
  │                             │ ───────────────────▶ │ beefy: flotilla daemon + clones │
  │ Transport:                  │   flotilla list --json│   logs / inbox / questions      │
  │  • LocalTransport → Fleet   │ ◀─────────────────────│   docker + devcontainer         │
  │  • SSHTransport  → ssh+exec │      bare JSON array   └────────────────────────────────┘
  │                             │
  │ fan-out + merge + tag host  │      ssh flotilla-cloud-1  ┌────────────────────────────┐
  │ → { rows, hosts }           │ ─────────────────────────▶│ cloud1: full engine         │
  └─────────────────────────────┘ ◀─────────────────────────└────────────────────────────┘
```

The client picks a **transport** per target host:

- **`LocalTransport`** — invokes `Fleet` in-process, exactly as today (the implicit `local` host).
- **`SSHTransport`** — shells `ssh <target> flotilla <args> --json` and parses the remote engine's
  existing JSON output.

Aggregate commands iterate the selected transports concurrently; single-host commands use one. The
remote flotilla is **completely unaware** it's driven remotely — its behavior and output are
unchanged. All the new code is the client layer; `Fleet` and `Backend` are untouched.

### 4.1 Code shape

- **`internal/remote`** (new): the host registry (load/save `hosts.toml`), the `Transport` interface,
  `LocalTransport`, `SSHTransport` (argv construction + shell-escaping + `ssh` invocation), and the
  fan-out/merge/host-tagging helpers.
- **`internal/cli`**: commands gain `--host` / `--all-hosts` awareness and route through a transport
  instead of calling `Fleet` directly. A new `flotilla host` command group (§6). The existing
  `version` command grows a `--json` form reporting `{version, contract}` (§7).

`Backend`, `Fleet`, `gitops`, `daemon` are unchanged.

## 5. Host registry

`~/.flotilla/hosts.toml` maps a name → SSH destination (a real `user@host`, an `ssh://` URL, or a
`~/.ssh/config` Host alias so keys/ProxyJump/known_hosts are reused). An implicit **`local`** host
always exists and uses `LocalTransport`.

```toml
[hosts.beefy]
ssh = "user@beefy.example.com"

[hosts.cloud1]
ssh = "flotilla-cloud-1"   # ~/.ssh/config Host alias → keys/ProxyJump reused
```

Optional per-host fields are reserved for later (e.g. a custom remote `flotilla` binary path); v1
needs only `ssh`.

## 6. Host management — `flotilla host`

```
flotilla host add <name> <ssh-target>     # register; refuses to clobber an existing name without --force
flotilla host ls [--json]                  # list hosts + reachability/version/contract (runs doctor per host)
flotilla host rm <name>                    # deregister (never touches the remote)
flotilla host doctor [<name>]              # preflight one or all hosts (§7)
```

`host ls` and `host doctor` run the preflight (§7) against each host in parallel and render a health
table; `local` is included.

## 7. Doctor & version compatibility

`flotilla host doctor [name]` runs the **existing** `doctor` command on the remote over SSH plus a
version handshake. Per host it checks:

- **SSH reachable** (a trivial `ssh <target> true` succeeds).
- **`flotilla` present** on the remote `PATH`.
- **`docker` + `devcontainer` CLIs present** (the remote `doctor`'s existing checks).
- **Contract compatibility.** `flotilla version --json` reports `{ "version": "0.x.y", "contract": N }`
  where `contract` is an integer bumped only when the client⇄engine JSON contract changes
  incompatibly. The client compares its own `contract` to each remote's:
  - **Equal contract → OK.** If the `version` *string* differs, emit a non-fatal warning.
  - **Different contract → block that host** with an actionable error (`host %q runs flotilla
    contract N, client expects M — align versions`). Other hosts are unaffected (the block is
    per-host, surfaced inline like any other host error, §8).

## 8. Selection, addressing & aggregation

**Selection.** `--host <name>` (and `FLOTILLA_HOST`) pick a single host. Absent both, aggregate
commands use **all** registered hosts; single-agent commands resolve the owning host (below).

**Addressing.** An agent is `host:agent`. The `host:` prefix, an explicit `--host`, or a unique match
disambiguates. Resolution for single-agent commands (`attach`, `logs`, `submit`, `stop`, `rm`,
`answer`, `status`):

1. Explicit `--host` or a `host:` prefix → use it directly.
2. Otherwise the client does a quick fan-out `list` to locate the agent:
   - **unique across hosts** → use that host;
   - **ambiguous** (same name on ≥2 hosts) → error listing the `host:agent` candidates;
   - **not found** → error.

**Aggregation shape.** Aggregate commands (`list`, `agents`, `inbox`, `questions`) fan out in
parallel. The remote engine returns its existing **bare JSON array**; the **client** tags each row
with its `host` and wraps the merge:

```json
{
  "rows": [ { "host": "beefy", "agent": "brave-otter", "status": "running", ... }, ... ],
  "hosts": [
    { "name": "beefy",  "ok": true,  "version": "0.4.0", "contract": 1 },
    { "name": "cloud1", "ok": false, "error": "ssh exit 255: connection refused" }
  ]
}
```

- The `hosts` array reports per-host health so a partial failure is **visible, not silent**.
- Human (non-`--json`) output gains a **HOST** column, collapsed when only one host is in play; an
  unreachable or contract-blocked host renders as a warning line (`! cloud1: connection refused`) and
  the reachable hosts still render. **A failing host never aborts the command** (non-zero exit only if
  *every* targeted host fails).
- The wrapped `{rows, hosts}` object is emitted by the **client layer** only. The remote engine, when
  invoked over SSH, still emits its unchanged bare shapes — so the contract boundary is "client parses
  the remote's existing JSON," which is exactly what the `contract` integer guards.

## 9. attach & streaming

Interactive and streaming commands pass through the SSH pipe:

- `--host X attach <agent>` → `ssh -t <target> flotilla attach <agent>` (TTY passthrough; the remote
  `attach` does its existing `docker exec` / prints VS Code attach info).
- `--host X logs <agent> -f` and `--host X questions --watch` stream the remote engine's output over
  the SSH connection until interrupted.

For VS Code, the extension uses **Remote-SSH** to the host and then attaches to the container there
(the remote `attach` still yields the container id / exec form). This is a note for the extension
spec, not work in this slice.

## 10. SSH mechanics & safety

- The client shells out to the system `ssh`, reusing the user's SSH config, keys, `ProxyJump`, and
  `known_hosts`. No bespoke SSH library in v1.
- **Argument safety.** Each argument forwarded to the remote `flotilla` is **shell-escaped** before
  being placed on the `ssh` command line (which runs through the remote login shell). This avoids the
  same shell-metacharacter hazard already documented for prompt interpolation in the backlog — agent
  names, prompts, and answer text must round-trip verbatim.
- **Connection reuse.** The client recommends/sets `ControlMaster=auto` + `ControlPersist` with a
  control socket under `~/.flotilla/ssh/` so a fan-out to N hosts doesn't pay full handshake latency
  per command. Documented; defaults are conservative and overridable via the user's SSH config.

## 11. Trust boundary

- **SSH is the boundary.** The client only ever runs `flotilla` on hosts the user already controls and
  can already SSH into. It grants no capability beyond what an SSH session to that host already does.
- **No secrets transit the client.** Each host's engine owns its own git/gh/Claude credentials locally
  — consistent with the project's no-creds-in-container posture (here: *no-creds-on-client*). The only
  thing crossing the wire is the (shell-escaped) command and the JSON response.
- **No cross-host access.** A command targets exactly the hosts the user selects; there is no implicit
  lateral movement between hosts, and `host rm` never touches the remote.

## 12. Docs to correct

- **README** — the `## Status` / roadmap "`DOCKER_HOST` over TLS/SSH for multi-machine" line is
  replaced with the federated-client model (run the engine per host; drive many over SSH). Note the
  remote-Docker-socket approach was evaluated and rejected (§2).
- **backlog.md** — the "Remote backend" entry's "the `Backend` interface seam is already in place"
  framing is corrected: this is a client transport layer above the CLI, not a new `Backend`. The
  Docker-Sandboxes/`sbx` future-backend note stays (it *is* a real future `Backend`).
- **design spec §7** — add a pointer noting the remote story is realised as a client transport, not a
  substrate/Backend swap.

## 13. Out of scope (future)

- **Install / provision a host** — `flotilla host install` (scp/curl the binary, check deps) and
  full provisioning (install Docker/devcontainer). v1 is talk-only; setup is documented and manual.
- **Pool / scheduler** — picking a least-loaded host automatically, load-balancing spawns across
  hosts. This federated client is the foundation; scheduling is its own later spec.
- **Distributed single-control-plane state (the rejected SSH-as-Backend / Option 3)** — the laptop
  owning aggregated agent state and bridging the log/Q/A filesystem across SSH.
- **Remote-`DOCKER_HOST`-socket mode (the rejected Option 1)** — pointing a local engine at a remote
  Docker daemon.
- **Docker Sandboxes / `sbx` backend** — a genuine future `Backend`, added when it lands on Linux;
  orthogonal to this client layer.
- **A persistent client↔engine API / socket** — v1 is one `ssh exec` per command; a long-lived
  channel (for push notifications to the extension) is a later upgrade.

## 14. Testing

Docker-free and SSH-free where possible (a fake `Transport`), plus a self-skipping live path.

- **Host registry** — load/save `hosts.toml`; `host add` refuses to clobber without `--force`;
  `host rm` removes only the registry entry; the implicit `local` host is always present.
- **`SSHTransport` argv** — the constructed `ssh` argument vector is correct and every forwarded
  argument is shell-escaped so names/prompts/answers with metacharacters round-trip (asserted on the
  argv, no real ssh).
- **Fan-out merge** (fake transport) — rows from multiple hosts are merged and tagged with `host`; the
  `{rows, hosts}` shape is correct; ordering is stable.
- **Per-host error is non-fatal** (fake transport that errors for one host) — the failing host appears
  in `hosts` with `ok:false` + `error`, reachable hosts still return rows, and the command exits
  non-zero only when *all* targeted hosts fail.
- **Version / contract gating** — equal contract proceeds (warning emitted on differing version
  string); differing contract blocks that host with a clear error while other hosts proceed.
- **Cross-host agent resolution** — unique match resolves; ambiguous errors listing `host:agent`
  candidates; missing errors clearly; explicit `--host`/prefix bypasses the lookup.
- **`flotilla version --json`** — reports `{version, contract}`.
- **Live (self-skips when ssh-to-localhost is unavailable)** — register `local` as an SSH target to
  `localhost`, run `flotilla --host <that> list`, and assert it round-trips through `ssh` +
  `flotilla` and parses (mirrors the Docker integration self-skip pattern).

## 15. Sequencing & dependencies

1. **The full engine** (done) — spawn/list/attach/stop/rm/submit, logs/transcript, daemon, on-demand
   fetch — is what each remote host runs unchanged. No engine changes are required by this slice.
2. **`--json` outputs** (largely done — `list`/`status`/`inbox`/`questions`) — the client parses these.
   Any aggregate command missing a `--json` form gets one as part of this slice.
3. **Agent question/answer channel** — orthogonal; `answer`/`questions` route to the owning host like
   any other command. No ordering dependency either way.
4. **VS Code extension** (separate spec, in flight) — consumes this slice's integration surface:
   `--host` / `--all-hosts` + the existing `--json` outputs + the new wrapped `{rows, hosts}` shape,
   and Remote-SSH for `attach` (§9).
