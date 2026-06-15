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
1. **Egress firewall** — default-deny egress with a per-profile `EgressAllow` allowlist
   (global default + per-project override). Compact code → small default budget.
2. **Submission flow** — push/PR only (agents never push to protected branches directly), plus the
   `DoneSignal` handling so the engine knows when an agent finished.
3. **Logs / transcript mounts** — persist per-session logs + the agent transcript
   (`TranscriptPath`) to a host dir under `~/.flotilla`, with a good date+repo naming convention;
   consider a mounted read-only volume for live inspection.
4. **On-demand fetch/pull** — let a running agent request the engine re-fetch/pull during a session
   (engine-side, no creds in container).
5. **CLI-driver skill** — a skill modelled on playwright-cli so agents can drive `flotilla` (the
   CLI is the primary control surface; the skill sits on top).
6. **VS Code extension** — UI over the CLI for managing multiple agents across repos at once.
7. **Remote backend** — `DOCKER_HOST` over TLS/SSH for multi-machine; the `Backend` interface seam
   is already in place. Docker Sandboxes / `sbx` could be added as an additional backend once it
   lands on Linux (see spec §7).

## Known issues / robustness (surfaced in the skeleton's final review — deferred, not blocking)

- **README oversells "functional."** `README.md` `## Status` is now more accurate: the lifecycle
  (spawn/list/attach/stop/rm) **and** a runnable agent (devcontainer + injection) work. Egress
  firewall and submission/PR flow are still pending (next plans #1 and #2).
- **No LICENSE.** README notes "all rights reserved pending a decision." Pick a license.
- **Prompt → `sh -c` shell-quoting hazard** (`internal/agent/profile.go` `RenderLaunch`). The
  prompt is interpolated verbatim into the launch template run via `sh -c`; a prompt with shell
  metacharacters (`"`, `$`, backtick) breaks/alters the command. Documented in code. Fix by passing
  the prompt out-of-band (env var or argv) instead of string interpolation.
- **Name-collision race** (`Spawn`: `List` → `naming.Pick` → `Create`, no lock). Two concurrent
  spawns can pick the same name; the loser's `docker create --name` fails loudly (and now its clone
  dir is cleaned up), so blast radius is small. Revisit for the concurrent/remote backend.
- **`parseLabels` splits on `,`** (`internal/backend/docker.go`). Corrupts any label value
  containing a comma. Today's labels are comma-free; `flotilla.repo` (arbitrary URL) is the closest
  risk. Revisit when label values get richer.

## Test-coverage gaps to close as features land

- `parseLabels` / `parseDockerTime` are only covered via the live Docker integration path; add unit
  tests with sample `docker ps` JSON.

## Repo hygiene

- Apply Mick's dev-env standard to this repo (mise tool pinning — Go is pinned; add hk pre-commit
  running linters/tests + gitleaks, a CI workflow mirroring it, and README/CLAUDE.md version notes).
  Use the `dev-hooks:dev-env-setup` skill.
