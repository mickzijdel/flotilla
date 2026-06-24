# Flotilla — Operator skill (CLI-driver skill)

**Date:** 2026-06-24
**Status:** Draft for review
**Scope:** A Claude Code **skill** that teaches a host-side agent to drive the `flotilla` CLI to run
and supervise a fleet — spawn agents across repos, watch them, answer their questions, fetch refs in,
and submit PRs. Modelled on the `playwright-cli` skill: a thin, command-reference-shaped layer over an
already-complete CLI. No engine changes; the CLI *is* the control surface and the skill sits on top.

## 1. Goal

The `flotilla` binary already exposes the whole operator surface (§4). What's missing is the
*knowledge layer* that lets a Claude session reach for it correctly: which command to run, what its
JSON shape is, how the status model works, and the small number of multi-step workflows that matter
(spawn → watch → answer → submit). Today an operator agent has to rediscover all of that from `--help`
and source. This skill encodes it once, the way `playwright-cli` encodes browser automation.

The audience is the **host operator**: a Claude session running on the machine (laptop or remote host)
where the flotilla engine lives, managing a fleet on the human's behalf. It is explicitly **not** the
in-container agent — see §3 and §7.

## 2. Why a skill (and why this shape)

- **The CLI is stable and complete.** `spawn`/`list`/`agents`/`attach`/`stop`/`rm`/`submit`/`logs`/
  `fetch`/`inbox`/`questions`/`answer`/`doctor`/`daemon` are all shipped and tested. The skill
  documents a moving-free target; it adds no code paths of its own.
- **`playwright-cli` is the proven template.** A short SKILL.md with a Quick-start, a flat command
  reference grouped by area, and a handful of worked end-to-end examples. We mirror that structure so
  the skill is skimmable and the agent can pattern-match a task to a command fast.
- **No dependency on the remote backend.** The remote-backend slice is purely additive — it adds
  `--host`/`--all-hosts` selection and a `flotilla host` group, and remote engines emit the *same* bare
  JSON shapes the skill already documents (the `{rows, hosts}` wrap is client-side). So this skill can
  be written and shipped now against the single-host CLI; remote addressing folds in later as one
  forward-looking section (§7), not a blocker.

## 3. Decisions locked

| # | Area | Decision |
|---|------|----------|
| 1 | Audience | **Host operator only.** A Claude session driving the full `flotilla` binary from the engine host. The in-container agent surface (the `flotilla-ask` / `flotilla-fetch` shims) is a **separate, deferred** concern (§7), already covered by auto-injected prompt preambles. |
| 2 | Shape | **`playwright-cli`-style SKILL.md**: Quick-start, command reference grouped by lifecycle/supervision/submission, then 3–4 worked examples. Reference, not tutorial. |
| 3 | Source of truth | The skill documents the **`--json` outputs and exit semantics** as the contract an agent parses — never screen-scraped human output. Where a command has both, the skill steers the agent to `--json`. |
| 4 | Status model | The skill teaches the real status model explicitly (§5): `running` / `blocked` / `exited` from `list`, the session `status` file's `running`→`done`, and `submit`'s exited-gating. |
| 5 | Daemon stance | The skill states the daemon is **optional** — everything works without it; with it, `done` auto-submits and `inbox` populates. It tells the agent to check `flotilla doctor` / `daemon status` rather than assume. |
| 6 | No engine changes | This slice ships **only** the skill (a SKILL.md + any examples). If a gap in the CLI surfaces while writing it, that's a separate backlog item, not folded in here. |

## 4. Command surface the skill covers

Grounded in `internal/cli` as shipped. Grouped the way the SKILL.md will group them.

**Discovery / preflight**
- `flotilla doctor` — checks docker, docker daemon, devcontainer CLI, `gh` auth, daemon state. Run
  first; non-zero exit ⇒ missing prerequisites.
- `flotilla agents` — list available agent profiles (built-ins: `claude`, `codex`).

**Lifecycle**
- `flotilla spawn <repo> [--agent claude] [--prompt "…"] [--no-egress-firewall]` — engine-side clone +
  start. Prints `name<TAB>status<TAB>id`. Best-effort auto-starts the daemon.
- `flotilla list [--json]` — the fleet. Human: `name<TAB>status<TAB>repo`. JSON: array of agent
  objects (`name`, `repo`, `status`, `created`, `id`).
- `flotilla attach <agent>` — prints the `docker exec …` line and VS Code attach info (auto-starts an
  exited container).
- `flotilla stop <agent>` / `flotilla rm <agent>` — stop / remove the container.

**Supervision (operator inbox + Q/A)**
- `flotilla inbox [--json] [--watch] [--since RFC3339]` — daemon events (`agent done`, `PR opened`,
  `submit skipped`, `question`, `fetch_done`, …). JSONL with `--json`; live with `--watch`.
- `flotilla questions [--json] [--watch]` — agents currently **blocked** awaiting an answer. Rows are
  `agent<TAB>id<TAB>age<TAB>text`.
- `flotilla answer <agent> <text> [--id <qid>]` — unblock an agent. `--id` only needed when several
  questions are pending for that agent.
- `flotilla logs <agent> [-f] [--json]` — stream `container.log`; `-f` follows until the session
  status reads `done`; `--json` returns `{logDir, status, transcript}` metadata instead.
- `flotilla fetch <agent>` — re-`git fetch origin` into a running agent's clone (the container holds no
  git credentials), so the agent can integrate new refs locally.

**Submission**
- `flotilla submit <agent> [--force] [--json]` — push `flotilla/<agent>` (force-with-lease) and
  open/update a PR via `gh`. **Exited-gated**: refuses a still-running agent unless `--force`. JSON:
  `{branch, prURL, created, pushOnly, note}`. Falls back to a push-only compare URL when `gh` is
  unavailable.

**Daemon (optional supervisor)**
- `flotilla daemon start|stop|status [--json]` — the auto-submit + inbox supervisor. `status` reports
  `{running, pid, watchedAgents, recent[]}`.

## 5. The status model the skill must teach

This is the part an operator agent most often gets wrong, so the skill calls it out explicitly:

- **`list` statuses:** `running`, `exited`, and a derived **`blocked`** overlay — a `running` agent
  that has a pending operator question (`internal/fleet/fleet.go`). `blocked` ⇒ go run
  `flotilla questions` and `flotilla answer`.
- **Session `status` file:** `running` → `done` (what `logs -f` watches and the daemon's auto-submit
  trigger keys on). `done` is the agent's *finished* signal; `exited` is the *container* state.
- **Submission gate:** `submit` requires the container to be `exited` (the agent stopped) unless
  `--force`. The skill tells the agent to let an agent finish (or `stop` it) before submitting, and to
  reach for `--force` deliberately.
- **No-creds-in-container invariant:** the agent can't `git fetch`/push itself — that's why `fetch`
  and `submit` are *operator* verbs. The skill states this so the agent doesn't try to drive git
  inside the container.

## 6. SKILL.md outline (deliverable)

Mirrors `playwright-cli`:

1. **Frontmatter** — `name: flotilla-operator`; a `description` that triggers on "run an agent on this
   repo", "spawn a flotilla agent", "manage my fleet", "submit the agent's work", "what are my agents
   doing", "answer the agent's question".
2. **Quick start** — doctor → spawn → list → logs -f → answer → submit, as a copy-pasteable strip.
3. **Command reference** — the §4 groups, each command with its flags, output shape, and the one-liner
   of when to use it. `--json` shapes shown inline.
4. **The status model** — §5, condensed.
5. **Worked examples** (3–4):
   - *Run an agent and ship it*: spawn → watch `logs -f` → on `done`, `submit --json`, report the PR.
   - *Supervise a blocked fleet*: `list --json` → for each `blocked`, `questions` → `answer`.
   - *Bring an agent up to date*: `fetch <agent>` after upstream moves, confirm via inbox.
   - *Triage with the daemon*: `daemon status` → `inbox --watch` to react to `done` / `PR opened`.
6. **Gotchas** — exited-gating on submit; daemon optional; prompts/answers with shell metacharacters
   (quote them — the documented `sh -c` interpolation hazard); `--id` needed only on multiple pending
   questions.

## 7. Out of scope / deferred

- **In-container agent skill (`flotilla-agent`).** Inside a flotilla container the agent has only the
  injected `flotilla-ask` / `flotilla-fetch` shims and the submission contract (`wrap_up`,
  commit-before-exit) — and the engine **already auto-appends ask/fetch awareness preambles to every
  agent prompt** (the Q/A-channel and on-demand-fetch slices). So a second skill there would largely
  duplicate the preambles. Deferred deliberately; only worth building if recursive/nested flotilla
  (an agent spawning its own sub-fleet) becomes real. Tracked as its own backlog item.
- **Remote addressing.** When the remote-backend slice lands, agents become `host:agent` and commands
  gain `--host` / `--all-hosts`. This is **additive** — the skill gains one "Multi-host" section
  noting the prefix and flags; nothing in §4–§6 changes. Not a dependency of this slice.
- **VS Code extension.** A separate view-plane spec; it consumes the same CLI/JSON surface this skill
  documents. No overlap in deliverable.

## 8. Verification (Always-Works)

A skill is prose, but the prose makes *factual* claims about the CLI — those get verified, not
eyeballed:

- **Every command + flag in the skill exists.** Cross-check each against `flotilla <cmd> --help` (or
  `internal/cli`). A flag the skill names that the binary lacks is a bug in the skill.
- **Every `--json` shape is real.** Run each `--json` command against a live (or faked) fleet and diff
  the documented shape against actual output — `list`, `submit`, `logs`, `questions`, `inbox`,
  `daemon status`.
- **The Quick-start strip runs end-to-end** against a real repo on a Docker-capable host: doctor →
  spawn → list → logs -f → (answer if it asks) → submit, observing the PR/compare URL with our own
  eyes. Self-skips where Docker/`gh` is unavailable, mirroring the existing integration-test pattern.
- **Trigger check.** The `description` actually fires the skill on the example phrases in §6.1 (a
  quick skill-eval, per `skill-creator`).

## 9. Sequencing & dependencies

1. **CLI surface (done).** Nothing to build in the engine; §4 is all shipped.
2. **Write the SKILL.md** per §6, grounded in `internal/cli`.
3. **Verify** per §8 (live round-trip on a Docker host + JSON-shape diffs + trigger eval).
4. **Land it** in the project's skill location (alongside the repo, or the user's skills dir per the
   build plan). No engine commit required.

Independent of the remote backend (§7) and the VS Code extension; can proceed immediately.
