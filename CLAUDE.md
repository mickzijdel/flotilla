# Flotilla

Flotilla is a Go CLI that manages a fleet of sandboxed, containerized coding agents: each agent runs in its own Docker container on an engine-side fresh clone of a repo, with no git credentials inside the container. The engine (control plane) owns clone/push/PR so submission is "PR-only" by construction; agents are described by declarative TOML profiles (Claude and Codex ship built-in) so new CLI agents drop in with no code changes. The walking skeleton is functional: `spawn`, `list`, `attach`, `stop`, `rm`, and `agents`, backed by a local Docker backend abstracted behind a `Backend` interface (with an in-memory fake for tests). See README.md, docs/specs/2026-06-14-flotilla-design.md, and docs/plans/2026-06-14-flotilla-engine-skeleton.md for the full design.

## Key package versions

- Go: 1.26.4 (`go` directive in go.mod)
- github.com/spf13/cobra: v1.10.2 (CLI)
- github.com/BurntSushi/toml: v1.6.0 (agent profiles)

## Development

Toolchain is pinned via mise (mise.toml): hk, pkl, gitleaks, go, golangci-lint, node. Run `mise install` to provision; tools are checksum-locked in mise.lock.

- Build: `go build ./...`
- Test: `go test ./...` — the backend integration test in `internal/backend` self-skips when Docker is unavailable; CI runs it with `-race`.
- Lint: `golangci-lint run ./...` and format check `golangci-lint fmt --diff` (config in `.golangci.yml`, v2 schema). Auto-fix/format: `golangci-lint fmt` and `golangci-lint run --fix ./...`.
- Pre-commit hooks via hk (hk.pkl): run `hk install` once. `hk run check` runs the full suite (golangci-lint, shellcheck/shfmt on shipped shell scripts, jscpd duplication audit, gitleaks, large-file check). Pre-commit auto-fixes formatting.
- CI mirrors these checks in `.github/workflows/ci.yml` (lint, test, gitleaks, audit jobs).

## Layout

- `main.go` — entrypoint; builds a `Fleet` over the Docker backend and runs the cobra root.
- `internal/` — the packages:
  - `agent` — profile model, TOML loader, embedded built-in profiles
  - `backend` — `Backend` interface, shared types, Docker impl, in-memory fake
  - `cli` — cobra command wiring
  - `fleet` — `Spawn`/`List`/`Attach`/`Stop`/`Remove` orchestration
  - `gitops` — engine-side fresh clone
  - `naming` — curated word-list agent naming
