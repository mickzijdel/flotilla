# Flotilla — Submission flow (engine push + PR)

**Date:** 2026-06-23
**Status:** Draft for review
**Scope:** "Next plan #1" from [the backlog](../backlog.md) — the engine-side half of
Flotilla's core loop: take a finished agent's local commits and turn them into a reviewable
pull request, with git credentials living **only** on the engine. Builds on the merged
devcontainer + injection slice and the egress firewall. Realises design-spec §4.5
("Isolation & submission — PR-only by construction") and §5's `flotilla submit` CLI line.

## 1. Summary

Flotilla's thesis is **sandboxed agent → reviewed PR, with no git credentials ever inside the
container**. The left half exists today: agents spawn, run isolated, and commit **locally** into an
engine-side clone that is bind-mounted into the container. The right half — getting that work out —
is missing, which leaves an agent's output trapped in the container. The only way out today is to
attach and push by hand, which requires putting credentials somewhere and breaks the one security
property the project is built around.

This slice adds **`flotilla submit <agent>`**: the engine inspects the agent's host-side clone,
pushes its commits to a stable branch using the engine's own credentials, and opens (or updates) a
pull request. Because the container never holds a remote git credential, the worst case remains "an
unreviewed PR appears" — never a leaked token or a force-push to a protected branch.

The approach is **essentially forced** by the security model: given "no creds in the box," the engine
is the only place push/PR can happen. The alternatives (a credential-mediating push proxy; letting the
agent push; emitting only a patch/bundle) each either reintroduce the attack surface we removed or drop
the reviewed-PR workflow. So the design's only real degrees of freedom are ergonomics and packaging,
captured below.

## 2. Decisions locked (from brainstorming, 2026-06-23)

| # | Area | Decision |
|---|---|---|
| 1 | Trigger | **Manual, status-gated.** `flotilla submit <agent>` refuses unless the container has **exited** (the `process-exit` done-signal). `--force` overrides the status gate only. No background daemon — fits the stateless-CLI model; "auto on done-signal" is deferred. |
| 2 | Work state | **Strict.** Require a **clean working tree** with **≥1 commit ahead of base**. A dirty tree is refused ("commit inside the container first"); zero commits is refused ("nothing to submit"). The engine never auto-commits. |
| 3 | Branch | **Stable `flotilla/<agent>`**, pushed with `--force-with-lease`. Re-submitting updates the branch in place; an open PR auto-reflects the new commits. |
| 4 | PR mechanism | **`gh pr create --fill`** when `gh` is installed + authenticated and the remote is GitHub; otherwise **push-only fallback** — branch is pushed and the compare URL is printed for the user to open the PR manually. |
| 5 | PR content | **From the agent's commit messages** (`--fill`). "Let the agent write it" — every CLI agent commits, nothing extra pollutes the diff, no new convention. |
| 6 | Wrap-up | **Both layers.** A universal `wrap_up` prompt-contract (new profile field, default text) tells any agent to commit before finishing; plus a **Claude-specific Stop hook** that commits leftovers as a safety net. |
| 7 | Structure | **gitops primitives + new `forge` package + `Fleet.Submit` + `submit` CLI** (approach A — mirrors existing layering, fakeable `gh` seam). |
| 8 | Attach | **`attach` auto-starts an exited container** so a shell is always reachable — the clean recovery path for the strict-refuse case post-exit. |

## 3. Architecture

### 3.1 Where work lives, and why host-side git is safe

The engine clones the repo to `dest = ~/.flotilla/work/<agent>/` ([fleet.go](../../internal/fleet/fleet.go)),
then `devcontainer up --workspace-folder dest` bind-mounts `dest` **read-write** into the container.
That bind mount means **the host clone *is* the agent's working tree** — the agent's commits land
directly in `dest/.git` on the host. So the engine performs all submission git/gh operations
**host-side** against `dest` (via `git -C dest …` / `gh` with cwd `dest`); nothing execs into the
container, and the container — which holds no git credential — is never involved in the push.

### 3.2 Package boundaries (approach A)

```
flotilla submit <agent>
        │   cli.submitCmd
        ▼
   Fleet.Submit(ctx, name, force)          internal/fleet  (orchestration; sibling of Spawn/Stop)
        │
        ├─ resolve(name) ───────────────►  backend (existing): find container, exclude proxy
        ├─ status gate (exited?) ───────►  backend.Container.Status
        ├─ gitops.Inspect(dest) ────────►  internal/gitops (pure git, host-side, read-only)
        ├─ gitops.Push(dest, branch) ───►  internal/gitops (force-with-lease)
        └─ forge.EnsurePR(dest, …) ─────►  internal/forge  (gh wrapper + push-only fallback)
```

- **`internal/gitops`** (extend): pure git, no PR knowledge. New `Inspect` and `Push`.
- **`internal/forge`** (new): wraps `gh`, owns the PR-vs-push-only decision. A `Forge` interface with a
  real `gh` impl and a `fakeForge` for tests — the same seam pattern as `backend.Backend`/`Fake`.
- **`internal/fleet`** (extend): `Submit` orchestrator + `Submission` result; `Attach` gains auto-start.
- **`internal/cli`** (extend): `submitCmd`; `doctor` gains an advisory `gh` check.
- **`internal/agent`** (extend): `Profile.WrapUp` field + default; built-in profiles set wrap-up text;
  the `claude` profile additionally declares a Stop hook.

### 3.3 Type contracts

```go
// internal/gitops
type WorkState struct {
    Base         string // base branch (from origin/HEAD), e.g. "main"
    CommitsAhead int    // commits on HEAD not on origin/<Base>
    Dirty        bool   // uncommitted changes (tracked or untracked)
    RemoteURL    string // origin URL (forge detection + compare URL)
}
func Inspect(ctx context.Context, dir string) (WorkState, error)
func Push(ctx context.Context, dir, branch string) error // git push --force-with-lease origin HEAD:refs/heads/<branch>

// internal/forge
type PRResult struct {
    URL      string // PR URL, or compare URL on fallback
    Created  bool   // true = a new PR was opened (false = existing PR, or push-only)
    PushOnly bool   // true = gh unavailable / non-GitHub remote; no PR opened
}
type Forge interface {
    Available(ctx context.Context) bool
    EnsurePR(ctx context.Context, dir, branch string, st gitops.WorkState) (PRResult, error)
}

// internal/fleet
type Submission struct {
    Agent    string `json:"agent"`
    Branch   string `json:"branch"`
    PRURL    string `json:"prURL"`
    Created  bool   `json:"created"`
    PushOnly bool   `json:"pushOnly"`
}
func (f *Fleet) Submit(ctx context.Context, name string, force bool) (Submission, error)
```

## 4. The submit flow

`Fleet.Submit(ctx, name, force)`, host-side against `dest = ~/.flotilla/work/<name>/`:

1. **Resolve** via the existing `resolve(ctx, name)` (excludes the proxy sidecar). Absent → `no agent named %q`.
2. **Status gate (done-signal).** This is where `done_signal = "process-exit"` finally gets meaning:
   require `container.Status == "exited"`. Still running and no `--force` → refuse:
   *"agent <name> is still running; wait for it to finish or pass --force"*. `--force` bypasses only
   this gate. We do **not** look up the agent's profile — an exited container *is* the process-exit
   signal universally; other `done_signal` modes are future work.
3. **Inspect** (`gitops.Inspect`):
   - `Dirty` → refuse: *"agent <name> has uncommitted changes; commit them inside the container first"*.
   - `CommitsAhead == 0` → refuse: *"nothing to submit: agent <name> has no commits beyond <Base>"*.
   - else proceed with `Base`, `CommitsAhead`, `RemoteURL`.
4. **Push** (`gitops.Push`): `git push --force-with-lease origin HEAD:refs/heads/flotilla/<name>`.
5. **Ensure PR** (`forge.EnsurePR(ctx, dest, "flotilla/<name>", st)`):
   - `gh` available + GitHub remote → existing PR for the branch returns it (`Created:false`);
     else `gh pr create --fill` (title/body from commit messages).
   - else → **push-only fallback**: the branch is already pushed; return the compare URL
     `<RemoteURL>/compare/<Base>...flotilla/<name>`, `PushOnly:true`.
6. **Return** `Submission{Agent, Branch, PRURL, Created, PushOnly}`.

### 4.1 Base-branch discovery

The base is **discovered, never assumed `main`**: `git symbolic-ref refs/remotes/origin/HEAD`
(e.g. `origin/main` → `main`), falling back to the clone's current branch if unset. `CommitsAhead` is
`git rev-list --count origin/<Base>..HEAD`. The PR's base defaults to `<Base>`; `gh pr create --fill`
targets the repo's default branch.

### 4.2 Ownership & permissions (host-side git on container-written files)

The clone's `dest/` and `dest/.git/` are created by the engine (host) user, so the **directory**
owners match the engine's euid. The agent's commits write *new files inside* `.git` as the container's
`remoteUser`. Crucially, the devcontainer CLI defaults `updateRemoteUserUID: true` (we don't override
it), which remaps the `remoteUser`'s uid/gid to the host engine user — so in the **common case the
agent's commits land owned by the host engine uid** and host-side git Just Works.

Mismatch only reappears in edge cases: a **root `remoteUser`** (remap doesn't apply to root), a repo
`devcontainer.json` setting `updateRemoteUserUID: false`, or **rootless / userns-remapped Docker**.
Two distinct failure modes and their mitigations, built in from the start:

- **Git's dubious-ownership guard** (CVE-2022-24765) — refuses when the repo *directory* owner ≠ euid.
  Mitigation: every host git call passes `-c safe.directory=<dest>` (scoped to that path, never the
  global `*`).
- **Permission-denied writing inside `.git`** — e.g. `git status` refreshing a root-owned
  `.git/index`. Mitigation: `Inspect` uses **read-only plumbing that never rewrites `.git`**
  (`git rev-list --count`, `git ls-files` / `git diff --quiet`, `git symbolic-ref`) instead of
  `git status`. `git push` only *reads* local objects (world-readable) and writes to the remote, so the
  push is safe regardless of owner.

If a git call still fails on ownership, the engine surfaces git's stderr verbatim rather than masking it.

## 5. Agent wrap-up contract

Goal: by the time the agent process exits, the tree is clean with ≥1 well-messaged commit, so
strict-refuse (decision #2) does not dead-end the happy path. Two layers:

### 5.1 Baseline — prompt-injected contract (all agents)

A new profile field `wrap_up` (string, with a built-in default) whose text the engine appends, clearly
delimited, to the prompt it already writes at spawn ([fleet.go:135](../../internal/fleet/fleet.go#L135)).
Default wording:

> *Before you finish, commit all your changes with clear, descriptive messages — your commit messages
> become the pull request. Do not leave uncommitted work; anything uncommitted will be discarded and the
> submission rejected.*

Agent-agnostic, declarative, no per-agent code; a profile can override the wording or set it empty to
disable.

### 5.2 Enhancement — Claude Stop hook (where supported)

The `claude` profile additionally declares a Claude Code **Stop hook** that runs
`git add -A && git commit` as a safety net for anything the agent left uncommitted. This is genuinely
agent-specific (Codex has no equivalent), so it lives as a profile-declared hook, not the baseline —
exactly the "richer hook where the agent supports one" the design spec anticipates. Installed via the
existing `setup` handler / config-injection path at spawn.

## 6. Interaction & recovery

Submission is **purely additive** — `attach`, `stop`, `rm` are untouched, and submit removes neither
the container nor the clone (so you can inspect, re-run, and re-submit; cleanup stays explicit via `rm`).

- **While running:** `flotilla attach <agent>` is `docker exec -it` into the live container — your
  escape hatch to curate commits by hand before the agent exits.
- **After exit (the wrinkle):** the `process-exit` signal leaves the container *stopped but present*,
  and `docker exec` doesn't work on a stopped container. So `attach` **auto-starts an exited container**
  (decision #8) before exec'ing, making a shell always reachable. Independently, the host clone at
  `~/.flotilla/work/<agent>/` *is* the working tree, so a dirty tree can also be recovered with
  `git -C ~/.flotilla/work/<agent> add -A && git commit` (modulo §4.2 ownership). The wrap-up contract
  (§5) is designed so this recovery is rarely needed.

## 7. Error handling

Every failure is a clear, actionable error with no silent partial state:

| Condition | Behaviour |
|---|---|
| Agent not found | `no agent named %q` |
| Still running, no `--force` | refuse with wait/`--force` hint |
| Dirty tree | refuse: commit in-container first (strict) |
| Zero commits ahead of base | `nothing to submit` |
| Push fails (network/auth/ownership) | surface git stderr verbatim; `safe.directory` always set |
| `gh` present but PR-create fails | report it; branch is already pushed, so still print the compare URL |
| Re-submit | force-with-lease updates the branch; existing PR detected-and-skipped (never errors) |

## 8. CLI surface

`flotilla submit <agent> [--force] [--json]`:

- Human output: `Pushed flotilla/<agent> → opened PR <url>` / `→ updated existing PR <url>` /
  `→ open a PR: <compare-url>` (push-only).
- `--json`: the `Submission` struct.
- `doctor` gains an **advisory** check: is `gh` installed + authenticated? (Submit still works
  push-only without it.)

## 9. Testing

- **`gitops.Inspect` / `Push`** — real-git tests in a temp dir: init a bare "remote", clone, commit,
  assert `CommitsAhead`/`Dirty`/`Base`/`RemoteURL` and that `Push` lands the branch on the bare remote.
  No Docker.
- **`forge`** — `Forge` interface exercised via `fakeForge`; the real `gh` impl gets a thin test gated on
  `gh` availability (self-skips like the Docker backend test).
- **`fleet.Submit`** — in-memory `backend.Fake` + `fakeForge` + a temp git clone; assert the status gate,
  strict-tree refusals, branch name, update-in-place, and push-only fallback.
- **`Fleet.Attach`** — assert it starts an exited container before returning attach info (via `Fake`).
- **CLI** — wire test for flag parsing and the three output shapes.
- **Profiles** — assert `wrap_up` default is present and appended to the injected prompt; assert the
  `claude` profile carries the Stop-hook config.

## 10. Out of scope (future)

- **Auto-submit on done-signal** (needs a watcher/daemon — deliberately deferred per decision #1).
- **Richer `done_signal` modes** beyond `process-exit`.
- **`--title`/`--body` overrides** (commit-message `--fill` is the v1 channel).
- **Per-repo PR templating / labels / reviewers.**
- **Non-GitHub forges** beyond the push-only fallback (e.g. GitLab `glab`).
