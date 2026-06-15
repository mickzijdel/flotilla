# Flotilla

Flotilla is a manager for a fleet of sandboxed, containerized coding agents — each agent runs in its own Docker container on an engine-side clone of a repo, with no git credentials inside the container.

## Status

The walking skeleton is functional. The `flotilla` CLI can `spawn`, `list`, `attach`, `stop`, and `rm` agents, and list available `agents` profiles. It is backed by a local Docker backend and ships with `claude` and `codex` built-in agent profiles.

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

List the fleet (add `--json` for machine-readable output), print attach info (a `docker exec` line and a VS Code hint), then stop and remove the container:

```bash
./bin/flotilla list
./bin/flotilla attach <name>
./bin/flotilla stop <name>
./bin/flotilla rm <name>
```

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
