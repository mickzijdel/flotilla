# Flotilla

Flotilla is a manager for a fleet of sandboxed, containerized coding agents — each agent runs in its own Docker container on an engine-side clone of a repo, with no git credentials inside the container.

## Status

The CLI lifecycle (spawn/list/attach/stop/rm/submit/logs) is functional, and a spawned agent is now actually runnable: the engine provisions the repo's devcontainer with a vendored toolchain Feature (`devcontainer up --additional-features`), injects the agent token via a `0600` env-file and config into the container, and launches the agent — with git credentials never entering the container. The egress firewall and submission flow are both shipped. `flotilla submit <agent>` pushes the agent's commits to a `flotilla/<agent>` branch (force-with-lease) and opens or updates a PR via the `gh` CLI (`gh pr create --fill`), with a push-only fallback that prints a GitHub compare URL when `gh` is absent or unauthenticated. Submit is status-gated: it refuses a still-running agent (override with `--force`) and refuses a dirty working tree or zero new commits. Git credentials never enter the container — the engine does all push and PR work from the host-side clone. A `wrap_up` prompt contract (and a Claude Stop hook) guides agents to commit their work before exiting. `attach` now auto-starts an exited container. Per-session logs persist under `~/.flotilla/logs/<repo>/<YYYY-MM-DD-HHMM>-<agent>/`: a live transcript bind-mount, a teed `container.log`, and a daemon-free `status` file. An optional [daemon](#daemon-optional-supervisor) supervisor auto-submits a PR when an agent finishes and records events to an operator inbox — the CLI is unchanged and works without it.

## Installation

Requires Go 1.26+ and a running Docker daemon. Build the binary:

```bash
go build -o bin/flotilla .
```

## Usage

List available agent profiles, then spawn an agent on a repo:

```bash
./bin/flotilla agents
./bin/flotilla spawn https://github.com/octocat/Hello-World.git --prompt "noop"
```

List the fleet (add `--json` for machine-readable output), print attach info (a `docker exec` line and a VS Code hint), stop, remove, or submit the agent's work as a PR:

```bash
./bin/flotilla list
./bin/flotilla attach <name>
./bin/flotilla stop <name>
./bin/flotilla rm <name>
./bin/flotilla submit <name>
./bin/flotilla submit <name> --json  # machine-readable output
./bin/flotilla logs <name>           # stream container.log (-f follows until done)
```

`submit` pushes the agent's commits to a `flotilla/<name>` branch and opens a PR via `gh` (or prints a compare URL if `gh` is unavailable). It refuses a still-running agent unless `--force` is passed. Add `--json` for machine-readable output (matches `list --json` style).

`logs` streams the agent's `container.log` from `~/.flotilla/logs/<repo>/<date>-<name>/`. Pass `-f` to follow the log live until the agent exits.

Run `flotilla doctor` to check prerequisites; it also reports an advisory `gh` availability check — submit still works push-only without `gh` — and whether the daemon is running.

## Daemon (optional supervisor)

`flotilla` is a stateless CLI: every command talks straight to Docker + `~/.flotilla` and works with no daemon running. The **daemon** is an optional long-running supervisor that watches agents and reacts to events — its anchor behaviour is **auto-submitting a PR when an agent finishes**:

```bash
./bin/flotilla daemon start           # start in the background (spawn auto-starts it too)
./bin/flotilla daemon status [--json] # running?, pid, watched agents, recent events
./bin/flotilla daemon stop            # SIGTERM + wait for clean shutdown
./bin/flotilla inbox [--json] [--watch] [--since <RFC3339>]
```

- **Auto-submit on done.** When an agent's launch-wrapper writes `status` → `done`, the daemon reuses `flotilla submit` (force-with-lease push + PR) on a clean tree with ≥1 commit. A dirty tree or zero commits is recorded as a skip — it never force-commits. Idempotent via a per-agent last-submitted SHA, so a daemon restart won't re-open a PR.
- **Inbox.** Notable events (`agent_done`, `pr_opened`, `pr_updated`, `submit_skipped`) are appended to `~/.flotilla/inbox.jsonl`; read them with `flotilla inbox` (`--watch` tails).
- **Lifecycle.** Self-daemonizing (double-forks, pidfile, single-instanced via `flock`); `flotilla spawn` best-effort auto-starts it. `systemd --user` users can run the non-forking `flotilla daemon run` instead. On a `flotilla` upgrade the running daemon re-execs itself from the new binary.
- **Everything works without it** — you only lose the reactive behaviours; `flotilla submit` stays the manual fallback.

## Development

Provision the pinned toolchain (hk, gitleaks, go, golangci-lint, node, …) with mise, then install the pre-commit hooks:

```bash
mise install
hk install
```

Build, test, and lint:

```bash
go build ./...
go test ./...
golangci-lint run ./...
```

Run the full local check suite (linters, shell checks, duplication audit, gitleaks) before pushing:

```bash
hk run check
```

## Design and plan

- Design spec: [docs/specs/2026-06-14-flotilla-design.md](docs/specs/2026-06-14-flotilla-design.md)
- Implementation plan: [docs/plans/2026-06-14-flotilla-engine-skeleton.md](docs/plans/2026-06-14-flotilla-engine-skeleton.md)

## License

Not yet licensed. All rights reserved pending a license decision.
