# Flotilla — devcontainer + Feature overlay + credential/config injection

**Date:** 2026-06-15
**Status:** Draft for review
**Scope:** "Next plan #1" from [the backlog](../backlog.md) — the piece that makes a spawned
agent *actually runnable*. Builds on the merged walking skeleton (see
[the design spec](2026-06-14-flotilla-design.md) and
[the skeleton plan](../plans/2026-06-14-flotilla-engine-skeleton.md)).

## 1. Summary

Today `Spawn` clones a repo engine-side and runs the agent's launch command directly as a
container's main process over a bare base image. Nothing wires the profile's `Env`, `Install`,
`Setup`, or `config_mounts`, so a spawned `claude`/`codex` container has no agent CLI, no token,
and no config — it exits immediately.

This slice replaces that with a faithful **devcontainer** flow: provision the repo's
`devcontainer.json` (or a bundled default) with `devcontainer up`, inject a vendored **toolchain
Feature** via `--additional-features`, then inject the agent's **token** and **config** and launch
the agent inside the now-running container.

The credential-isolation invariant is preserved **and pinned by a test**: the container receives
the agent's API token and config it needs to run, but **git credentials never enter** — the engine
performs all remote git operations.

## 2. Decisions locked (from brainstorming, 2026-06-15)

| # | Area | Decision |
|---|---|---|
| 1 | Run model | **exec-into-idle.** `devcontainer up` provisions + idles the container; the engine runs Install/Setup then launches the agent backgrounded. Precise exit/done tracking stays deferred to next-plan #3. |
| 2 | Feature source | **Vendor locally** under `features/flotilla-toolchain/`, referenced by absolute path in `--additional-features`. GHCR publishing is a later, additive step. |
| 3 | Secret transport | **`docker cp` a `0600` env-file** into the container, sourced by the launch wrapper — guarantees the *detached* agent process sees the token, with values never in argv or `docker inspect`. (`devcontainer --secrets-file` evaluated in Task 1; preferred if it alone suffices.) |
| 4 | Claude auth | **`CLAUDE_CODE_OAUTH_TOKEN`** via `claude setup-token` (the user logs in with OAuth, not an API key). Replaces `ANTHROPIC_API_KEY` in the default claude profile. |
| 5 | Backend seam | **Add `Up` + `ExecDetached` to the `Backend` interface.** The fake records them, so `Spawn` stays unit-testable and the cred-isolation test asserts against `fake.UpOpts`. |

## 3. Provisioning model — exec-into-idle

`Spawn` orchestrates (clone step is existing):

```
1. gitops.Clone(repoURL, dest)                 # engine-side, existing — engine holds git creds
2. preflight: docker + devcontainer present     # else clear, actionable error
3. Backend.Up(UpOpts{WorkspaceFolder: dest, ...})   # devcontainer up + Feature → idle container
4. inject secrets: docker cp 0600 env-file → /run/flotilla/agent.env
5. inject config:  setup handler / config_mounts → docker cp host files → container home
6. Backend.Exec(install)        # profile.Install (sync) — e.g. npm i -g the agent CLI
7. Backend.ExecDetached(launch) # wrapper sources agent.env, then exec <profile.Launch>; backgrounded
8. return Agent{...}            # Spawn returns; agent runs on
```

- The container's main process is the devcontainer's own idle loop (devcontainer up keeps it
  running by default). The agent is **not** PID 1; it is a backgrounded exec. Consequently "is the
  agent done?" cannot be read from container state in this slice — that is next-plan #3 by design.
- **Run user (revised — see §14).** The agent **setup** and **launch** run as the devcontainer's
  non-root `remoteUser` (the default config pins `ubuntu`), because Claude Code refuses
  `--dangerously-skip-permissions` as root. The **install** step stays root (global `npm` needs it).
  The secret env-file and the agent's config home (`~/.claude`/`~/.codex`) therefore live under the
  run user's home (`/home/<user>/…`) and are chowned to the run user after `docker cp`.

## 4. Backend interface changes

Two new methods. The Docker backend shells to the `devcontainer`/`docker` CLIs; the in-memory
`Fake` records calls so `fleet.Spawn` remains testable without Docker.

```go
// Up provisions a devcontainer (build + inject Feature + start, idling) and
// returns the container ID. Replaces Create+Start for the agent path.
Up(ctx context.Context, opts UpOpts) (string, error)

// ExecDetached runs cmd in the container without waiting (the backgrounded launch).
ExecDetached(ctx context.Context, id string, cmd []string) error

type UpOpts struct {
    Name               string            // container/agent name (also a label)
    WorkspaceFolder    string            // engine clone dir → devcontainer --workspace-folder
    ConfigPath         string            // external default devcontainer.json (repos without one); "" = auto-discover
    AdditionalFeatures map[string]any    // e.g. {"/abs/path/features/flotilla-toolchain": {}}
    Labels             map[string]string // flotilla.* labels
}
```

- `Exec(ctx, id, cmd)` (existing) and `ExecDetached` shell `docker exec` / `docker exec -d` **by
  container id** — no workspace-folder threading, simple and robust. Fidelity loss (devcontainer
  `remoteUser`/`remoteEnv` not auto-applied to these execs) is acceptable: the agent runs as root
  and reads its token from the injected env-file, so neither is needed.
- `Create`/`Start`/`Stop`/`Remove`/`List`/`AttachInfo` are unchanged. `Create`/`Start` remain as
  raw lower-level primitives (still used by the docker integration test and by unit tests that seed
  the fake); the agent path now uses `Up`.
- **Docker impl of `Up`:** `devcontainer up --workspace-folder <dest> [--config <ConfigPath>]
  --additional-features '<json>' --id-label flotilla.agent=<name> ...`, then resolve and return the
  container id (e.g. `devcontainer up` JSON output / `docker ps` by label). Labels are applied so
  `List` continues to derive the fleet from Docker.
- **Fake impl of `Up`:** stores the `UpOpts` (exposed for assertions, like the existing
  `ExecCalls`) and creates an in-memory `running` container, mirroring `Create`+`Start`.

## 5. devcontainer.json handling

1. **Repo has `.devcontainer/`** → `devcontainer up --workspace-folder <clone>` auto-discovers it;
   the engine adds only `--additional-features`. `ConfigPath` is empty.
2. **Repo has none** → the engine supplies a bundled minimal **default devcontainer.json** via
   `--config <path>` (written to an engine-side temp/known location, not into the repo). Base image
   = `Fleet.BaseImage`; relies on devcontainer's default idle behavior. No edits to the repo's
   files either way.
3. The toolchain Feature is injected non-invasively through `--additional-features` in both cases.

## 6. Vendored toolchain Feature

A standard Dev Container Feature checked into the repo:

```
features/flotilla-toolchain/
  devcontainer-feature.json   # id "flotilla-toolchain", name, version
  install.sh                  # installs common tooling only
```

`install.sh` installs **only** common tooling needed to install and run a CLI agent:
**node** (for `npm i -g` agent installs), **git**, **gh**, **mise**. It installs **no agent CLI**
(that is the profile's `Install`) and **no credential of any kind**. Keeping the Feature cred-free
keeps the built image cacheable and safe to share.

Referenced by absolute path so no registry is required:
`--additional-features '{"<repo>/features/flotilla-toolchain": {}}'`. Publishing to
`ghcr.io/<owner>/features/flotilla-toolchain` is deferred and purely additive (the design spec's
§4.3 target).

## 7. Credential & config injection — one mechanism (`docker cp`)

After `Up`, everything that enters the container is copied host→container with `docker cp`. `docker
cp` never places contents in argv and the values never appear in `docker inspect` container env, so
this is the single, auditable injection point.

### 7.1 Secrets (the agent token)

- The **only** secret is the agent's token. The profile's `Env` is an **allowlist** of key names.
- The engine resolves each allowlisted key host-side from the process environment (where
  `fnox activate` / `fnox exec` already placed it). Nothing leaks unless named in the allowlist.
- Resolved `KEY=VALUE` pairs are written to a host temp file (`0600`), `docker cp`'d to
  `/run/flotilla/agent.env` (`0600`, root-owned) inside the container, and the host temp file is
  deleted (always, including on error paths).
- The **launch wrapper** sources it: `sh -c 'set -a; . /run/flotilla/agent.env; set +a; exec <launch>'`.
- Default profiles: `claude` → `env = ["CLAUDE_CODE_OAUTH_TOKEN"]`; `codex` → `env = ["OPENAI_API_KEY"]`.

### 7.2 Config (non-secret)

- Setup handlers and declarative `config_mounts` `docker cp` config **files** (not bind mounts) into
  the container home. Copy semantics keep the container isolated — edits inside never leak back to
  the host config.

### 7.3 Profile changes

- `claude.toml`: `env = ["CLAUDE_CODE_OAUTH_TOKEN"]`, `install = "npm i -g @anthropic-ai/claude-code"`,
  `setup = "builtin:claude"`.
- `codex.toml`: `env = ["OPENAI_API_KEY"]`, `install = "npm i -g @openai/codex"` (unchanged),
  `setup = "builtin:codex"`; `config_mounts` copy `~/.codex` if present.

## 8. Setup handlers

A registry `map[string]Handler` keyed by `"builtin:<name>"`; `setup = "declarative"` (the default
for drop-in agents) runs only `config_mounts`. A `Handler` receives the agent name/container id, an
exec/copy capability, and the resolved profile, and assembles the agent's home.

- **`builtin:claude`** — ensure `~/.claude/`, write a minimal `settings.json` (enough to skip the
  onboarding/first-run prompts in headless mode), optionally copy the host's global `~/.claude/CLAUDE.md`.
  Auth is the env token (§7.1), so the handler does **no** secret work. Deliberately does **not**
  copy `~/.claude/.claude.json` (large; holds global state + per-project history) — matches the
  design spec's §4.4 curation rule. A minimal, container-safe `settings.json` is written rather than
  copying the host's full `settings.json` (which may reference host-only hooks/plugins).
- **`builtin:codex`** — assemble `~/.codex/config.toml` + `AGENTS.md`; auth via `OPENAI_API_KEY`
  (§7.1) or copy `~/.codex/auth.json` if it exists. Kept intentionally minimal (Codex is not the
  primary agent).

## 9. Credential-isolation invariant + the test

**Invariant:** the container receives the *agent's* token and config; **git credentials never
enter**. The engine is the sole holder of git creds and performs all remote git ops.

A new `fleet` unit test (against the `Fake` backend, no Docker) drives `Spawn` and asserts that
**none** of the following appear in `fake.UpOpts`, the recorded `Exec`/`ExecDetached` commands, or
the injected env/config:

- No git/GitHub env keys: `GITHUB_TOKEN`, `GH_TOKEN`, `GH_*`, `GIT_*`, no `git` credential helper.
- No mount/copy of a git credential source: `~/.git-credentials`, `~/.config/gh`, `~/.ssh`, the host
  `~/.gitconfig`.
- The only host path that enters is the **engine clone** (`WorkspaceFolder`); host `$HOME` is never
  mounted or copied wholesale.

This pins the invariant so a future change that tried to inject a git credential into the container
would fail the test.

## 10. Preflight

The `devcontainer` CLI is **not guaranteed present** (it is absent on the current dev machine).

- `Spawn` runs a preflight that checks for `docker` and `devcontainer` on PATH and returns a clear,
  actionable error when missing (`devcontainer CLI not found — install with: npm i -g @devcontainers/cli`).
- A small `flotilla doctor` command surfaces the same checks (docker daemon reachable, devcontainer
  CLI present, agent token resolvable for the default profile).
- Integration tests that need Docker/devcontainer **skip** when either is absent, mirroring the
  existing `dockerAvailable()` pattern in `internal/backend/docker_test.go`.

## 11. Out of scope (later plans, unchanged)

- **Egress firewall** (next-plan #2) — default-deny + per-profile allowlist. The toolchain Feature
  ships the firewall *later*; this slice installs tooling only.
- **Done-signal / submission** (next-plan #3) — exit tracking + engine push/PR. This slice starts
  the agent; it does not detect completion or push work.
- **Logs / transcript mounts** (next-plan #4) — the launch wrapper's output goes to a simple
  in-container log file for now; host transcript mounts come later.
- **On-demand fetch/pull** (next-plan #5).

## 12. Testing strategy

- **Unit (no Docker), against `Fake`:** `Spawn` orchestration order (Up → cp secret → cp config →
  Exec install → ExecDetached launch); profile `Env` allowlist resolution from host env; setup-handler
  registry dispatch (`builtin:claude`, `builtin:codex`, `declarative`); **the credential-isolation
  test (§9)**; preflight error when a prerequisite is reported missing (injected check).
- **Unit, pure:** launch-wrapper string assembly; default-devcontainer.json generation;
  Feature-path / `--additional-features` JSON assembly.
- **Integration (skip without docker + devcontainer):** a real `devcontainer up` on a sample repo
  with `--additional-features` injects the Feature cleanly (the §7 / design-spec §7 hands-on check);
  `docker cp` of the env-file lands `0600`; a trivial exec inside the container sees the injected env.
- Run Go via `mise exec -- go ...` (Go is mise-pinned; plain `go` is not on PATH).

## 13. Open items folded into Task 1 (the spike)

- Confirm `devcontainer up --additional-features` injects the vendored Feature cleanly on a sample
  repo (design spec §7's outstanding hands-on check).
- Determine whether `devcontainer --secrets-file` alone reaches the *detached* launch process; if
  so, prefer it over the `docker cp` env-file for the secret (decision #3 fallback).
- Confirm the container-id resolution from `devcontainer up` output (JSON vs `docker ps` by label).

## 14. Corrections from live verification (2026-06-15)

Resolved against the real `devcontainer` CLI (v0.87.0); the implementation follows these, which
refine §5–§6:

- **Local Feature reference.** `--additional-features` does **not** accept an absolute (or
  arbitrary) local path — it treats the key as an OCI ref and fails ("may not be logged in"). A
  *local* Feature is only resolved when it lives in a sub-folder of the workspace's `.devcontainer/`
  and is referenced by a path **relative to that folder**. So the engine extracts the vendored
  Feature to `<clone>/.devcontainer/flotilla-toolchain/` and passes
  `--additional-features '{"./flotilla-toolchain": {}}'`. This cleanly overlays on top of the repo's
  **own** devcontainer when present, and on a bundled default (written to
  `<clone>/.devcontainer/devcontainer.json`) when not — both via plain auto-discovery, so the
  external `--config` path and the `UpOpts.ConfigPath` field were dropped.
- **Container idles + workspace.** `devcontainer up` keeps the container alive (default
  `overrideCommand`), and mounts the workspace under `/workspaces/<name>`; the launch wrapper `cd`s
  into it (resolved via a `/workspaces/*` glob) so the agent operates on the repo.
- **Secret transport.** The `docker cp` `0600` env-file (decision #3 primary) works as designed and
  reaches the detached launch; `--secrets-file` was not needed.
- **Run as non-root.** Claude Code refuses `--dangerously-skip-permissions` as root (the engine's
  first cut ran everything as root). Fix: the default devcontainer pins `remoteUser: "ubuntu"`,
  `Backend.Up` returns the `remoteUser`, and the engine runs **setup** + **launch** as that user
  (via `su <user> -c`) while keeping **install** as root (global `npm`). The secret env-file
  (`<home>/.flotilla/agent.env`) and `~/.claude` live under the run user's home and are chowned to
  the run user after `docker cp` (so ownership is correct regardless of the host source uid). This
  matches Anthropic's own recommendation (run Claude Code as a non-root user in a dev container);
  the `IS_SANDBOX=1` root-bypass was rejected as undocumented, Claude-specific, and flaky across
  versions.
- **End-to-end verified** on `octocat/Hello-World` (no repo devcontainer), via a real
  `fnox exec -- flotilla spawn … --agent claude`: the toolchain Feature installs node/git, the agent
  CLI installs, `~/.claude` is assembled, the `CLAUDE_CODE_OAUTH_TOKEN` env-file lands `0600`, the
  workspace is mounted, the agent runs **as `ubuntu`** and autonomously **created the requested
  file** (owned `ubuntu:ubuntu`), and **no git credentials are present in the container**.
  `CLAUDE_CODE_OAUTH_TOKEN` is stored globally in fnox (`bws-general` provider) so `flotilla` picks
  it up from the host env.
- **New robustness items** (stale work-dir orphan collision; root `.devcontainer.json` form;
  `/workspaces/*` glob; no Feature caching) are captured in [the backlog](../backlog.md).
