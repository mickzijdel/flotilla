# Flotilla — Design Spec

**Date:** 2026-06-14
**Status:** Draft for review
**Working name:** Flotilla (a fleet of small craft; complements *Prow*). Trivially renamed.

## 1. Summary

Flotilla manages a fleet of **autonomous Claude Code agents**, each running in its own
**isolated local Docker container**, working on real repositories in parallel. The
container is the blast radius, so agents run with `--dangerously-skip-permissions` for
long unattended stretches.

The core is a **CLI engine** (the control plane). Everything else — a Claude **skill** that
lets an orchestrating agent drive the CLI, and a **thin VS Code extension** — is a client on
top. The engine is built to abstract its compute target behind a **backend interface** so a
remote Docker host (and, later, multiple machines with session transfer) drops in without a
rewrite.

The design deliberately keeps **all remote-write credentials out of the sandbox**: the engine
clones the repo, the agent only commits locally, and the engine performs the push + PR. "Agents
can only PR to remote" is therefore enforced *by construction*, not by policy.

## 2. Goals / Non-goals

### Goals
- Run many autonomous Claude agents across many repos at once, locally, safely.
- One repo can host multiple competing agents (parallel attempts) with zero interference.
- Reuse each repo's real dev environment via its `devcontainer.json`, with the agent toolchain
  layered on non-invasively.
- CLI-first and fully scriptable; an agent can orchestrate the fleet via a documented skill.
- Explore any agent's live state in VS Code on demand.
- Strong default security posture: no write creds in the box, default-deny egress, resource caps.

### Non-goals (v1)
- Web dashboard (YAGNI; CLI + VS Code cover the need).
- Cloud VM provisioning (Prow already does that; Flotilla is local Docker, remote-Docker-capable).
- A background daemon (state is derived from Docker; revisit only if push-notifications demand it).
- Bandwidth/traffic shaping (the egress *allowlist* is the security primitive, not byte caps).
- Bypassing interactive-auth MCP servers in-container (we proxy to a host instance instead).

## 3. Decisions locked (from brainstorming)

| Area | Decision |
|---|---|
| Substrate | Local Docker now; **compute-backend seam** for remote Docker / multi-machine later |
| Environment | Build on the official `devcontainer` CLI from the repo's `devcontainer.json`, **plus** a layered agent-toolchain **Dev Container Feature** (Claude, mise, gh, …); default config when the repo has none |
| Surfaces | **CLI engine first** (JSON output) → **thin VS Code extension** (sidebar list/start/stop + native "Attach to Running Container"); also an **agent-drivable skill** modeled on `playwright-cli` |
| Autonomy | `--dangerously-skip-permissions` inside the sandbox |
| Credentials | **Personal Claude creds** injected; **scoped/none for GitHub** — engine-mediated push/PR means the container holds no remote write cred |
| Isolation | **Engine clones fresh per agent** into the agent's volume, mounts it; agent commits locally |
| Submission | **Engine** pushes the branch and opens/updates the PR on a done-signal → "PR-only" by construction |
| State | **Stateless** — derive from Docker labels + logs + a per-agent status file |
| Egress | **Default-deny allowlist firewall** (lift Anthropic's devcontainer `init-firewall` pattern) |
| MCP interactive auth | **Host-run the MCP server, containers connect over the network**; token-reuse where a server supports it |
| Resource limits | **Global config defaults** (`~/.claude-agent/config.toml`) + per-project override |
| Logs | Dedicated `~/.claude-agent/` home; per-session dir named by repo+date; **piggyback Claude's own transcript storage** via a mounted volume |
| Home for the code | **New standalone repo** (this one) |
| Build vs adopt | **Build the thin engine**, standing on existing primitives, stealing the firewall |
| Substrate choice | **Parallel spike** `devcontainer` CLI + raw Docker **vs** Docker Sandboxes, then pick |

## 4. Architecture

### 4.1 Planes

- **Control plane — the engine (`flotilla` CLI).** Owns all orchestration: clone, build, run,
  inject, list, attach, submit, stop. Holds the only GitHub write credential. Emits JSON.
- **View plane — thin clients.** The VS Code extension and the agent-facing skill are *both* just
  callers of the CLI. No business logic lives in a client.

```
                 ┌─────────────────────────────────────────────┐
   you / agent → │  flotilla CLI (engine, control plane)        │
   VS Code ext → │  spawn · list · attach · submit · stop       │
                 │     │                                        │
                 │     ▼  compute-backend interface             │
                 │  ┌───────────────┐   ┌────────────────────┐  │
                 │  │ local Docker  │   │ remote Docker host │…│  │  (future: host registry)
                 │  └───────────────┘   └────────────────────┘  │
                 └─────────────────────────────────────────────┘
                        │ per agent
                        ▼
        ┌───────────────────────────────────────────┐
        │ container: devcontainer(repo) + Feature    │
        │  - fresh clone (mounted from host volume)  │
        │  - Claude creds + curated ~/.claude        │
        │  - allowlisted env (.env / fnox)           │
        │  - default-deny egress firewall            │
        │  - claude --dangerously-skip-permissions   │
        │  - transcript volume → ~/.claude-agent/logs│
        └───────────────────────────────────────────┘
```

### 4.2 Compute-backend interface

A small interface (`create`, `start`, `stop`, `exec`, `attach-info`, `remove`, `list`) with a
**local Docker** implementation for v1. A **remote Docker host** implementation (Docker API over
TLS/SSH) is the second target. A **host registry** in config (`local`, `vps-a`, …) lets the user
pick a host per agent; **session transfer** = re-clone the branch + replay the agent's
config/state onto the target host and resume (the branch is the portable state; logs persist to a
known volume). Multi-machine/transfer is explicitly future, but the seam is v1.

### 4.3 Container build — devcontainer + Feature overlay

1. If the repo has `.devcontainer/`, run `devcontainer up` against it; otherwise use a bundled
   **default `devcontainer.json`** (base image + the same Feature).
2. Inject the **agent-toolchain Dev Container Feature** (`ghcr.io/<you>/features/claude-agent`)
   via `devcontainer up --additional-features` — non-invasive, no edits to the repo's files. The
   Feature installs *only* tooling (mise, Claude Code, gh, the egress-firewall init); credentials
   and config are injected at runtime (§4.4), keeping the image cred-free and cacheable.
3. Cache built images keyed by repo + devcontainer hash to amortize first-build cost.

### 4.4 Credential & config injection — the single control point

A per-repo `.claude-agent.toml` (with a global default in `~/.claude-agent/config.toml`) is the
**only** place that decides what enters a sandbox:

- **Env:** an explicit **allowlist** of keys, resolved *on the host* from `.env` or
  `fnox export`, injected as container env. The allowlist *is* the boundary — nothing leaks unless
  named.
- **Claude config:** the container's `~/.claude` is **assembled from curated pieces** — (a) auth
  credentials, (b) a chosen `settings.json`, (c) plugins, (d) global `CLAUDE.md`. We do **not**
  copy `.claude.json` wholesale (huge, holds global oauth + per-project history). MCP servers known
  to need interactive auth are stripped or repointed at a host instance (§4.7).
- **GitHub:** **no write credential** in the container. The engine clones (optionally with a
  read-only token for private repos) and is the only holder of the write token.

### 4.5 Isolation & submission — "PR-only" by construction

- The **engine** does a fresh `git clone` per agent into that agent's workspace volume, then mounts
  it into the container. The agent commits **locally only**.
- On a **done-signal** (Claude Stop hook / sentinel file), the engine pushes the agent's branch and
  opens/updates a PR via `gh pr create`. Re-runnable to update the PR as the agent iterates.
- Because the container never holds a remote write cred, the worst case is "an unreviewed PR
  appears." Pair with branch protection on `main` (require PR, no direct push, no force-push) for
  defense in depth.

### 4.6 Egress — default-deny allowlist firewall

Each container runs with `NET_ADMIN` and an init firewall (lifted from Anthropic's
`claude-code-devcontainer` / ClaudeBox `init-firewall.sh`): **default-deny egress**, allow a
curated set — GitHub, package registries (npm/pypi/ghcr/mise), `api.anthropic.com`, the MCP host —
plus per-project `egress_allow = [...]` additions. Shipped as part of the agent Feature.

### 4.7 Interactive-auth MCP servers

- **Primary:** host-run the MCP server (authenticated once via normal browser flow); every
  container points its config at `http://host:port` (HTTP/SSE transport). Shared auth, single login.
- **Secondary:** transplant an already-obtained refresh token into the container for servers that
  persist one, or use device-code flow where supported.
- Per-server opt-in/out in config; default to stripping servers known to require interactive auth.

### 4.8 State — stateless

Containers carry labels (`flotilla.repo`, `flotilla.agent`, `flotilla.created`, `flotilla.host`).
The CLI derives the fleet from `docker ps` + `docker logs` + a per-agent **status file**
(written by an in-container notification/Stop hook: `running` / `blocked` / `done`). The VS Code
extension polls `flotilla list --json`. No daemon.

### 4.9 Logs & home folder

```
~/.claude-agent/
  config.toml                         # global defaults
  state/                              # optional derived caches
  logs/<repo>/<YYYY-MM-DD-HHMM>-<agent>/
    transcript/   ← mounted as the container's ~/.claude project/transcript dir (live)
    container.log ← teed container stdout/stderr
    status        ← running|blocked|done
```

The transcript dir is **mounted into the container** as Claude's own projects/transcript area, so
Claude's session transcript lands on the host live (no copying) and is openable in VS Code while
the agent runs.

### 4.10 Resource limits

`config.toml` global defaults: `cpus`, `memory`, `auto_stop`, `max_concurrent` (the runaway-
orchestrator guardrail). `.claude-agent.toml` overrides per repo. Applied as Docker
`--cpus`/`--memory` and an idle/auto-stop timer (mirrors Prow's `auto_stop`).

## 5. CLI surface (control plane)

All commands support `--json`. Indicative set:

```
flotilla spawn <repo> [--task <file>|--prompt <str>] [--host local] [--machine ...]
flotilla list [--json]                 # the fleet + status
flotilla logs <agent> [-f]
flotilla attach <agent>                # prints VS Code attach target / docker exec info
flotilla submit <agent>                # engine push + open/update PR (also auto on done-signal)
flotilla stop <agent> | --all
flotilla rm <agent> | --all
flotilla doctor                        # preflight: docker, devcontainer CLI, gh, creds
```

### 5.1 Agent-drivable skill

A `claude-agent` skill modeled on `playwright-cli`'s `SKILL.md`: documents the subcommands and
their JSON contracts so an **orchestrating** agent (on the host, where creds + Docker live) can
spawn workers, poll status, and collect PRs. Worker agents *inside* sandboxes do not orchestrate
and hold no creds. `max_concurrent` + resource caps bound a runaway orchestrator.

## 6. VS Code extension (view plane)

A sidebar TreeView listing agents across repos (status, repo, age) that shells out to
`flotilla list --json`; start/stop/submit buttons call the CLI; "Attach" triggers the **built-in
Dev Containers "Attach to Running Container"** so VS Code itself provides editor/terminal/file
exploration. A few hundred lines; no webview, no bundled UI.

## 7. Substrate spike (pre-implementation)

Per the parallel-prototype rule, build two minimal spikes that each: clone a repo, build via the
backend, run `claude` once, and surface logs/attach. Compare:

- **A — `devcontainer` CLI + raw Docker SDK:** max control, matches the design as specced.
- **B — Docker Sandboxes (official) as the backend:** less plumbing; inherits clone-mode + safety.

**Criteria:** devcontainer.json fidelity, Feature/firewall injection, attach ergonomics, image
caching/start time, multi-agent-per-repo, remote-host path, maintenance surface. Recommend one,
confirm, then build out behind the compute-backend interface.

## 8. Prior art (why build)

The 2026 field is crowded but nothing matches the full combination. Closest:
- **Sculptor** (Imbue) — closest whole-vision (local Docker, devcontainer, "Pairing Mode" ≈
  VS Code on demand) but **closed-source, GUI-first, no CLI/skill, no PR-only, no egress firewall,
  no multi-machine**.
- **Container Use** (Dagger) — closest engine (OSS CLI, per-agent containers + branches, attach)
  but **MCP/agent-driven not host-orchestrated, Dagger-based, no security layer**.
- **Docker Sandboxes** (official) — candidate **substrate** (spike B), not a competitor.
- **Conductor / Claude Squad / Crystal / Vibe Kanban / Uzi** — worktree-based, weaker isolation.
- **ClaudeBox / claude-code-devcontainer / codex-lockbox** — single-box Docker + **allowlist
  firewall**; the **reference to steal the firewall from**.
- **E2B / Daytona / Modal / Coder / Fly** — candidate **remote backends** later.

The differentiators Flotilla owns — agent-drivable CLI skill, PR-only-by-construction (zero creds
in box), devcontainer.json + Feature overlay, multi-machine/session-transfer — are exactly what
none of these provide.

## 9. Security model (summary)

- Container isolates the **filesystem**, not your **identity** — so the design removes identity from
  the box: no GitHub write cred, default-deny egress, curated config.
- Residual trust: personal **Claude** creds run as you (accepted — it's your usage); a read-only
  GitHub token may enter the box for private clones (prefer engine-side clone to avoid even that).
- Guardrails: branch protection on `main`, `max_concurrent`, per-container cpu/mem caps, auto-stop.

## 10. Open questions

- Exact done-signal mechanism (Stop hook script vs sentinel file vs both) — settle in the plan.
- Whether private-repo clone uses a read-only in-box token or engine-side clone only.
- Feature distribution: publish to GHCR vs vendor locally for the spike.
- Final name (Flotilla is a working name).
