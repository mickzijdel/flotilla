# Flotilla Backlog

Running list of deferred work and known issues, captured so future sessions/plans pick them up.
The v1 walking skeleton (engine: spawn/list/attach/stop/rm over a local Docker backend, engine-side
clone, claude+codex profiles) is merged. See [the design spec](specs/2026-06-14-flotilla-design.md)
and [the skeleton plan](plans/2026-06-14-flotilla-engine-skeleton.md) for what's already built.

## Next plans (deferred by design — each its own spec → plan → build cycle)

Roughly in dependency order:

- ~~**devcontainer.json + Feature overlay + credential/config injection.**~~ **Done** — see
  [plan](plans/2026-06-15-flotilla-devcontainer-injection.md) and
  [spec](specs/2026-06-15-flotilla-devcontainer-injection-design.md).
- ~~**Egress firewall** — default-deny egress with a per-profile `EgressAllow` allowlist
  (global default + per-project override).~~ **Done** — out-of-container proxy model (per-agent
  `ubuntu/squid` sidecar on a `--internal` network, agent network-swapped onto it). See
  [plan](plans/2026-06-15-flotilla-egress-firewall.md) and
  [spec](specs/2026-06-15-flotilla-egress-firewall-design.md).
- ~~**Submission flow** — push/PR only (agents never push to protected branches directly), plus the
  `DoneSignal` handling so the engine knows when an agent finished.~~ **Done** — `flotilla submit
  <agent>` pushes to `flotilla/<agent>` (force-with-lease) and opens/updates a PR via `gh --fill`
  (push-only compare-URL fallback). Status-gated; `wrap_up` prompt contract + Claude Stop hook
  guides agents to commit before exit; `attach` auto-starts exited containers. See
  [spec](specs/2026-06-23-flotilla-submission-flow-design.md) and
  [plan](plans/2026-06-23-flotilla-submission-flow.md).
- ~~**Logs / transcript mounts** — persist per-session logs + the agent transcript to a host dir
  under `~/.flotilla/logs/<repo>/<YYYY-MM-DD-HHMM>-<agent>/` (live transcript bind-mount,
  teed `container.log`, daemon-free `status`), plus `flotilla logs <agent> [-f]`.~~ **Done** — the
  launch-wrapper `status` → `done` marker is the daemon's done-signal (the daemon builds on it).
  See [spec](specs/2026-06-23-flotilla-logs-transcript-mounts-design.md) and
  [plan](plans/2026-06-23-flotilla-logs-transcript-mounts.md).
- ~~**Daemon (supervisor)** — an optional long-running process that watches agents and reacts:
  auto-submit a PR on done, an operator inbox, and the request-handler seam.~~ **Done** — additive
  supervisor (CLI unchanged, works without it): polls the logs-tree `status` → `done` marker (with a
  `docker events` die/stop fallback) and reuses `flotilla submit` (SHA-deduped, never force-commits);
  `~/.flotilla/inbox.jsonl` + `flotilla inbox`; state mirror under `~/.flotilla/daemon/`; self-daemonizing
  (`flock` + pidfile, `daemon start|stop|status|run`, re-exec on upgrade); `spawn` best-effort auto-start;
  request-handler seam scaffolded (no real handler yet — that's on-demand fetch). Socket/broker deferred
  to a later Option-C upgrade. See [spec](specs/2026-06-23-flotilla-daemon-design.md) and
  [plan](plans/2026-06-23-flotilla-daemon.md).
- ~~**On-demand fetch/pull** — let a running agent (no creds in container) ask the engine to `git fetch`
  `origin` into its bind-mounted clone mid-session; the new refs are live in the container instantly and
  the agent integrates locally.~~ **Done** — fetch-only primitive (`gitops.Fetch` =
  `git fetch --prune origin`, working-tree-neutral). Two triggers, one primitive: daemon-independent
  `flotilla fetch <agent>` (`Fleet.Fetch`, host-side), and an in-container `flotilla-fetch` shim that
  rides the daemon's request-handler seam (terminal `fetch` handler → `fetch_done` inbox event). Shim
  injected on `PATH` at spawn; a constant fetch-awareness preamble is appended to every agent prompt.
  See [spec](specs/2026-06-23-flotilla-on-demand-fetch-design.md) and
  [plan](plans/2026-06-24-flotilla-on-demand-fetch.md).
1. **Agent question/answer channel** — a running agent asks its operator a question
   (`flotilla-ask "…"`) and blocks for the reply; the operator answers with `flotilla answer <agent>
   "…"`. Rides the daemon's request-handler seam (notify via inbox + `flotilla questions`), realises
   the deferred **`blocked`** status, and the answer path is daemon-independent. Spec drafted:
   [agent question channel](specs/2026-06-23-flotilla-agent-question-channel-design.md).
2. **CLI-driver skill** — a skill modelled on playwright-cli so agents can drive `flotilla` (the
   CLI is the primary control surface; the skill sits on top).
3. **VS Code extension** — UI over the CLI for managing multiple agents across repos at once.
4. **Remote backend** — `DOCKER_HOST` over TLS/SSH for multi-machine; the `Backend` interface seam
   is already in place. Docker Sandboxes / `sbx` could be added as an additional backend once it
   lands on Linux (see spec §7).

## Known issues / robustness (surfaced in the skeleton's final review — deferred, not blocking)

- **README oversells "functional."** `README.md` `## Status` is now more accurate: the lifecycle
  (spawn/list/attach/stop/rm/submit), a runnable agent (devcontainer + injection), the default-deny
  egress firewall, and the submission flow all work.
- **No LICENSE.** README notes "all rights reserved pending a decision." Pick a license.
- **Prompt → `sh -c` shell-quoting hazard** (`internal/agent/profile.go` `RenderLaunch`). The
  prompt is interpolated verbatim into the launch template run via `sh -c`; a prompt with shell
  metacharacters (`"`, `$`, backtick) breaks/alters the command. Documented in code. Fix by passing
  the prompt out-of-band (env var or argv) instead of string interpolation.
- **Name-collision race** (`Spawn`: `List` → `naming.Pick` → `Up`, no lock). Two concurrent
  spawns can pick the same name; the loser's labelled `devcontainer up` / clone fails loudly, so
  blast radius is small. Revisit for the concurrent/remote backend.
- **Stale work-dir orphans collide on clone** (surfaced in live verification). `naming.Pick` avoids
  names of *running* containers (via `List`), but an orphaned on-disk clone left in
  `~/.flotilla/work/<name>` (e.g. from an interrupted run) isn't reflected there, so the next spawn
  can pick that name and `git clone` fails with "destination path already exists." Fix: have `Spawn`
  detect/clean a pre-existing `dest`, and/or fold the work dir into name avoidance.
- **Repos using the root `.devcontainer.json` form** (not `.devcontainer/devcontainer.json`). The
  toolchain Feature is overlaid as a local Feature under `<clone>/.devcontainer/`, referenced
  relative to that folder — which the devcontainer CLI requires. A repo whose config is the root
  `.devcontainer.json` variant will get a flotilla default `.devcontainer/devcontainer.json` that
  shadows it. Rare; handle by detecting that form (or publish the Feature to GHCR so the overlay no
  longer needs a local path).
- **Launch `cd` uses a `/workspaces/*` glob** (`launchWrapper`, best-effort). Correct for the
  devcontainer default and the bundled default config; a repo whose devcontainer sets a non-default
  `workspaceFolder` (outside `/workspaces`) won't have the agent `cd`'d into it. Robust fix: capture
  `remoteWorkspaceFolder` from `devcontainer up`'s JSON output and pass it explicitly.
- **Toolchain Feature re-installs every spawn** (no image-layer caching across agents). The vendored
  local Feature is rebuilt per container. Promote to a prebuilt base image or a GHCR-published
  Feature (spec §4.3 / §10) so node/gh/mise are cached.
- **`parseLabels` splits on `,`** (`internal/backend/docker.go`). Corrupts any label value
  containing a comma. Today's labels are comma-free; `flotilla.repo` (arbitrary URL) is the closest
  risk. Revisit when label values get richer.

## Test-coverage gaps to close as features land

- `parseLabels` / `parseDockerTime` are only covered via the live Docker integration path; add unit
  tests with sample `docker ps` JSON.
- End-to-end live transcript mount: a self-skipping `devcontainer up` test that spawns through the
  full fleet path and asserts the transcript dir, `container.log`, and `status` appear on the host
  (design spec §11). The new backend primitives (`CopyFrom`, `ReadConfig`) and all fleet logic are
  unit-covered via `backend.Fake`; only the real-container live-mount round-trip is deferred.

## Repo hygiene

- Apply Mick's dev-env standard to this repo (mise tool pinning — Go is pinned; add hk pre-commit
  running linters/tests + gitleaks, a CI workflow mirroring it, and README/CLAUDE.md version notes).
  Use the `dev-hooks:dev-env-setup` skill.
