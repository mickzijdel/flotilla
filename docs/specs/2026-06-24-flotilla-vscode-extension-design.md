# Flotilla — VS Code extension (view plane)

**Date:** 2026-06-24
**Status:** Draft for review
**Scope:** A VS Code extension that is a **view-plane client** over the `flotilla` CLI: a single
editor-area dashboard tab to monitor and drive a fleet of agents — across repos **and across hosts** —
answer agents' questions, acknowledge/open finished PRs, and trigger container attach. The engine (the
`flotilla` CLI) stays the only control plane; the extension shells out to it and never talks to Docker,
SSH, or the daemon directly. Supersedes the thin-TreeView sketch in the design spec
([2026-06-14-flotilla-design.md](2026-06-14-flotilla-design.md) §6) — the sidebar is replaced by an
editor tab, and one small webview is now in scope.

**Dependencies:**
- **Shipped — buildable against today:** the **agent question/answer channel**
  ([2026-06-23-flotilla-agent-question-channel-design.md](2026-06-23-flotilla-agent-question-channel-design.md);
  `flotilla questions`/`answer` are wired in `internal/cli`), **logs/transcript mounts**
  ([2026-06-23-flotilla-logs-transcript-mounts-design.md](2026-06-23-flotilla-logs-transcript-mounts-design.md)),
  the **daemon/inbox** ([2026-06-23-flotilla-daemon-design.md](2026-06-23-flotilla-daemon-design.md)),
  and the **submission flow** (`submit --json`).
- **Soft — lights up when it lands, doesn't block the build:** the **remote backend / federated SSH
  client** ([2026-06-24-flotilla-remote-backend-design.md](2026-06-24-flotilla-remote-backend-design.md)) —
  the host dimension, the wrapped `{rows,hosts}` JSON shape, `flotilla version --json`, and remote attach
  via Remote-SSH; and the deferred **objective/progress extractor** (§14.1).

## 1. Goal

Running a fleet from the terminal works, but watching several agents — across several repos and, once
the federated client lands, several hosts — is exactly the kind of at-a-glance, multi-row state a GUI
shows better than scrollback: who's blocked on a question, who's done with a PR waiting to be
acknowledged, who exited, which host is unreachable. The extension gives the operator one **editor
tab** that renders the whole fleet, collects the things that need a human (questions and finished PRs)
into one place, and maps every lifecycle action to the CLI verb that already implements it. It adds
**no new control surface** and **no engine logic**: every privileged action is a `flotilla …`
invocation. Uninstall it and nothing about the engine changes.

## 2. Decisions locked (from brainstorming, 2026-06-24)

| # | Area | Decision |
|---|------|----------|
| 1 | Surface | **One editor-area `WebviewPanel`** titled "Flotilla" (a regular tab, full editor width), not an activity-bar TreeView. A sidebar tree is too cramped for a multi-repo / multi-host fleet plus a needs-attention area. |
| 2 | Entry points | **Command-palette "Flotilla: Open Dashboard" + a status-bar item.** No activity-bar icon. The status-bar item shows fleet health and turns attention-colored when something needs the operator. |
| 3 | Control plane | **CLI-only.** The extension `exec`s the `flotilla` binary and parses `--json`; it never calls Docker, SSH, or the daemon directly. The local `flotilla` binary *is* the federated client, so "remote" is transparent to the extension. |
| 4 | Daemon coupling | **None required.** Lifecycle/answer commands are direct CLI calls. The inbox watcher only makes notifications *push* for the **local** host; everything still works (by polling) with the daemon down or for remote hosts. |
| 5 | Scope | **Full lifecycle:** spawn (native QuickPick/InputBox flow), refresh, attach, logs, submit, stop, rm, inbox, the questions/answer loop, and PR acknowledge/open. |
| 6 | Spawn input | **Native QuickPick/InputBox**, not an HTML form — better affordances than hand-rolled webview widgets. |
| 7 | Webview boundary | **One webview, kept small** (single file, framework-free, `acquireVsCodeApi` + `postMessage`). It renders state and posts intents; it never `exec`s anything itself. |
| 8 | Host dimension | **`host`-aware throughout**, but degrades to a single implicit `local` host before the federated client lands. `cli.ts` normalizes both JSON shapes (§5.1). |
| 9 | Needs-attention | A single section collects **blocked agents (questions)** and **done agents with an open PR awaiting acknowledgement** (§7). |
| 10 | Objective/progress | **Deferred** (§14.1). The per-row sub-line slot is reserved and the render contract defined; extraction is an agent-specific, engine-side concern with its own spec. |
| 11 | Distribution | **`clients/vscode/`, VSIX-only** for v1 (`vsce package`; install locally / attach to releases). No Marketplace publish yet. |

## 3. Architecture

```
  ┌──────────────────────── VS Code extension host (Node) ────────────────────────┐
  │  extension.ts        activation, command registration, status-bar item        │
  │  cli.ts              typed exec() wrapper: locate binary, run, parse + NORMALISE│
  │                      (absorbs bare-array vs {rows,hosts}; defaults host=local)  │
  │  fleet.ts            poll loop + state model (status, blocked overlay, hosts)   │
  │  dashboard.ts        WebviewPanel: render state ⇄ receive intents (postMessage) │
  │  inboxWatcher.ts     FileSystemWatcher on the LOCAL ~/.flotilla/inbox.jsonl     │
  │  ack.ts              PR-acknowledgement set persisted in globalState            │
  └───────┬───────────────────────────────────────────────────────────┬──────────┘
          │ child_process exec                                          │ executeCommand / createTerminal
          ▼                                                             ▼
   flotilla list --json         flotilla questions --json       remote-containers.attachToRunningContainer (local)
   flotilla --host H logs -f     flotilla answer …              opensshremotes.openEmptyWindow + attach (remote)
   flotilla submit --json        flotilla version --json        integrated terminal: flotilla logs -f / spawn
```

Six small, independently-testable modules. `cli.ts` is the only thing that knows how to invoke the
binary *and* the only thing that knows the JSON wire shape — it normalizes everything into one internal
`FleetState` so the rest of the extension never branches on "local vs remote" or "old vs new contract".
Everything privileged funnels through the host; the webview is pure render + intent.

## 4. The dashboard webview

A singleton `WebviewPanel` (re-focus if already open) opened in `ViewColumn.Active`. Layout
(mockup of the single-host case: [assets/2026-06-24-flotilla-vscode-extension-mockup.png](assets/2026-06-24-flotilla-vscode-extension-mockup.png);
editable source: [assets/2026-06-24-flotilla-vscode-extension-mockup.html](assets/2026-06-24-flotilla-vscode-extension-mockup.html)):

```
🚢 Flotilla  (editor tab)
┌──────────────────────────────────────────────────────────────────────────────┐
│ [＋ Spawn agent…]  [⟳ Refresh]                     updated 2s ago · poll 3s    │
├──────────────────────────────────────────────────────────────────────────────┤
│ ⚑ NEEDS ATTENTION                            (shown only when non-empty)        │
│  ⏸ wise-lynx  beefy/acme/api                                    blocked 5m     │
│     "Should I drop the legacy users_old table, or add a backfill migration?"    │
│     [ your answer……………………………………… ]  [Send]  [Stop agent]                     │
│  ✓ keen-fox   local/acme/api   PR #482 opened, not merged       done 1h        │
│     [Open PR on GitHub]  [Check out locally]  [Acknowledge]                      │
├──────────────────────────────────────────────────────────────────────────────┤
│ ▸ beefy            (host header; collapsed when only `local`)   ok · v0.4.0     │
│   acme/api · 2 agents                                                          │
│    ● brave-otter   running   2h14m     Attach · Logs · Submit · Stop · Rm      │
│      └ (reserved sub-line: objective · step N/M · current todo — see §14.1)    │
│    ⏸ wise-lynx     blocked   5m        Answer↑ · Attach · Logs · Stop · Rm     │
│ ▸ local                                                          ok · v0.4.0    │
│   acme/api · 1 agent                                                           │
│    ✓ keen-fox      done      1h02m     Open PR · Attach · Logs · Submit▸PR · Rm │
│ ! cloud1           connection refused                          (warning row)    │
└──────────────────────────────────────────────────────────────────────────────┘
status bar:  🚢 Flotilla · 5 agents · ●2 ⏸1 ✓1 ✕1 · 1 PR · ⚠cloud1   (amber)
```

- **Grouping: host → repo → agent.** The host level collapses to nothing when only the implicit
  `local` host is in play (the common, pre-federation case — identical to the mockup). Each host header
  shows reachability/version from the `hosts` health array; an unreachable or contract-blocked host
  renders as a **warning row** and never blanks the rest of the table.
- **Fleet rows.** status dot + name, status chip, age, and an always-visible action bar gated by status
  (§6). `Rm` is styled destructive.
- **Needs-attention section** at the top (§7) renders only when there's a pending question or an
  unacknowledged PR.
- **Theming.** Chips/dots use `ThemeColor` tokens (`charts.green/yellow/red/blue`) so they track the
  user's theme; the mockup PNG is hard-coded dark for illustration only.

## 5. Host ⇄ webview message protocol

The webview holds no privilege — it renders `FleetState` and emits intents.

**Host → webview** (on every poll and on inbox events):
```json
{ "type": "state",
  "updatedAt": "2026-06-24T08:17:02Z",
  "hosts":  [ { "name":"beefy","ok":true,"version":"0.4.0","contract":1 },
              { "name":"cloud1","ok":false,"error":"ssh exit 255: connection refused" } ],
  "agents": [ { "host":"beefy","name":"wise-lynx","repo":"acme/api","status":"blocked",
                "ageSeconds":300,"id":"<containerId>",
                "objective":null,"progress":null,
                "pr":null } ],
  "questions": [ { "host":"beefy","agent":"wise-lynx","id":"…","text":"Should I…?","ageSeconds":300 } ],
  "prs": [ { "host":"local","agent":"keen-fox","repo":"acme/api","branch":"flotilla/keen-fox",
             "prURL":"https://github.com/acme/api/pull/482","acknowledged":false } ] }
```

**Webview → host** (button clicks):
```json
{ "type":"action", "verb":"answer", "host":"beefy", "agent":"wise-lynx", "id":"…", "text":"Yes." }
```
`verb ∈ {spawn, refresh, attach, logs, submit, stop, rm, answer, openPR, checkoutPR, ackPR}`. The host
validates the (host, agent) exists in current state, performs the action (§6), then re-polls and pushes
fresh `state`.

### 5.1 JSON normalization (the federated-client seam)

`cli.ts` is the single place that tolerates the wire-shape change the remote slice introduces:

- **Bare array** (pre-federation, or a single remote engine invoked directly) — e.g. `flotilla list
  --json` → `[ {name,repo,status,created,id,logDir}, … ]`. Normalized by wrapping each row with
  `host:"local"`.
- **Wrapped object** (post-federation client layer, remote spec §8) — `{ "rows":[ {host,…}, … ],
  "hosts":[ {name,ok,error,version,contract}, … ] }`. Used directly.

Detection is structural (array vs object with a `rows` key), so the extension works **before and after**
the remote slice lands with no setting to flip. The extension also probes `flotilla version --json`
`{version, contract}` once at startup and warns (non-fatally) if the engine's `contract` is newer than
the shape it knows — the same contract integer the remote spec uses to guard the boundary. **`version`
is itself a remote-slice addition** (there is no `version` command in the root today), so the probe
treats a missing/unknown-subcommand result as *contract 0 / legacy bare-array* and proceeds — it never
hard-fails on an engine that predates the federated client.

## 6. Commands & lifecycle

All commands are registered both as palette commands (`flotilla.*`) and as webview intents. Single-agent
commands are routed to the owning host with `--host <host>` (the remote client resolves `local`
in-process), so the same code path serves local and remote.

| Action | Host implementation | Status gate |
|---|---|---|
| **Spawn** | `QuickPick` of hosts (when >1 registered) → `QuickPick` of profiles (`flotilla agents`) → `InputBox` repo URL → `InputBox` prompt → run `flotilla [--host H] spawn <repo> --agent <p> --prompt <…>` in a **visible integrated terminal**. | always |
| **Refresh** | re-poll immediately. | always |
| **Attach (local host)** | `executeCommand('remote-containers.attachToRunningContainer', <id>)` with the container `id` from `list --json` — this command accepts the container id as a string argument (the id prefix stays `remote-containers.*` even though the extension is now "Dev Containers"). On an older Dev Containers version that ignores the argument it falls back to the native picker. | running/blocked/exited (exited → starts first) |
| **Attach (remote host)** | open a **Remote-SSH** window to the host (the Remote-SSH "connect to host in new window" command — likely `opensshremotes.openEmptyWindow`, exact id confirmed in implementation — passing the host's ssh target), then run the attach there — per remote spec §9. If neither Remote-SSH nor Dev Containers is installed, show an actionable message with install links. | as above |
| **Logs** | `createTerminal` + `sendText('flotilla [--host H] logs <agent> -f')`. | always |
| **Submit** | `flotilla [--host H] submit <agent> --json`; render the resulting PR/branch URL as a toast with an "Open" action and add it to the PR list (§7). | done/running (running → confirm, maps to `--force`) |
| **Stop** | `flotilla [--host H] stop <agent>`. | running/blocked |
| **Rm** | `flotilla [--host H] rm <agent>` behind a confirmation modal. | any |
| **Answer** | `flotilla [--host H] answer <agent> --id <id> "<text>"` (§7). | blocked |
| **Open PR** | `env.openExternal(Uri.parse(prURL))`. | has PR |
| **Acknowledge PR** | mark `(host,agent,branch)` acknowledged in `globalState` (§7); the item leaves Needs-attention. | has PR |
| **Check out PR locally** | **Feature-gated on the GitHub Pull Requests extension** (`extensions.getExtension('github.vscode-pull-request-github')`). Shown only when that extension is installed; clicking delegates to it to open/checkout the PR by URL (exact command resolved in implementation), with `gh pr checkout <prURL>` in an integrated terminal as the dependency-light fallback. Hidden when absent — no dead UI. (§14.2 covers the harder remainder.) | has PR |
| **Inbox** | open a terminal running `flotilla inbox --watch` (raw event stream). | always |

**Two execution paths, both metachar-safe by construction:**
- **Non-interactive** (`list`/`questions`/`submit`/`answer`/`stop`/`rm` + the `version` probe) run via
  Node `child_process.execFile(binary, argv[])` in `cli.ts` — **no shell**, so repo URLs, prompts, and
  answer text can't be reinterpreted. This is the path for everything the dashboard parses.
- **Interactive / streaming** (`logs -f`, `spawn`) run in an integrated terminal. `sendText` *does* go
  through a shell, so any operator-supplied argument (notably the **spawn prompt**, free text) is
  shell-quoted before it's sent — the same metacharacter hazard the backlog already flags for engine-side
  prompt interpolation. Where a stream isn't needed, prefer `execFile` with output to an `OutputChannel`
  to avoid the quoting surface entirely.

## 7. Needs-attention: questions + finished PRs

One section collects everything that wants a human. Two item kinds:

**Blocked agents (questions)** — uses the shipped Q/A channel (`flotilla questions`/`answer`).
- **Surfacing.** The local `inboxWatcher` sees a `question` event → re-poll `flotilla questions --json`,
  push `state`, and raise `showInformationMessage("<agent> asks: …", "Answer…", "Open logs")`. For
  **remote** hosts there's no local inbox file, so questions surface on the **poll** of `flotilla
  questions --json` (which fans out over SSH) — no proactive toast until the future client↔engine socket
  (remote spec §13).
- **Answering.** Inline field → **Send** → `flotilla [--host H] answer <agent> --id <id> "<text>"`. On
  the `question_answered` event (local) or the next poll finding the question gone, the row clears and
  the agent leaves `blocked`. **Stop agent** maps to `flotilla [--host H] stop <agent>` — the Q/A spec's
  escape hatch.

**Done agents with an open PR (acknowledge / open)** — your "make me notice the PR" ask.
- **Source.** The daemon already emits `pr_opened` / `pr_updated` inbox events carrying
  `{branch, prURL}`, and `flotilla submit --json` returns `{agent,branch,prURL,created,pushOnly,note}`.
  The extension builds the PR list from these (local: the inbox watcher; remote: the `inbox --json`
  poll). An agent that is `done`/`exited` with a known PR URL that the operator hasn't acknowledged
  appears here.
- **Actions.** **Open PR on GitHub** (`openExternal`), **Acknowledge** (dismiss — see below), and
  **Check out locally** (shown only when the GitHub Pull Requests extension is installed; delegates to
  it, §6 / §14.2).
- **Acknowledgement is extension-local.** A set of `(host, agent, branch)` keys persisted in
  `globalState`; acknowledging removes the item from Needs-attention but never touches the engine or the
  PR. (No engine change — purely a "I've seen this" marker.) A `pushOnly` submission with no PR URL shows
  as "pushed `flotilla/<agent>` — open a PR on your host" with the branch name and no GitHub link.

## 8. Status model, polling & watching

- **Poll** `flotilla list --json` and `flotilla questions --json` every
  `flotilla.refreshIntervalSeconds` (default 3); when hosts are registered these fan out over SSH inside
  the CLI, so the extension still issues one command. Each `exec` is time-bounded; a transient failure
  keeps the last good state behind a muted "couldn't refresh" banner rather than blanking.
- **Status.** From `list --json` `status` (Docker state), with a **`blocked`** overlay for any agent
  present in `flotilla questions --json` (decoupled from whether `list` itself carries `blocked` yet).
- **Host health.** From the `hosts` array of the wrapped shape (§5); unreachable/contract-blocked hosts
  render as warning rows and feed the status-bar `⚠` indicator. Empty/absent (`local`-only) → no host
  chrome.
- **Watch** the **local** `~/.flotilla/inbox.jsonl` (path from `flotilla.home`) via
  `createFileSystemWatcher` for push `question`/`pr_opened`/`agent_done` events. The inbox lives
  *outside* the workspace, so the watcher is constructed with a `RelativePattern` rooted at an absolute
  base (the only form that watches non-workspace paths). It's best-effort sugar over the poll loop and
  **local-only by nature** (no remote filesystem); if the watcher yields no events on a given platform,
  the 3 s poll still catches everything. Remote hosts rely on the poll regardless. Documented plainly so
  the degradation isn't surprising.
- **Status-bar item** (`window.createStatusBarItem`): `🚢 N agents · ●R ⏸B ✓D ✕E · P PR · ⚠H`; when
  `B>0 || P>0 || H>0` it sets `backgroundColor = ThemeColor('statusBarItem.warningBackground')`; its
  command opens/focuses the dashboard.

## 9. Configuration

| Setting | Default | Purpose |
|---|---|---|
| `flotilla.binaryPath` | `flotilla` (on `PATH`) | absolute path to the engine/client binary |
| `flotilla.refreshIntervalSeconds` | `3` | fleet/questions poll cadence |
| `flotilla.home` | `~/.flotilla` | base dir for the **local** inbox/session paths the watcher reads |

Hosts themselves are **not** an extension setting — they live in the engine's `~/.flotilla/hosts.toml`
and are managed with `flotilla host add/ls/rm` (remote spec §6). The extension reads host health from the
wrapped `--json` shape; v1 does not edit the registry (deferred, §15). **Activation:**
`onCommand:flotilla.openDashboard` + `onStartupFinished` (status-bar item), no `*`.

## 10. Packaging, build & distribution

- **Location:** new top-level `clients/vscode/` (keeps the Go engine and the TS client cleanly
  separated; room for future clients under `clients/`).
- **Stack:** TypeScript, `@types/vscode` pinned to a conservative `engines.vscode` (e.g. `^1.85`),
  bundled with **esbuild** to a single `out/extension.js`. ESLint + `tsc --noEmit`.
- **Package:** `vsce package` → `flotilla-<version>.vsix`, installed via "Install from VSIX…" or
  attached to GitHub releases. No Marketplace publish in v1 (needs publisher account, icon, `LICENSE` —
  currently "all rights reserved" — and a CI publish step; backlog).
- **CI:** a `clients/vscode` job (`npm ci`, `tsc --noEmit`, `eslint`, `npm test`, `vsce package`),
  path-filtered to `clients/vscode/**` so it only runs when the client changes — mirroring how the Go
  jobs are scoped.

## 11. Error handling

| Condition | Behaviour |
|---|---|
| `flotilla` not found / not on `PATH` | actionable error with a button linking to `flotilla.binaryPath`; dashboard empty-state repeats the hint. |
| CLI exits non-zero | surface stderr in a notification; keep last good state; transient banner, never a blank table. |
| One host unreachable / contract-blocked | warning row in its host group + status-bar `⚠`; other hosts unaffected (mirrors the CLI's per-host non-fatal model). |
| Newer engine `contract` than the extension knows | non-fatal startup warning to update the extension; it still renders what it can parse. |
| Dev Containers / Remote-SSH extension absent (Attach) | message naming the needed extension (`ms-vscode-remote.remote-containers` / `…remote-ssh`) with install links. |
| Hung CLI | per-`exec` timeout; the poll tick is skipped, not blocked. |
| Daemon down | dashboard fully functional; only the local proactive toasts are missing (§7/§8). |

## 12. Testing

Unit tests are the bulk; the host side is fully testable with `vscode` mocked.

- **`cli.ts` normalization** — bare array → `host:"local"`; wrapped `{rows,hosts}` → used directly;
  malformed input dropped without losing last good state; `version --json` contract comparison.
- **Argument construction** — argv-style, metachar-safe, correct `--host`/`--id` placement for
  `answer`, `logs`, `submit`, `spawn`.
- **`fleet.ts`** — status model incl. `blocked` overlay; PR list assembly from `pr_opened`/submit JSON;
  acknowledgement filtering; host-health mapping; age computation; last-good-state retention.
- **`dashboard.ts`** — `FleetState` → render payload (host→repo→agent grouping; host collapse when
  `local`-only); intent → action dispatch (right `flotilla` invocation per `verb`); `rm` confirmation.
- **`inboxWatcher.ts`** — JSONL parsing incl. `question`/`question_answered`/`pr_opened`; debounced
  re-poll.
- **`ack.ts`** — globalState round-trip; ack survives reload; keyed by `(host,agent,branch)`.
- **Integration** (self-skips when `flotilla` isn't on `PATH` or there's no display): spawn an agent,
  assert it appears; answer a question end-to-end; submit → PR appears in Needs-attention → acknowledge
  clears it.
- A thin **webview smoke test** that the HTML loads and posts a well-formed intent.

## 13. Sequencing & dependencies

1. **Shipped, hard dependencies (all exist in `internal/cli` today):** `flotilla list --json`,
   `attach`, `logs -f`, `inbox`, `submit --json`, and the **Q/A channel** (`questions --json`, `answer`,
   `question`/`question_answered` events). The dashboard, attach, logs, watcher, PR list, **and the
   Questions section** all rely on these and can be built now.
2. **Soft — remote/federated client** (`{rows,hosts}` shape, `--host`/`--all-hosts`, `version --json`
   `contract`, Remote-SSH attach): the extension runs against the bare-array local shape today and
   transparently gains the host dimension once the federated client lands, via the §5.1 normalization.
   The remote spec (§9, §15.4) was written to this integration surface, so the two are designed to fit.
   Note `flotilla version --json` does **not** exist yet (no `version` command in the root today) — the
   §5.1 probe treats its absence as the legacy/pre-federation contract.
3. **Soft — objective/progress extractor** (§14.1): rendered when present.

The extension can be **built now** against the current local CLI (the full fleet view, lifecycle,
Questions, and PR sections); the remote and objective/progress slices enrich it as they land, neither
blocks the build.

## 14. Deferred (slots reserved, contracts defined)

### 14.1 Per-agent objective & progress sub-line
A muted sub-line per agent row showing the **objective** (task prompt) and **progress** (`step N/M` +
current todo). The **data already exists on the host** — the transcript is bind-mounted at
`session/transcript/` (logs spec) and the prompt is known at spawn — but *extraction* is an engine-side,
agent-specific concern that must not live in the thin client:
- **Objective.** Today the prompt is injected only into the container (`~/.flotilla/prompt` →
  `$FLOTILLA_PROMPT`), not persisted host-side nor in `list --json`. Needs a small engine change (write
  the operator's prompt to the host session dir + expose it). Cheap.
- **Progress.** No engine concept exists (only the launch wrapper's `running`/`done` status).
  `step N/M` + current todo must be parsed from the transcript, whose format is **agent-specific**
  (Claude Code's JSONL `TodoWrite` stream ≠ Codex's). Belongs **behind the profile abstraction** as a
  per-profile "progress extractor," exposed via enriched `list --json` or a new `flotilla status
  <agent> --json`. Its own short companion spec.

**This spec's commitment:** the dashboard reserves the sub-line and defines the optional render fields
`objective: string|null` and `progress: {step,total,label}|null` (§5); rendered when present, absent
otherwise.

### 14.2 Check out a finished PR locally in VS Code
**In v1, feature-gated** (§6): when the **GitHub Pull Requests** extension
(`github.vscode-pull-request-github`) is installed, the PR item shows a **Check out locally** button
that delegates to that extension to open/checkout the PR by URL (with `gh pr checkout <prURL>` in a
terminal as the fallback); when it's absent, the button is hidden. Feature detection is the gate, so the
extension never reimplements git or shows a dead control.

**Deferred remainder:** the *frictionless* path — auto-resolving the operator's local clone of the repo
when no matching folder is open (the engine clone is engine-side / possibly on a remote host), and
checking out a PR that belongs to a **remote** host's engine (which would route through a Remote-SSH
window first). v1's gated delegation covers the common case (the repo is open locally); these harder
cases are follow-ups.

## 15. Out of scope (future)

- **Marketplace publish** (publisher account, icon, `LICENSE`, CI publish on tag).
- **Editing the host registry from the extension** (`flotilla host add/rm` UI) — v1 reads host health
  only; manage hosts via the CLI.
- **A persistent client↔engine socket for true remote push** — until then remote hosts poll (§8); this
  tracks the remote spec's own deferred socket (§13 there).
- **Spawn config-file picker / profile editor / egress-allowlist editing** — v1 spawn is the native
  prompts.
- **Transcript rendering inside VS Code** — use native Attach or the `logs -f` terminal.
- **Structured/multiple-choice question answering** — mirrors the Q/A spec's out-of-scope (v1 free-text).
- **Activity-bar tree** — explicitly replaced by the editor tab + status-bar item.
```
