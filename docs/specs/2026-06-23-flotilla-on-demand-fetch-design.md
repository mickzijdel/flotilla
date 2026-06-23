# Flotilla — On-demand fetch/pull for running agents

**Date:** 2026-06-23
**Status:** Draft for review
**Scope:** Backlog item #2 ("On-demand fetch/pull"). Lets a running agent — which holds **no git
credentials** — get the engine to re-fetch `origin` mid-session, so it can pick up base-branch changes
without restarting. Builds directly on the **daemon's request-handler seam**
([2026-06-23-flotilla-daemon-design.md](2026-06-23-flotilla-daemon-design.md) §9) and the engine-side
clone the submission flow already established.

## 1. Goal

An agent runs against an engine-side clone bind-mounted into its container. During a long session the
base branch on `origin` can move (a dependency PR merges, the operator pushes a fix). The agent may want
those changes — but it can't `git fetch`, because the container deliberately has no credentials. Only
the engine can reach `origin`.

This slice adds **on-demand fetch**: the agent (or the operator/orchestrator) asks the engine to run
`git fetch` against that agent's clone. Because the clone is bind-mounted, the new `origin/*` refs are
**live inside the container the instant the engine fetches** — no restart, no copy. The agent then
integrates them itself (`git merge origin/<base>` / `git rebase`), which is a **local, credential-free**
operation it can already do.

**Fetch-only is the primitive.** The engine never touches the working tree or the current branch, so a
fetch can't conflict with the agent's in-progress edits. Integration is the agent's call.

## 2. Decisions locked (from brainstorming, 2026-06-23)

| # | Area | Decision |
|---|---|---|
| 1 | Operation | **Fetch-only** (`git fetch --prune origin`): update remote-tracking refs only; never merge, rebase, or touch the working tree. The agent integrates locally. A `--pull`/fast-forward variant was rejected for v1 — it couples the engine to integration semantics and can dead-end on a dirty tree. |
| 2 | Triggers | **Two paths, one primitive.** (a) **Agent-initiated** via an in-container `flotilla-fetch` shim that goes through the daemon's request channel and blocks for the result — the daemon, always watching, services it promptly. (b) **Host/orchestrator** via `flotilla fetch <agent>`, a direct host-side git op that works daemon-or-not. |
| 3 | Shim & channel | In-container command **`flotilla-fetch`**; requests live under the session mount at `/flotilla/session/requests/`, responses at `/flotilla/session/responses/` (the daemon-spec §9 envelope). |
| 4 | Servicer | The **daemon's `fetch` handler** (registered on the §9 seam). No lazy-drain / no separate foreground watcher — the daemon obsoletes both. |

## 3. Architecture — two triggers, one primitive

```
(a) agent-initiated (no creds in container)         (b) host/orchestrator
  ┌───────────────────────────┐                     ┌────────────────────────┐
  │ container: `flotilla-fetch`│                     │ `flotilla fetch <agent>`│
  └────────────┬──────────────┘                     └───────────┬────────────┘
   write requests/<id>.json (session mount)                     │ Fleet.Fetch(name)
               │  (blocks polling responses/<id>.json)          │
               ▼                                                 │
      ┌──────────────────────┐                                  │
      │ daemon fetch handler  │                                  │
      │ (§9 dispatch loop)    │                                  │
      └──────────┬───────────┘                                  │
                 └──────────────► gitops.Fetch(dest) ◄───────────┘
                                   git -C dest fetch --prune origin
                                   (engine holds creds; never the container)
                                          │
                                  bind mount ⇒ origin/* refs live in container
```

Both paths converge on the **same** `gitops.Fetch`. Path (a) is the only way the *sandboxed agent* can
trigger a fetch (it can't run host commands or reach a credential); it requires the daemon (auto-started
on spawn). Path (b) is a direct host-side git op — daemon-agnostic, for the operator or an orchestrating
agent driving the CLI.

## 4. The git primitive

New in `internal/gitops` (reuses the existing `git()` helper, which already scopes
`-c safe.directory=<dir>` for container-written `.git` files):

```go
// Fetch updates origin's remote-tracking refs in the engine-side clone.
// Read/write-neutral on the working tree: it writes only refs/remotes/origin/*,
// FETCH_HEAD, and new objects — never the index, HEAD, or any local branch — so
// it is safe to run while the agent has uncommitted work. The engine holds the
// credentials; the container never does.
func Fetch(ctx context.Context, dir string) error // git -C dir -c safe.directory=dir fetch --prune origin
```

`--prune` keeps deleted upstream branches from lingering. Tags follow git's default refspec behaviour.

## 5. Host/orchestrator path — `flotilla fetch`

```go
// internal/fleet
func (f *Fleet) Fetch(ctx context.Context, name string) error
```

`Fleet.Fetch` resolves the agent (the existing `resolve`, excluding the proxy sidecar), confirms the
clone exists at `dest = ~/.flotilla/work/<name>/`, and runs `gitops.Fetch(ctx, dest)`. No container is
touched. Mirrors `Fleet.Submit`'s host-side model exactly.

CLI:

```
flotilla fetch <agent> [--json]
```

- Human: `Fetched origin for <agent>` (or git's stderr verbatim on failure).
- `--json`: `{ "agent": "<name>", "fetched": true }`.
- Works whether or not the daemon is running (direct git op).

## 6. Agent-initiated path — the daemon `fetch` handler

Registered on the daemon's §9 dispatch loop for `type: "fetch"`.

1. The agent's `flotilla-fetch` shim writes `/flotilla/session/requests/<id>.json`:
   `{"type":"fetch","id":"<id>"}` (written atomically — tmp file + rename — so the daemon never reads a
   partial request).
2. The daemon (watching `requests/` via fsnotify) dispatches to the `fetch` handler, which maps the
   session dir → that agent's clone `dest` (via the `flotilla.logdir`/agent labels) and runs
   `gitops.Fetch(ctx, dest)`.
3. The daemon writes `/flotilla/session/responses/<id>.json` atomically:
   `{"status":"ok"}` or `{"status":"error","error":"<git stderr>"}`. The presence of the response file
   is itself the "serviced" marker, so each `<id>` is handled exactly once.
4. The daemon also appends a `fetch_done` event to the inbox (daemon-spec §7) so the operator sees that
   the agent refreshed.

The handler acts **only** on the clone belonging to the session dir the request came from (trust
boundary, §10). It is a fixed, typed action — fetch that one repo — never arbitrary command execution.

## 7. The in-container shim

`flotilla-fetch` is a small POSIX-sh script injected at spawn (via the existing injector / `docker cp`,
the same path that places the prompt and env-file) to **`/usr/local/bin/flotilla-fetch`** — already on
`PATH`, so no launch-wrapper change — and `chmod +x`. It's written during the root-capable install step
(like the agent CLI install). It needs nothing but the session mount:

```sh
#!/bin/sh
# Ask the engine to fetch origin into this agent's clone (we have no git creds).
set -e
sess=/flotilla/session
id="$(date +%s%N)-$$"
mkdir -p "$sess/requests" "$sess/responses"
# atomic write: tmp + mv
printf '{"type":"fetch","id":"%s"}' "$id" > "$sess/requests/.$id.tmp"
mv "$sess/requests/.$id.tmp" "$sess/requests/$id.json"
# block until the daemon writes the response (cap the wait)
i=0
while [ ! -f "$sess/responses/$id.json" ]; do
  i=$((i+1)); [ "$i" -gt 120 ] && { echo "flotilla-fetch: timed out (is the daemon running?)" >&2; exit 1; }
  sleep 1
done
resp="$(cat "$sess/responses/$id.json")"
case "$resp" in
  *'"status":"ok"'*) echo "flotilla-fetch: origin fetched"; exit 0 ;;
  *) echo "flotilla-fetch: $resp" >&2; exit 1 ;;
esac
```

**Agent awareness.** A short line is appended to the injected prompt (the same mechanism as `wrap_up`),
e.g.: *"You have no git credentials. To pull in the latest base-branch changes, run `flotilla-fetch` —
the engine fetches `origin` for you — then `git merge origin/<base>` or rebase as you see fit."* This is
a constant preamble, not per-agent code.

## 8. Integration is the agent's job (and why that's safe)

After a fetch, `origin/<base>` has moved but the agent's branch, working tree, and index are untouched.
The agent decides how to integrate — `git merge origin/<base>`, `git rebase origin/<base>`, or just
`git log`/`git diff` against it — all **local, credential-free** operations inside the container. The
engine never integrates, so it never has to resolve a conflict or refuse on a dirty tree. This is the
whole reason fetch-only is the right primitive.

## 9. Concurrency & ownership

- **Concurrency.** A host-side fetch can race a container-side `git commit`/`git add` on the same
  `.git`. They touch mostly disjoint state (fetch → `refs/remotes/origin/*` + `FETCH_HEAD` + objects;
  commit → `refs/heads/*` + index), and git guards shared structures (`packed-refs` lock, object
  writes via tmp+rename). On the rare lock collision git exits non-zero; the shim surfaces it and the
  agent retries. No corruption risk.
- **Ownership.** `fetch` writes into `.git`. In the common case the devcontainer remaps the run user to
  the host engine uid (`updateRemoteUserUID: true`), so `.git` is host-owned and the fetch writes
  cleanly — the same situation the submission flow relies on
  ([submission-flow §4.2](2026-06-23-flotilla-submission-flow-design.md)). In the edge cases it calls
  out (root `remoteUser`, `updateRemoteUserUID:false`, rootless/userns Docker) a write into `.git` may
  hit permission-denied; we surface git's stderr verbatim rather than masking it. `safe.directory` is
  already scoped by the shared `git()` helper.

## 10. Trust boundary

- The engine (CLI for path b, daemon for path a) holds the credentials; the container never does — the
  one security property the project is built around is preserved.
- The daemon `fetch` handler acts **only** on the clone bound to the request's originating session dir,
  and the action is a fixed type (fetch that repo). A sandboxed agent can already write to its own
  session dir; this grants it exactly one new, bounded effect: "fetch my own origin." No arbitrary exec,
  no other agent's clone, no push.
- Fetch only *reads* from `origin` and writes remote-tracking refs locally — it can never move a
  protected branch or leak a token into the container.

## 11. Error handling

| Condition | Behaviour |
|---|---|
| Agent not found (CLI) | `no agent named %q` (existing `resolve`). |
| No clone at `dest` | clear error: "no workspace clone for agent %q". |
| `git fetch` fails (network/auth/ownership) | surface git stderr verbatim; `safe.directory` always set. |
| Shim, daemon not running | request file sits unserviced; shim times out with "is the daemon running?" (it's auto-started on spawn, so this is rare). The operator can still run `flotilla fetch` (path b). |
| Daemon handler can't map session → clone | writes `{"status":"error",...}`; inbox `fetch_done` notes the failure. |
| Concurrent git lock | git's non-zero surfaces to the shim/CLI; safe to retry. |

## 12. Testing

Docker-free where possible (real-git temp dirs + fake backend/daemon), plus the existing self-skipping
Docker integration path.

- **`gitops.Fetch`** — real git: bare "remote", clone, advance the remote, `Fetch`, assert
  `origin/<base>` moved **and** the working tree/HEAD/index are unchanged (dirty-tree case included to
  prove fetch is non-disruptive).
- **`Fleet.Fetch`** — fake backend + temp clone: resolves the agent, runs the fetch, errors clearly on a
  missing clone / unknown agent.
- **Daemon `fetch` handler** — drop a `requests/<id>.json` into a temp session dir wired to a temp
  clone; assert a `responses/<id>.json` with `{"status":"ok"}` appears, the clone's `origin/<base>`
  moved, a `fetch_done` inbox event was written, and a malformed/foreign session dir is rejected.
- **Shim** — script-level test (run `flotilla-fetch` against a fake session dir with a pre-seeded
  response): asserts the atomic request write, the block-until-response, and the ok/error exit codes +
  the timeout path.
- **CLI** — `flotilla fetch` flag parsing and the two output shapes (human / `--json`).
- **Prompt preamble** — assert the fetch-awareness line is appended to the injected prompt.
- **Docker integration** (self-skips without Docker): a real agent runs `flotilla-fetch`, the daemon
  services it, and the advanced `origin/<base>` is visible inside the container.

## 13. Sequencing & dependencies

1. **Logs / transcript mounts** (backlog #1) — provides `/flotilla/session` and the session-dir
   layout the request/response channel rides on.
2. **Daemon** ([daemon design](2026-06-23-flotilla-daemon-design.md)) — provides the request-handler
   seam (§9), the dispatch loop, and the inbox this handler registers against. **Hard prerequisite for
   the agent-initiated path.**
3. **On-demand fetch** (this spec) — `gitops.Fetch`, `Fleet.Fetch` + `flotilla fetch` (path b, daemon-
   independent), and the daemon `fetch` handler + `flotilla-fetch` shim (path a).

The host/orchestrator path (b) could in principle ship before the daemon (it's just a host-side git op),
but the agent-initiated path (a) — the point of the feature — needs the daemon seam, so this lands after
the daemon.

## 14. Out of scope (future)

- **`--pull` / fast-forward / auto-rebase** integration on the engine side — fetch-only is the v1
  contract; the agent integrates.
- **Auto-fetch policies** (e.g. periodic background fetch, fetch-on-base-change) — a later daemon slice.
- **Fetching arbitrary refs / other remotes** — v1 fetches `origin` with the default refspec.
- **A non-blocking/async shim mode** — v1 blocks for the result, which is what an agent wants before it
  integrates.
