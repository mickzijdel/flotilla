# Egress firewall spike — findings (2026-06-15)

De-risking spike for [docs/plans/2026-06-15-flotilla-egress-firewall.md](../plans/2026-06-15-flotilla-egress-firewall.md).
Verified the out-of-container proxy model: a `ubuntu/squid` sidecar on a fresh
`--internal` Docker network, with the agent network-swapped onto that internal
net so its only route out is the proxy.

## Pinned image (Task 5 `ProxyImage`)

```
ubuntu/squid@sha256:6a097f68bae708cedbabd6188d68c7e2e7a38cedd05a176e1cc0ba29e3bbe029
```

(`docker pull ubuntu/squid:latest` then `docker inspect --format '{{index .RepoDigests 0}}'`.)

## Topology / network-swap

- Test agent started on the default `bridge` network (confirms the topology
  guard's expected starting network = `["bridge"]`).
- After `network connect flotilla-net-spike` + `network disconnect bridge`, the
  agent's `NetworkSettings.Networks` showed **only** `flotilla-net-spike`.
- `docker exec` into the agent kept working after the swap (engine control plane
  is over the docker socket, not the container network) — attach/stop/launch are
  unaffected.

## Allow / deny / no-direct-route

squid `access.log` (the ground truth):

```
TCP_TUNNEL/200  CONNECT example.org:443   -> allowlisted host reaches
TCP_DENIED/403  CONNECT example.com:443   -> non-allowlisted host blocked
```

- **Allowed** (`example.org`, in `acl allowed dstdomain .example.org`) via proxy:
  curl `%{http_code}` = `200`.
- **Denied** (`example.com`, not allowlisted) via proxy: squid returns
  **`TCP_DENIED/403`**. curl reports `CONNECT tunnel failed, response 403`.
- **Direct** (no proxy) to `example.com` from the swapped agent: blocked (no
  route; curl `-m 5` returns nothing).

## ⚠️ Important nuance for the Task 9 smoke

Over **HTTPS** the deny is a denied `CONNECT` tunnel, so **curl's
`%{http_code}` is `000`, not `403`** — the 403 only appears on stderr
(`CONNECT tunnel failed, response 403`) and in squid's access log. The plan's
Task 9 text ("`example.com` is `403`") is true at the squid layer but the
agent-side curl probe sees `000`. When validating in Task 9, treat a denied
HTTPS host as **`000` at curl + `TCP_DENIED/403` in `docker logs <proxy>` /
access.log**, and an allowed host as a real `2xx/3xx/4xx`.

## squid.conf used (matches `egress.SquidConf` output)

```
http_port 3128
acl SSL_ports port 443
acl CONNECT method CONNECT
http_access deny CONNECT !SSL_ports
acl allowed dstdomain .example.org
http_access allow allowed
http_access deny all
cache deny all
```

No config tweak was needed — squid started clean (`listening port: 3128`) and
enforced the allowlist as written.

## Task 9 end-to-end smoke (real devcontainer + claude agent) — results

Ran `flotilla spawn … --agent claude` with the firewall on (default). Verified
the guarantee against a real provisioned agent:

- **Agent on the internal net only** — `NetworkSettings.Networks` =
  `flotilla-net-atlas` (no `bridge`).
- **Real agent traffic, allowed:** squid logged `TCP_TUNNEL/200 CONNECT
  api.anthropic.com:443` — the claude agent reached the Anthropic API through
  the proxy (allowlisted via the claude profile's `egress_allow`).
- **Real agent traffic, denied:** `TCP_DENIED/403 CONNECT
  http-intake.logs.us5.datadoghq.com:443` — claude's telemetry to a
  non-allowlisted host was blocked by default-deny.
- **Manual allow/deny via the proxy:** `api.github.com` → `200`,
  `example.com` → `000` (denied CONNECT).
- **No direct route:** `curl --noproxy '*'` from the agent is blocked.
- **`flotilla list`** shows exactly one agent (proxy excluded).
- **Teardown** (`stop` + `rm`) removes the proxy container, the internal
  network, and the agent container.

### Bugs the smoke caught (now fixed)

1. **squid FATAL on overlapping `dstdomain`.** The baked allowlist listed both a
   parent and its subdomains (`github.com` + `api.github.com` +
   `codeload.github.com`; `crates.io` + `static.crates.io`). squid refuses a
   dstdomain list where one entry covers another → the proxy exited 1. Fixed in
   `egress.SquidConf` (reduce to the minimal covering set).
2. **Dead proxy went undetected.** `docker start` returns before squid validates
   its config, so a crashed proxy left the agent confined with no egress and no
   error. `setupFirewall` now verifies the proxy is still running (short settle)
   before swapping the agent's network.
3. **Proxy polluted `flotilla list`.** The proxy carries `flotilla.agent` (needed
   so the docker backend's always-on agent-label scope can find it for
   teardown), so it appeared as a second phantom "atlas" and could be returned by
   `resolve()`. `List`/`resolve` now skip `flotilla.proxy`-tagged containers.
