# Flotilla — Design Spec

**Date:** 2026-06-14
**Status:** Draft for review
**Working name:** Flotilla (a fleet of small craft; complements *Prow*). Trivially renamed.

## 1. Summary

Flotilla manages a fleet of **autonomous coding agents** — Claude Code by default, but
**agent-agnostic**: Codex, Gemini CLI, opencode, or any CLI agent works via a drop-in **agent
profile** (§4.11), no code changes. Each agent runs in its own **isolated local Docker container**,
working on real repositories in parallel. The container is the blast radius, so agents run in their
fully-autonomous mode (e.g. Claude's `--dangerously-skip-permissions`) for long unattended stretches.

The core is a **CLI engine** (the control plane). Everything else — an agent-facing **skill** that
lets an orchestrating agent drive the CLI, and a **thin VS Code extension** — is a client on
top. The engine is built to abstract its compute target behind a **backend interface** so a
remote Docker host (and, later, multiple machines with session transfer) drops in without a
rewrite.

The design deliberately keeps **all remote-write credentials out of the sandbox**: the engine
clones the repo, the agent only commits locally, and the engine performs the push + PR. "Agents
can only PR to remote" is therefore enforced *by construction*, not by policy.

## 2. Goals / Non-goals

### Goals
- Run many autonomous coding agents (Claude Code, Codex, Gemini CLI, opencode, …) across many
  repos at once, locally, safely.
- **Agent-agnostic:** new agents are drop-in via a declarative **profile** (§4.11) — install +
  launch command + config to inject + done-signal — with no code changes. First-class agents
  (**Claude and Codex** in v1) additionally get a hardcoded **setup handler** for smart config
  assembly.
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
| Agents | **Agent-agnostic**: Claude Code is the default profile; Codex / Gemini CLI / opencode / any CLI agent drop in via a **profile** file (§4.11). First-class agents also get a hardcoded **setup handler** |
| Environment | Build on the official `devcontainer` CLI from the repo's `devcontainer.json`, **plus** a layered **toolchain Feature** (mise, gh, git, firewall); the agent CLI itself installs per the active profile; default config when the repo has none |
| Surfaces | **CLI engine first** (JSON output) → **thin VS Code extension** (sidebar list/start/stop + native "Attach to Running Container"); also an **agent-drivable skill** modeled on `playwright-cli` |
| Engine language | **Go** (single static binary; shells out to `docker`, `devcontainer`, `gh`, `git`) |
| Autonomy | Run the agent in its fully-autonomous mode (Claude `--dangerously-skip-permissions`; Codex `--full-auto`; etc. — from the profile) inside the sandbox |
| Credentials | Agent creds injected **per the profile** (Claude oauth/`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …); **no GitHub cred** in the box — engine-mediated git means the container holds no remote cred |
| Isolation | **Engine clones fresh per agent** into the agent's volume, mounts it; agent commits locally |
| Submission | **Engine** pushes the branch and opens/updates the PR on a done-signal → "PR-only" by construction |
| State | **Stateless** — derive from Docker labels + logs + a per-agent status file |
| Egress | **Default-deny allowlist firewall** (lift Anthropic's devcontainer `init-firewall` pattern) |
| MCP interactive auth | **Host-run the MCP server, containers connect over the network**; token-reuse where a server supports it |
| Resource limits | **Global config defaults** (`~/.flotilla/config.toml`) + per-project override |
| Logs | Dedicated `~/.flotilla/` home; per-session dir named by repo+date; **piggyback the agent's own transcript storage** (path from the profile) via a mounted volume |
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
        │  - agent creds + config (per profile)      │
        │  - allowlisted env (.env / fnox)           │
        │  - default-deny egress firewall            │
        │  - <agent launch cmd> (autonomous mode)    │
        │  - transcript volume → ~/.flotilla/logs    │
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
2. Inject the **Flotilla toolchain Dev Container Feature** (`ghcr.io/<you>/features/flotilla-toolchain`)
   via `devcontainer up --additional-features` — non-invasive, no edits to the repo's files. The
   Feature installs *only* common tooling (mise, gh, git, the egress-firewall init); **the agent
   CLI itself is installed per the active agent profile** (§4.11). Credentials and config are
   injected at runtime (§4.4), keeping the image cred-free and cacheable.
3. Cache built images keyed by repo + devcontainer hash to amortize first-build cost.

### 4.4 Credential & config injection — the single control point

A per-repo `.flotilla.toml` (with a global default in `~/.flotilla/config.toml`) plus the active
**agent profile** (§4.11) together decide what enters a sandbox:

- **Env:** an explicit **allowlist** of keys, resolved *on the host* from `.env` or
  `fnox export`, injected as container env. The allowlist *is* the boundary — nothing leaks unless
  named.
- **Agent config (hybrid):** two modes, set by the profile.
  - *Declarative (default, drop-in)* — the profile's `config_mounts` + `env` copy/mount named
    host files into the agent's home. Zero code; how a brand-new agent works.
  - *Built-in setup handler (first-class agents)* — a hardcoded per-agent routine for smart
    assembly. v1 ships handlers for **Claude and Codex**. **Claude's handler** builds `~/.claude`
    from curated pieces — auth credentials, a chosen `settings.json`, plugins, global `CLAUDE.md` —
    and deliberately does **not** copy `.claude.json` wholesale (huge; holds global oauth +
    per-project history). **Codex's handler** assembles `~/.codex/` (`config.toml` + auth /
    `OPENAI_API_KEY`) and `AGENTS.md`. Other agents (`~/.gemini`, `~/.config/opencode`, …) start
    declarative and get a handler when we choose to support them deeply.
  - Either way, MCP/tool servers known to need interactive auth are stripped or repointed at a host
    instance (§4.7).
- **GitHub:** **no credential of any kind** in the container — not even read-only. The engine is
  the sole holder of all GitHub creds and performs **every** remote git op (clone, fetch, pull,
  push, PR) on the agent's behalf (§4.5).

### 4.5 Isolation & submission — "PR-only" by construction

- The **engine** does a fresh `git clone` per agent into that agent's workspace volume, then mounts
  it into the container. The agent commits **locally only**. **Engine-side clone only** — no GitHub
  cred enters the box even for private repos.
- **On-demand fetch/pull:** the agent can request a fresh fetch/pull mid-session through the control
  channel (the agent signals; the **engine** performs the fetch into the mounted volume with its
  own creds). The agent never gains remote access — it only *asks*. (Request mechanism — sentinel
  file vs in-container `flotilla` shim that signals the host — settled in the plan.)
- On a **done-signal** — by default the **agent process exiting** (the launched command returns),
  which is agent-agnostic; optionally a richer hook where the agent supports one (e.g. Claude's
  Stop hook) — the engine pushes the agent's branch and opens/updates a PR via `gh pr create`.
  Re-runnable to update the PR as the agent iterates.
- Because the container never holds any remote git cred, the worst case is "an unreviewed PR
  appears." Pair with branch protection on `main` (require PR, no direct push, no force-push) for
  defense in depth.

### 4.6 Egress — default-deny allowlist firewall

> **Superseded (2026-06-15):** the in-container iptables approach below was replaced, during the
> egress design, by an **out-of-container proxy model** (agent on a Docker `--internal` network;
> egress only via a per-agent tinyproxy sidecar; no `NET_ADMIN`). See
> [the egress firewall spec](2026-06-15-flotilla-egress-firewall-design.md). The allowlist sources
> and default-deny intent below still hold; the enforcement mechanism changed.

Each container runs with `NET_ADMIN` and an init firewall (lifted from Anthropic's
`claude-code-devcontainer` / ClaudeBox `init-firewall.sh`): **default-deny egress**, allow a
curated set — GitHub, package registries (npm/pypi/ghcr/mise), **the active agent's API endpoint
(from its profile, e.g. `api.anthropic.com`, `api.openai.com`, `generativelanguage.googleapis.com`)**,
the MCP host — plus per-project `egress_allow = [...]` additions. Shipped as part of the toolchain
Feature.

### 4.7 Interactive-auth MCP servers

- **Primary:** host-run the MCP server (authenticated once via normal browser flow); every
  container points its config at `http://host:port` (HTTP/SSE transport). Shared auth, single login.
- **Secondary:** transplant an already-obtained refresh token into the container for servers that
  persist one, or use device-code flow where supported.
- Per-server opt-in/out in config; default to stripping servers known to require interactive auth.

### 4.8 State — stateless

Containers carry labels (`flotilla.repo`, `flotilla.agent`, `flotilla.created`, `flotilla.host`).
The CLI derives the fleet from `docker ps` + `docker logs` + a per-agent **status** (default:
derived from the agent process — alive = `running`, exited = `done`; optionally `blocked` via an
agent hook where supported, written to a status file). The VS Code extension polls
`flotilla list --json`. No daemon.

### 4.9 Logs & home folder

```
~/.flotilla/
  config.toml                         # global defaults
  agents/<name>.toml                  # drop-in agent profiles (built-ins + user-added)
  state/                              # optional derived caches
  logs/<repo>/<YYYY-MM-DD-HHMM>-<agent>/
    transcript/   ← mounted as the agent's transcript dir (path from profile; live)
    container.log ← teed container stdout/stderr
    status        ← running|blocked|done
```

The transcript dir is **mounted into the container** as the agent's own transcript area (path
declared by the profile, §4.11), so the session transcript lands on the host live (no copying) and
is openable in VS Code while the agent runs. `container.log` is the universal fallback for agents
that don't expose a transcript path.

### 4.10 Resource limits

`config.toml` global defaults: `cpus`, `memory`, `auto_stop`, `max_concurrent` (the runaway-
orchestrator guardrail). `.flotilla.toml` overrides per repo. Applied as Docker
`--cpus`/`--memory` and an idle/auto-stop timer (mirrors Prow's `auto_stop`).

### 4.11 Agent profiles — the agnosticism model

An **agent profile** is a declarative TOML manifest describing everything that varies between
agents. The engine knows nothing agent-specific; it just reads a profile. Built-in profiles ship
for **Claude Code (default) and Codex** (both first-class, with setup handlers), plus declarative
starters for **Gemini CLI and opencode**. Users **drop a new profile into
`~/.flotilla/agents/<name>.toml`** (or a repo-local one) to add any CLI agent — no code changes.

A profile declares:

```toml
name          = "codex"                          # identifier
install       = "npm i -g @openai/codex"         # or a Feature ref; optional if base-image-baked
launch        = 'codex exec --dangerously-bypass-approvals-and-sandbox "{prompt}"'
                                                 # autonomous-run template ({prompt}/{task_file})
setup         = "builtin:codex"                  # or "declarative" (default)
config_mounts = ["~/.codex:/home/agent/.codex"]  # declarative copy/mount of host config
env           = ["OPENAI_API_KEY"]               # keys to inject for this agent
transcript_path = "~/.codex/sessions"            # for the live transcript mount (optional)
egress_allow  = ["api.openai.com"]               # agent API endpoint(s) for the firewall
done_signal   = "process-exit"                   # default; or a hook descriptor where supported
```

For reference, the Claude default profile's `launch` is
`claude --dangerously-skip-permissions -p "{prompt}"`, `setup = "builtin:claude"`,
`env = ["ANTHROPIC_API_KEY"]`, `egress_allow = ["api.anthropic.com"]`.

`setup = "builtin:<name>"` invokes the hardcoded setup handler (§4.4) for smart config assembly;
`setup = "declarative"` uses only `config_mounts` + `env`. Anything expressible as "install a CLI +
run a command + inject this config + know when it finished" is drop-in. The named agents
(Claude/Codex/Gemini/opencode) are all CLIs that fit this shape; GUI- or daemon-based agents are
out of scope.

## 5. CLI surface (control plane)

All commands support `--json`. Indicative set:

```
flotilla spawn <repo> [--agent claude|codex|...] [--task <file>|--prompt <str>] [--host local] [--machine ...]
flotilla agents                        # list available agent profiles (built-in + drop-in)
flotilla list [--json]                 # the fleet + status
flotilla logs <agent> [-f]
flotilla attach <agent>                # prints VS Code attach target / docker exec info
flotilla submit <agent>                # engine push + open/update PR (also auto on done-signal)
flotilla stop <agent> | --all
flotilla rm <agent> | --all
flotilla doctor                        # preflight: docker, devcontainer CLI, gh, creds
```

### 5.1 Agent-drivable skill

A `flotilla` skill modeled on `playwright-cli`'s `SKILL.md`: documents the subcommands and
their JSON contracts so an **orchestrating** agent (on the host, where creds + Docker live) can
spawn workers, poll status, and collect PRs. Worker agents *inside* sandboxes do not orchestrate
and hold no creds. `max_concurrent` + resource caps bound a runaway orchestrator.

## 6. VS Code extension (view plane)

A sidebar TreeView listing agents across repos (status, repo, age) that shells out to
`flotilla list --json`; start/stop/submit buttons call the CLI; "Attach" triggers the **built-in
Dev Containers "Attach to Running Container"** so VS Code itself provides editor/terminal/file
exploration. A few hundred lines; no webview, no bundled UI.

## 7. Substrate decision — RESOLVED: devcontainer CLI + raw Docker

**Decision (2026-06-14): Substrate A — the `devcontainer` CLI + raw Docker — is the v1 backend.**
Resolved on capability/availability grounds, so the parallel build-spike was skipped.

**Why A:**
- **Availability:** Docker Sandboxes (B) requires **Docker Desktop and supports only macOS/Windows**
  as of Mar 2026 (Linux on the roadmap; the standalone `sbx` CLI is nascent and the Desktop-
  integrated `docker sandbox` is already deprecated). The target machine is Linux without Desktop —
  B is a non-starter.
- **Fit:** A natively matches three design pillars — the **devcontainer.json + Feature overlay**
  (`devcontainer up --additional-features`), the **engine-clone-mount-no-creds** model (plain
  `docker run -v` + host-side git), and **remote-host/multi-machine** (`DOCKER_HOST` over TLS/SSH).
  B fights all three.
- **Trade-off accepted:** B's microVM gives a stronger boundary than A's container. The security
  layer (zero creds in box, default-deny egress, resource caps, branch protection) closes most of
  that gap for a local single-user fleet.

**Future:** because the **compute-backend interface** already abstracts this, Docker Sandboxes /
`sbx` can be added as an *additional* backend later (when Linux lands), without disturbing the
default Docker backend. Not either/or.

**Only hands-on check still worth doing** (folded into the plan's first task): confirm
`devcontainer up --additional-features` cleanly injects the agent Feature on a sample repo.

## 8. Prior art (why build)

The 2026 field is crowded but nothing matches the full combination. Closest:
- **Sculptor** (Imbue) — closest whole-vision (local Docker, devcontainer, "Pairing Mode" ≈
  VS Code on demand) but **closed-source, GUI-first, no CLI/skill, no PR-only, no egress firewall,
  no multi-machine**.
- **Container Use** (Dagger) — closest engine (OSS CLI, per-agent containers + branches, attach)
  but **MCP/agent-driven not host-orchestrated, Dagger-based, no security layer**.
- **Docker Sandboxes** (official) — candidate **substrate** (evaluated, deferred — §7), not a
  competitor; a possible *additional* backend later.
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
- Residual trust: the **agent's** creds (Claude oauth, `OPENAI_API_KEY`, …) run as you inside the
  box (accepted — it's your usage). No GitHub cred enters the box at all (engine-side clone only).
- Guardrails: branch protection on `main`, `max_concurrent`, per-container cpu/mem caps, auto-stop.

## 10. Open questions

- Exact done-signal / fetch-request mechanism (process-exit default vs Stop hook vs sentinel file
  vs in-container `flotilla` shim) — settle in the plan.
- Codex autonomous-mode flag (`--full-auto` keeps Codex's own sandbox vs
  `--dangerously-bypass-approvals-and-sandbox` for full autonomy inside our container) — confirm in
  the plan.
- Built-in setup-handler interface (how `builtin:<name>` handlers are registered in Go) — settle in
  the plan.
- Feature distribution: publish to GHCR vs vendor locally.
- Final name (Flotilla is a working name).
