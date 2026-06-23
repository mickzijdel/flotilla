# Flotilla — Daemon (supervisor)

**Date:** 2026-06-23
**Status:** Draft for review
**Scope:** A new backlog item, designed ahead of "On-demand fetch/pull" (backlog #2) because that
feature and several others all converge on the same missing piece: a long-running process that
watches agents and reacts to events. This spec defines that process — the **daemon** — and builds
its anchor feature, **auto-submit a PR when an agent finishes**, plus an operator **inbox** and the
**request-handler seam** that on-demand fetch and the future agent→operator question channel plug into.

> Realises the "watcher/daemon" deferred by the submission-flow spec
> ([2026-06-23-flotilla-submission-flow-design.md](2026-06-23-flotilla-submission-flow-design.md) §10,
> "auto-submit on done-signal") and the `blocked`-status note in the logs spec
> ([2026-06-23-flotilla-logs-transcript-mounts-design.md](2026-06-23-flotilla-logs-transcript-mounts-design.md) §12).

## 1. Goal

A finished agent's work should turn into a reviewable PR **without a human running a command at the
right moment**, and an agent that needs attention should be able to reach its operator. Today neither
happens: `flotilla` is a stateless CLI with no process of its own, so every reaction depends on someone
invoking a command. This slice adds an **optional, long-running supervisor** that closes that gap:

- **Auto-submit on done** — when an agent's process finishes, the daemon pushes its commits and opens
  (or updates) a PR, reusing the existing submission flow. Safe-gated (clean tree + ≥1 commit) and
  idempotent.
- **Operator inbox** — the daemon records notable events (`pr_opened`, `submit_skipped`, `agent_done`,
  later `question`) to a host-side append-only log the CLI and the future VS Code extension read.
- **Request-handler seam** — a typed agent→daemon control channel + dispatch loop that **on-demand
  fetch** (the next spec) and the **agent question/answer channel** (a later spec) slot into as handlers.

The daemon is **additive and optional** (Option A of the brainstorm): the CLI is untouched, talks
straight to Docker + `~/.flotilla` as today, and works with no daemon running — you only lose the
reactive behaviours. This preserves the stateless-CLI property the rest of the tool relies on and keeps
the decision reversible.

## 2. Decisions locked (from brainstorming, 2026-06-23)

| # | Area | Decision |
|---|---|---|
| 1 | Daemon role | **Additive supervisor**, not a broker. CLI keeps talking directly to Docker + the filesystem; the daemon is a *second consumer* of the same substrates that reacts to events. No CLI↔daemon RPC. A mandatory-broker model (CLI → socket → daemon) was rejected: it re-plumbs every command and makes the daemon a hard dependency for basic operations, none of which the current roadmap needs. |
| 2 | Lifecycle | **Self-daemonizing CLI.** `flotilla daemon start\|stop\|status` double-forks, writes a pidfile, and single-instances via `flock`. An optional `systemd --user` unit ships too. |
| 3 | Auto-start | **`flotilla spawn` auto-starts the daemon** if it isn't running, so auto-submit and the inbox "just work" after a spawn. |
| 4 | Auto-submit | **On by default, safe-gated.** Fires only on a clean tree with ≥1 commit (reusing `Submit`'s strict checks). Dirty/zero-commit → inbox event + skip; never force-commits. Idempotent via a per-agent last-submitted SHA. |
| 5 | Notifications | **Filesystem inbox** (`~/.flotilla/inbox.jsonl`), read by `flotilla inbox` and watched by the future extension. No OS-notification coupling. |
| 6 | Done-signal | **The logs spec's `/flotilla/session/status` → `done`** marker. Container-exit does **not** fire on its own (the agent is a backgrounded `docker exec -d`), so the launch-wrapper status file is the real signal. **This makes the logs/transcript-mounts feature a hard prerequisite** — sequencing is logs → daemon → fetch. |
| 7 | Extension-readiness | The daemon **persists live state to `~/.flotilla`** (files, not just memory) and every command stays `--json`, so the VS Code extension can read/watch without a socket. A Unix socket is the explicit *later* upgrade seam (Option C) for true push, droppable in additively. |
| 8 | Version skew | CLI and daemon are **one binary**, so they can't skew on install. On startup the daemon records its binary version; if the on-disk binary changes it **re-execs itself** (or refuses with a clear "restart the daemon" message). |

## 3. Where the daemon sits (Option A)

```
        flotilla spawn/list/submit/...            flotilla daemon (long-running)
                   │  (unchanged)                          │
                   ▼                                        ▼
        ┌──────────────────────┐                ┌────────────────────────┐
        │  cli → Fleet → Backend│                │  daemon.Supervisor      │
        └──────────┬───────────┘                │   ├─ watch: done-signal  │
                   │                             │   │   (status files) +   │
                   │                             │   │   Backend.Events     │
                   ▼                             │   ├─ handler: auto-submit│ → Fleet.Submit
        ┌──────────────────────┐                │   ├─ inbox writer        │ → ~/.flotilla/inbox.jsonl
        │ Docker  +  ~/.flotilla│◄───────────────┤   ├─ state mirror        │ → ~/.flotilla/daemon/
        │  (shared substrates)  │   both read    │   └─ request dispatch    │ → (fetch/question handlers, later)
        └──────────────────────┘                └────────────────────────┘
```

Both the CLI and the daemon derive truth from **Docker** (container status/labels) and the
**`~/.flotilla` filesystem** (clones, session/log dirs). They never call each other. The only new
message-passing is the agent→daemon control channel (§9), which is itself filesystem-mediated.

The daemon is constructed over the **same `fleet.Fleet`** the CLI builds in `main.go` (so it inherits
the configured `Backend` and `Forge`). `flotilla daemon start` builds that Fleet and runs
`daemon.Supervisor.Run(ctx)`.

## 4. Lifecycle & single-instance

New `internal/daemon` package + `daemonCmd` in the CLI.

- **`flotilla daemon start`** — double-forks into the background, writes `~/.flotilla/daemon.pid`,
  acquires an exclusive `flock` on `~/.flotilla/daemon.lock` (held for the process lifetime). A second
  `start` while the lock is held exits cleanly ("daemon already running, pid N"). Logs to
  `~/.flotilla/daemon.log`.
- **`flotilla daemon stop`** — reads the pidfile, sends SIGTERM, waits for a clean shutdown (the
  supervisor cancels its context, finishes in-flight handlers, releases the lock).
- **`flotilla daemon status [--json]`** — reports running/not, pid, uptime, watched-agent count, and
  last N inbox events. Reads the pidfile + the state mirror (§8); works even though there's no socket.
- **Auto-start** — `Fleet.Spawn`'s CLI wrapper (`spawnCmd`) calls a best-effort
  `daemon.EnsureRunning()` after a successful spawn: if the lock is free, fork `flotilla daemon start`.
  Failure is advisory (spawn still succeeds; you just don't get auto-actions).
- **Re-exec on upgrade** — at startup the daemon stamps its binary's version/mtime into the state dir;
  a periodic self-check re-execs the process from the new binary if it changed, so a `flotilla` upgrade
  doesn't leave a stale daemon running old reaction logic. Because there's no CLI↔daemon protocol, a
  briefly-stale daemon is at worst a stale *reaction*, never a broken command.

`systemd --user` users can instead run `flotilla daemon run` (a non-forking foreground variant) under a
shipped unit; the `flock` still guards against a double instance.

## 5. The done-signal (prerequisite: logs/transcript mounts)

**Container-exit is not a usable trigger.** The agent runs as `docker exec -d` (a backgrounded exec),
so when the agent process finishes the container's idle main process keeps running and the container
stays `running`. (See [off-topic-improvements](../../plans/off-topic-improvements.md): `done_signal =
"process-exit"` is declared but not wired to container status today.)

The real signal is the **launch-wrapper status file** introduced by the logs spec: the wrapper drops
`exec`, runs the agent, and writes `done` to `/flotilla/session/status` (host path
`~/.flotilla/logs/<repo>/<ts>-<agent>/status`) when the agent process exits. The daemon **watches those
status files** (host-side `fsnotify` on the logs tree, plus a startup scan to catch agents that
finished while the daemon was down) and treats a flip to `done` as "agent finished."

This is why **logs/transcript-mounts is a hard prerequisite** and must land first. A secondary trigger —
a Docker **stop/die** event (§10 `Backend.Events`) — covers the cases the status file can't (container
crash/OOM, manual `flotilla stop`), so a killed container still gets handled.

## 6. Auto-submit handler

On a done-signal for agent `<name>`:

1. **Dedup by SHA.** Read the per-agent record `~/.flotilla/daemon/agents/<name>.json`
   (`{ lastSubmittedSHA }`). Resolve the clone's current HEAD (`gitops`). If HEAD == `lastSubmittedSHA`,
   **skip** (already handled — covers daemon restarts and an attach→exit-again with no new work).
2. **Submit.** Call `Fleet.Submit(ctx, name, force=true)`. `force` is correct here: the daemon has an
   authoritative done-signal (the status file), so it intentionally bypasses Submit's *container-status*
   gate — but **not** the strict tree checks, which still apply:
   - clean tree + ≥1 commit ahead of base → push `flotilla/<name>` (force-with-lease) + ensure PR;
     write a `pr_opened` (or `pr_updated`) inbox event; record the submitted SHA.
   - dirty tree **or** zero commits → the strict refusal surfaces as an error; the daemon **does not
     force-commit**. It writes a `submit_skipped` inbox event with the reason and records nothing.
3. **Always** write an `agent_done` inbox event (independent of the submit outcome) so the operator sees
   the agent finished even when there was nothing to submit.

Auto-submit reuses `Fleet.Submit` verbatim — no duplicated push/PR logic. Racing a manual
`flotilla submit` is **benign**: Submit is idempotent (force-with-lease + existing-PR detection), so the
worst case is a redundant no-op push.

## 7. Inbox (daemon → operator)

The daemon appends JSON-lines to **`~/.flotilla/inbox.jsonl`** (create-on-first-write, append-only):

```json
{"ts":"2026-06-23T14:02:11Z","agent":"brave-otter","type":"pr_opened","message":"opened PR","data":{"prURL":"https://github.com/o/r/pull/42","branch":"flotilla/brave-otter"}}
{"ts":"2026-06-23T14:05:30Z","agent":"calm-finch","type":"submit_skipped","message":"uncommitted changes; not submitted","data":{}}
```

- **`flotilla inbox [--json] [--watch] [--since <ts>]`** — prints events (human table or raw JSONL);
  `--watch` tails the file (the same poll-and-print loop pattern as `flotilla logs -f`); `--since`
  filters. The file is an append-only record, not a queue — there's no "mark read" in this slice.
- Event `type`s in this slice: `agent_done`, `pr_opened`, `pr_updated`, `submit_skipped`. The schema is
  open (`type` + free-form `data`) so later handlers add `question`, `fetch_done`, etc. without a format
  change.
- The VS Code extension watches `~/.flotilla/inbox.jsonl` directly for near-real-time notifications.

## 8. State mirror (extension-readiness, no socket)

So the CLI and a future extension can see live daemon state **without** a socket, the daemon mirrors
state to plain files under `~/.flotilla/daemon/`:

- `daemon.pid`, `daemon.log` — process basics.
- `version` — the running binary's version/mtime (drives §4 re-exec).
- `agents/<name>.json` — per-agent supervisor view: last-seen status, `lastSubmittedSHA`, last event ts.

These are the daemon's own bookkeeping; the authoritative agent list still comes from Docker via
`Backend.List`. The mirror exists purely so consumers can read daemon-derived facts (e.g. "did the
daemon already submit this?") off disk. **The Unix socket is deliberately out of scope** and noted as
the Option-C upgrade for when a consumer needs true push instead of file-watching (§15).

## 9. Request-handler seam (designed now, built later)

On-demand fetch and the future agent question/answer channel both need the **agent to reach the
daemon**. The sandbox can't reach a host socket, so the channel is **filesystem-mediated** through the
session mount (`/flotilla/session/` in the container = the host log session dir):

```
/flotilla/session/requests/<id>.json     ← agent writes  {"type":"fetch"|"question", ...}
/flotilla/session/responses/<id>.json     ← daemon writes {"status":..., ...}
```

The daemon watches `requests/` (fsnotify), dispatches by `type` to a registered handler, and writes a
matching `responses/<id>.json`; the agent's in-container shim blocks polling for its response. The
**dispatch loop, the request/response envelope, and the watch are defined here**; the concrete handlers
are separate slices:

- `fetch` → runs `gitops.Fetch` on that agent's clone (the next spec, original backlog #2).
- `question` → writes a `question` inbox event and waits for `flotilla answer <agent> "..."` to drop the
  answer, which the daemon relays back as the response (a later spec).

**Auto-submit uses none of this** (it's done-signal-driven), so this slice implements only the loop's
scaffolding + registration API, with auto-submit as the first non-request reaction. No agent→daemon
channel is exercised until the fetch slice lands.

## 10. Backend seam

One additive `Backend` method for the secondary done-trigger and future event-driven needs:

```go
type Event struct {
    Type   string // "die" | "stop" | "start" (Docker action)
    ID     string
    Labels map[string]string
}
// Events streams container lifecycle events for flotilla-labelled containers
// until ctx is cancelled. The channel closes on ctx.Done or a fatal stream error.
Events(ctx context.Context) (<-chan Event, error)
```

- **Docker impl** shells `docker events --format '{{json .}}' --filter label=<LabelAgent> --filter event=die --filter event=stop --filter event=start` and decodes each line.
- **`Fake`** returns a channel tests push `Event`s into, so supervisor reactions are testable with no Docker.

The **primary** done-trigger remains the §5 status-file watch (a host `fsnotify`, no Backend change);
`Events` is the secondary/robustness trigger and the seam future features build on.

## 11. CLI surface

```
flotilla daemon start|stop|status|run     # run = foreground (for systemd)
flotilla inbox [--json] [--watch] [--since <ts>]
```

- `spawn` gains the best-effort auto-start (§4); no new flag.
- `daemon status --json` and `inbox --json` emit structured output for the extension.
- `doctor` gains an advisory line: is the daemon running? (Everything works without it.)
- `flotilla answer <agent> "..."` is **declared as the question-channel CLI but deferred** to the later
  question slice; listed here only so the inbox/seam shape is coherent.

## 12. Trust & safety boundary

- The daemon holds git credentials via the host environment, exactly like the CLI does today — no new
  credential store.
- Auto-submit only ever pushes to **`flotilla/<agent>`** with `--force-with-lease`; never a base/protected
  branch. The worst case stays "an unreviewed PR appears," consistent with the submission-flow thesis.
- When the agent→daemon channel (§9) lands, every request is bound to its **originating session dir** and
  acts only on **that agent's own clone** (`~/.flotilla/work/<agent>`). The action set is a fixed,
  typed allowlist (fetch own repo / ask a question) — never arbitrary command execution. A sandboxed
  agent can already write to its own session dir; the daemon adds only these bounded reactions.
- The state mirror, inbox, pidfile, and lock live under `~/.flotilla` (user-owned, `0700` dir).

## 13. Failure modes

| Condition | Behaviour |
|---|---|
| Daemon not running | No auto-actions; every CLI command still works. Manual `flotilla submit` is the fallback. |
| Daemon crash mid-submit | On restart, the startup status-file scan re-detects `done`; SHA dedup (§6) prevents a duplicate PR; `Submit` is idempotent regardless. |
| Two `daemon start` invocations | `flock` rejects the second; it exits "already running, pid N". |
| Agent finished while daemon was down | Startup scan of the logs tree catches `status == done` with HEAD ≠ lastSubmittedSHA and submits then. |
| Container killed (no `done` written) | `Backend.Events` `die`/`stop` triggers the handler as a fallback. |
| Binary upgraded under a running daemon | Self-check re-execs from the new binary (§4). |
| `Submit` strict refusal (dirty/zero-commit) | `submit_skipped` inbox event; no force-commit, no PR. |

## 14. Testing

Docker-free where possible (fake backend + temp `~/.flotilla`), plus a self-skipping Docker path like
the existing backend test.

- **Supervisor / auto-submit** (fake `Backend` + `fakeForge` + temp git clone + temp logs dir): writing
  `done` to a session `status` file triggers exactly one `Submit`; a clean tree with commits → push +
  `pr_opened` inbox event + recorded SHA; a dirty tree → `submit_skipped` event and **no** push; a second
  `done` with unchanged HEAD → skipped (SHA dedup); HEAD advanced → re-submitted.
- **`Backend.Events` fallback**: a `die` event with no `done` file still triggers the handler (via `Fake`).
- **Lifecycle**: `start` writes the pidfile and takes the lock; a second `start` is rejected; `stop`
  releases it; re-exec-on-changed-version fires (inject a fake version stamp).
- **Inbox**: append + `--since` filter + `--json` shape; `--watch` drains appended lines.
- **Request-seam scaffolding**: a registered fake handler is dispatched for a `requests/<id>.json` and a
  matching `responses/<id>.json` is written (no real fetch/question handler in this slice).
- **CLI wiring**: `daemon`/`inbox` flag parsing and output shapes; `spawn` best-effort auto-start is
  invoked (and its failure is non-fatal).
- **Docker integration** (self-skips without Docker): real `docker events` decode through the Docker
  `Backend.Events`.

`backend.Fake` is extended with a pushable event channel.

## 15. Sequencing & dependencies

1. **Logs / transcript mounts** (backlog #1) — **prerequisite.** Provides the `/flotilla/session`
   mount and the `status` → `done` marker the daemon's done-signal depends on (§5).
2. **Daemon** (this spec) — supervisor + auto-submit + inbox + state mirror + request-handler seam.
3. **On-demand fetch/pull** (backlog #2) — a `fetch` handler on the §9 seam: the agent's shim requests,
   the daemon runs `gitops.Fetch` on the engine-side clone (no creds in the container).

## 16. Out of scope (future)

- **Unix socket / broker (Option C)** — for true push to the VS Code extension or cross-agent
  coordination. Additive later; the file-based state mirror + inbox cover the near term.
- **Agent question/answer channel** — `question` request handler + `flotilla answer`; its own spec.
- **`on-demand fetch` handler** — the next spec; this one only defines the seam.
- **Inbox as a queue** (mark-read / ack / retention) — it's an append-only log for now.
- **Auto-fetch / auto-rebase policies, auto-stop-on-done, multi-host supervision** — later daemon slices.
