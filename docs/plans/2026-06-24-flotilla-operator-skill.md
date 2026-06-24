# Flotilla Operator Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a `flotilla-operator` Claude Code skill that teaches a host-side agent to drive the
`flotilla` CLI, embedded in the binary and installed via `flotilla skill install`, with self-healing
auto-refresh on every binary upgrade and a drift-guard test that keeps the skill honest against the CLI.

**Architecture:** A new `internal/skill` package embeds `operator/SKILL.md` via `go:embed` and exposes
`Install` / `RefreshIfStale` / `Hash`. A `flotilla skill install` subcommand writes the skill into
`~/.claude/skills/flotilla-operator/` alongside a content-hash marker. `main.go` calls `RefreshIfStale`
on startup so an upgraded binary silently refreshes an already-installed skill. A drift-guard test in
`internal/cli` parses the embedded SKILL.md and asserts every command/flag it references actually exists
in the cobra tree.

**Tech Stack:** Go 1.26.4, cobra v1.10.2, stdlib `embed` + `crypto/sha256` (no new dependencies).

## Global Constraints

- Go: 1.26.4 (`go` directive in go.mod); cobra v1.10.2. **No new third-party dependencies** — use stdlib
  `embed`, `crypto/sha256`, `encoding/hex`, `os`, `path/filepath`.
- Module path: `github.com/mickzijdel/flotilla`.
- Follow existing `internal/cli` patterns: commands return `*cobra.Command`, write to
  `cmd.OutOrStdout()`, are wired in `BuildRoot` (`internal/cli/cli.go:20`).
- Tests are Docker-free here (pure filesystem + in-memory cobra); `go test ./...` must stay green, CI runs
  `-race`.
- The skill installs to `~/.claude/skills/flotilla-operator/` (`skill.Dir(home)`); the marker file is
  `.flotilla-skill-hash`.
- The shipped SKILL.md must reference **only commands/flags that exist today** — no remote `--host` /
  `--all-hosts` (that folds in when the remote backend lands). The drift-guard test enforces this.

---

### Task 1: `internal/skill` package — embed, install, self-healing refresh

**Files:**
- Create: `internal/skill/operator/SKILL.md`
- Create: `internal/skill/skill.go`
- Test: `internal/skill/skill_test.go`

**Interfaces:**
- Consumes: nothing (leaf package).
- Produces:
  - `skill.Name` (`const` = `"flotilla-operator"`)
  - `skill.Embedded() []byte`
  - `skill.Hash() string` — hex SHA-256 of the embedded SKILL.md
  - `skill.Dir(home string) string` — `<home>/.claude/skills/flotilla-operator`
  - `skill.Install(dir string) (mdPath string, err error)` — writes SKILL.md + marker, idempotent
  - `skill.RefreshIfStale(dir string) (refreshed bool, err error)` — rewrites only when dir exists and
    its marker hash differs from `Hash()`; no-op when dir is absent or already current

- [ ] **Step 1: Create the embedded skill asset**

Create `internal/skill/operator/SKILL.md` with the full operator skill (modelled on `playwright-cli`).
Every command and flag below is real (verified against `internal/cli`); the drift test (Task 4) guards it.

````markdown
---
name: flotilla-operator
description: Drive the flotilla CLI to run and supervise a fleet of sandboxed coding agents. Use when asked to run/spawn a flotilla agent on a repo, manage the fleet, see what agents are doing, answer an agent's question, fetch new refs into an agent, or submit an agent's work as a PR.
---

# Driving flotilla (operator skill)

`flotilla` runs each coding agent in its own Docker container on an engine-side clone of a repo, with
**no git credentials inside the container**. The engine (this CLI) owns clone/fetch/push/PR, so
submission is PR-only by construction. You are the *operator*: you drive the `flotilla` binary on the
host to spawn agents, watch them, answer their questions, and ship their work.

Parse `--json` output, never the human table. Let agents finish before submitting (submit is gated on
the container being exited). Quote prompts/answers that contain shell metacharacters.

## Quick start

```
flotilla doctor                         # check docker / devcontainer / gh / daemon
flotilla spawn https://github.com/me/app.git --prompt "fix the flaky test in auth_test.go"
flotilla list --json                    # see the fleet (name, repo, status, id)
flotilla logs brave-otter --follow      # stream until the agent is done
flotilla questions --json               # any agent blocked waiting on you?
flotilla answer brave-otter "use the v2 endpoint"
flotilla submit brave-otter --json      # push + open/update the PR
```

## Command reference

### Discovery / preflight
- `flotilla doctor` — checks docker, the docker daemon, the devcontainer CLI, `gh` auth, and daemon
  state. Run it first; a non-zero exit means a prerequisite is missing.
- `flotilla agents` — list available agent profiles (built-ins: `claude`, `codex`).

### Lifecycle
- `flotilla spawn <repo> [--agent claude] [--prompt "..."] [--no-egress-firewall]` — engine-side clone
  then start an agent. Prints `name<TAB>status<TAB>id`. Best-effort starts the daemon.
- `flotilla list [--json]` — the fleet. JSON is an array of `{name, repo, status, created, id}`.
- `flotilla attach <agent>` — prints the `docker exec ...` line and VS Code attach info (auto-starts an
  exited container).
- `flotilla stop <agent>` / `flotilla rm <agent>` — stop / remove the container.

### Supervision
- `flotilla inbox [--json] [--watch] [--since <RFC3339>]` — daemon events (agent done, PR opened, submit
  skipped, question, fetch_done). `--json` is JSONL; `--watch` streams.
- `flotilla questions [--json] [--watch]` — agents currently **blocked** awaiting an answer. Rows are
  `agent<TAB>id<TAB>age<TAB>text`.
- `flotilla answer <agent> <text> [--id <qid>]` — unblock an agent. `--id` is only needed when several
  questions are pending for that agent.
- `flotilla logs <agent> [--follow] [--json]` — stream `container.log`. `--follow` runs until the
  session status reads `done`. `--json` returns `{logDir, status, transcript}` instead of the log.
- `flotilla fetch <agent>` — re-`git fetch origin` into a running agent's clone (the container holds no
  git credentials), so the agent can integrate new refs locally.

### Submission
- `flotilla submit <agent> [--force] [--json]` — push `flotilla/<agent>` (force-with-lease) and
  open/update a PR via `gh`. **Refuses a still-running agent unless `--force`.** JSON:
  `{agent, branch, prURL, created, pushOnly, note}`. With no `gh`, it pushes and returns a compare URL
  (`pushOnly: true`).

### Daemon (optional supervisor)
- `flotilla daemon start | stop | status [--json]` — the auto-submit + inbox supervisor. Everything
  works without it; with it, a finished agent auto-submits and `inbox` populates. `status --json` reports
  `{running, pid, watchedAgents, recent}`.

### Skill maintenance
- `flotilla skill install [--dir <path>]` — install/refresh this skill into `~/.claude/skills`. The
  binary also auto-refreshes it on upgrade, so you rarely run this by hand.

## The status model (read this)

- **`list` statuses:** `running`, `exited`, and a derived **`blocked`** overlay — a running agent with a
  pending operator question. `blocked` means: go run `flotilla questions` then `flotilla answer`.
- **Session status:** the agent's own `running` → `done` signal (what `logs --follow` waits for and what
  the daemon auto-submits on). `done` = the agent finished; `exited` = the container stopped.
- **Submit gate:** `submit` needs the container `exited` (agent stopped) unless `--force`. Let the agent
  finish, or `stop` it, before submitting.
- **No creds in the container:** the agent cannot `git fetch`/push itself — that is why `fetch` and
  `submit` are *operator* verbs you run from the host.

## Examples

### Run an agent and ship it
```
name=$(flotilla spawn https://github.com/me/app.git --prompt "add retries to the http client" | cut -f1)
flotilla logs "$name" --follow          # blocks until done
flotilla submit "$name" --json          # → {"branch":"flotilla/...","prURL":"https://...","created":true}
```

### Supervise a blocked fleet
```
flotilla list --json | jq -r '.[] | select(.status=="blocked") | .name' | while read -r a; do
  flotilla questions --json | jq -r ".[] | select(.agent==\"$a\") | .text"
  flotilla answer "$a" "your decision here"
done
```

### Bring an agent up to date after upstream moved
```
flotilla fetch brave-otter
flotilla inbox --json | tail -1        # confirm a fetch_done event
```

## Gotchas
- **Submit is exited-gated.** A running agent won't submit without `--force`; prefer letting it finish.
- **The daemon is optional.** If `inbox` is empty, check `flotilla daemon status` — without the daemon
  there are no auto events, but every command still works.
- **Quote metacharacters.** Prompts/answers with `"`, `$`, or backticks must be quoted — they are
  interpolated into a shell on the way to the agent.
- **`--id` only when ambiguous.** `answer` needs `--id` only if an agent has more than one pending
  question.
````

- [ ] **Step 2: Write the failing test for the skill package**

Create `internal/skill/skill_test.go`:

```go
package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedAndHash(t *testing.T) {
	if len(Embedded()) == 0 {
		t.Fatal("embedded SKILL.md is empty")
	}
	if len(Hash()) != 64 {
		t.Fatalf("hash should be 64 hex chars, got %q", Hash())
	}
}

func TestDir(t *testing.T) {
	got := Dir("/home/x")
	want := filepath.Join("/home/x", ".claude", "skills", "flotilla-operator")
	if got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestInstallWritesSkillAndMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flotilla-operator")
	path, err := Install(dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed: %v", err)
	}
	if string(got) != string(Embedded()) {
		t.Error("installed SKILL.md does not match embedded content")
	}
	marker, err := os.ReadFile(filepath.Join(dir, ".flotilla-skill-hash"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(marker) != Hash() {
		t.Errorf("marker = %q, want %q", marker, Hash())
	}
}

func TestRefreshIfStaleNoopWhenAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-installed")
	refreshed, err := RefreshIfStale(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed {
		t.Error("RefreshIfStale should not create a fresh install")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("RefreshIfStale must not create the dir when absent")
	}
}

func TestRefreshIfStaleNoopWhenCurrent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flotilla-operator")
	if _, err := Install(dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	refreshed, err := RefreshIfStale(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed {
		t.Error("RefreshIfStale should be a no-op when already current")
	}
}

func TestRefreshIfStaleRewritesWhenStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flotilla-operator")
	if _, err := Install(dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Simulate an upgraded binary: clobber the on-disk skill + marker.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flotilla-skill-hash"), []byte("deadbeef"), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshed, err := RefreshIfStale(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !refreshed {
		t.Fatal("RefreshIfStale should rewrite a stale install")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if string(got) != string(Embedded()) {
		t.Error("stale SKILL.md was not refreshed to embedded content")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/skill/`
Expected: FAIL — `undefined: Embedded` / `Install` / `RefreshIfStale` (package `skill.go` not written yet).

- [ ] **Step 4: Write the package implementation**

Create `internal/skill/skill.go`:

```go
// Package skill embeds the flotilla-operator Claude Code skill and installs it
// into the user's ~/.claude/skills directory, refreshing it when the binary is
// upgraded.
package skill

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
)

//go:embed operator/SKILL.md
var operatorMD []byte

// Name is the installed skill's directory name under ~/.claude/skills.
const Name = "flotilla-operator"

// markerFile records the content hash of the last-installed SKILL.md so an
// upgraded binary can detect a stale on-disk copy and refresh it.
const markerFile = ".flotilla-skill-hash"

// Embedded returns the SKILL.md bytes baked into the binary.
func Embedded() []byte { return operatorMD }

// Hash is the hex SHA-256 of the embedded SKILL.md.
func Hash() string {
	sum := sha256.Sum256(operatorMD)
	return hex.EncodeToString(sum[:])
}

// Dir returns the install directory for a given home dir.
func Dir(home string) string {
	return filepath.Join(home, ".claude", "skills", Name)
}

// Install writes SKILL.md and the hash marker into dir, creating it. Idempotent;
// returns the SKILL.md path.
func Install(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	mdPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(mdPath, operatorMD, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, markerFile), []byte(Hash()), 0o644); err != nil {
		return "", err
	}
	return mdPath, nil
}

// RefreshIfStale rewrites the skill only when dir already exists and its marker
// hash differs from the embedded hash (i.e. the binary was upgraded). It never
// creates dir from scratch, so a user who never installed — or deliberately
// removed — the skill is left alone. Returns true when a refresh was written.
func RefreshIfStale(dir string) (bool, error) {
	if _, err := os.Stat(dir); err != nil {
		return false, nil // not installed → leave alone
	}
	marker, err := os.ReadFile(filepath.Join(dir, markerFile))
	if err == nil && string(marker) == Hash() {
		return false, nil // already current
	}
	if _, err := Install(dir); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/skill/`
Expected: PASS (all six tests).

- [ ] **Step 6: Commit**

```bash
git add internal/skill/
git commit -m "feat(skill): embed flotilla-operator skill with install + self-healing refresh"
```

---

### Task 2: `flotilla skill install` command

**Files:**
- Create: `internal/cli/skill.go`
- Modify: `internal/cli/cli.go:20` (add `skillCmd()` to `BuildRoot`'s `AddCommand` list)
- Test: `internal/cli/skill_test.go`

**Interfaces:**
- Consumes: `skill.Install`, `skill.Dir`, `skill.Name` (Task 1).
- Produces: `skillCmd() *cobra.Command` (no Fleet needed), wired into the root.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/skill_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func TestSkillInstallWritesToDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flotilla-operator")
	root := BuildRoot(&fleet.Fleet{Backend: backend.NewFake()})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"skill", "install", "--dir", dir})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not installed: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Installed")) {
		t.Errorf("expected an Installed confirmation, got %q", out.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestSkillInstall`
Expected: FAIL — `unknown command "skill"` (command not wired yet).

- [ ] **Step 3: Write the command**

Create `internal/cli/skill.go`:

```go
package cli

import (
	"fmt"
	"os"

	"github.com/mickzijdel/flotilla/internal/skill"
	"github.com/spf13/cobra"
)

func skillCmd() *cobra.Command {
	c := &cobra.Command{Use: "skill", Short: "Manage the flotilla-operator Claude Code skill"}
	c.AddCommand(skillInstallCmd())
	return c
}

func skillInstallCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "install",
		Short: "Install the flotilla-operator skill into ~/.claude/skills",
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := dir
			if target == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("locate home dir: %w", err)
				}
				target = skill.Dir(home)
			}
			path, err := skill.Install(target)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Installed %s skill → %s\n", skill.Name, path)
			return err
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "install directory (default ~/.claude/skills/flotilla-operator)")
	return c
}
```

- [ ] **Step 4: Wire it into the root**

In `internal/cli/cli.go`, add `skillCmd()` to the `AddCommand` call at line 20:

```go
root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), submitCmd(f), fetchCmd(f), logsCmd(f), daemonCmd(f), inboxCmd(f), questionsCmd(f), answerCmd(f), agentsCmd(), doctorCmd(), skillCmd())
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/cli/ -run TestSkillInstall`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/skill.go internal/cli/skill_test.go internal/cli/cli.go
git commit -m "feat(cli): flotilla skill install command"
```

---

### Task 3: Auto-refresh the installed skill on binary upgrade

**Files:**
- Modify: `main.go:14-26` (call `skill.RefreshIfStale` before `root.Execute`)

**Interfaces:**
- Consumes: `skill.RefreshIfStale`, `skill.Dir` (Task 1).
- Produces: nothing new; entrypoint glue only.

**Why no unit test here:** `main.go` is the thin process entrypoint (untested by convention in this repo);
all refresh logic is already covered by `internal/skill/skill_test.go` (Task 1, Steps 2–5). The change is
a best-effort call whose failure must never block a command.

- [ ] **Step 1: Add the refresh call to main.go**

Modify `main.go` so the body reads:

```go
func main() {
	f := &fleet.Fleet{
		Backend:        backend.NewDocker(),
		BaseImage:      "ubuntu:24.04",
		EgressFirewall: true,
		Forge:          forge.NewGH(),
	}
	// Self-healing skill: if the operator already installed the skill, silently
	// refresh it to match this (possibly upgraded) binary. Never blocks a command.
	if home, err := os.UserHomeDir(); err == nil {
		if refreshed, err := skill.RefreshIfStale(skill.Dir(home)); err == nil && refreshed {
			fmt.Fprintln(os.Stderr, "flotilla: refreshed flotilla-operator skill to match this binary")
		}
	}
	root := cli.BuildRoot(f)
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

Add the import `"github.com/mickzijdel/flotilla/internal/skill"` to the import block.

- [ ] **Step 2: Build and smoke-test the refresh end to end**

```bash
go build -o /tmp/flotilla . && \
HOME=/tmp/flotest /tmp/flotilla skill install && \
echo stale > /tmp/flotest/.claude/skills/flotilla-operator/.flotilla-skill-hash && \
HOME=/tmp/flotest /tmp/flotilla agents 2>&1 | grep -q "refreshed flotilla-operator" && \
echo "REFRESH-OK" && \
diff <(HOME=/tmp/flotest cat /tmp/flotest/.claude/skills/flotilla-operator/SKILL.md) internal/skill/operator/SKILL.md && \
echo "CONTENT-MATCHES"
```

Expected: prints `Installed flotilla-operator skill → ...`, then `REFRESH-OK`, then `CONTENT-MATCHES`
(the stale marker triggered a refresh on the unrelated `agents` command, and the on-disk skill now matches
the embedded one). Clean up: `rm -rf /tmp/flotest /tmp/flotilla`.

- [ ] **Step 3: Run the full suite**

Run: `go test ./...`
Expected: PASS (Docker-gated integration test self-skips).

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: auto-refresh the operator skill on binary upgrade"
```

---

### Task 4: Drift-guard test — SKILL.md references only real commands/flags

**Files:**
- Test: `internal/cli/skill_drift_test.go`

**Interfaces:**
- Consumes: `BuildRoot`, `skill.Embedded` (Tasks 1–2), `github.com/spf13/pflag`.
- Produces: a regression test that fails when the SKILL.md names a command/flag the CLI no longer has.

- [ ] **Step 1: Write the drift-guard test**

Create `internal/cli/skill_drift_test.go`:

```go
package cli

import (
	"regexp"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/mickzijdel/flotilla/internal/skill"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestOperatorSkillMatchesCLI fails when the embedded SKILL.md references a
// flotilla command or flag that no longer exists in the cobra tree — the
// mechanical half of "double-check the skill is still accurate" (see CLAUDE.md).
func TestOperatorSkillMatchesCLI(t *testing.T) {
	root := BuildRoot(&fleet.Fleet{Backend: backend.NewFake()})

	cmds := map[string]bool{}
	flags := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		cmds[c.Name()] = true
		c.Flags().VisitAll(func(f *pflag.Flag) { flags[f.Name] = true })
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) { flags[f.Name] = true })
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)

	md := string(skill.Embedded())

	// Command tokens: "flotilla <verb>" at a line start or after a backtick
	// (i.e. in a code fence or inline code), never loose prose like "drive flotilla".
	cmdRe := regexp.MustCompile("(?m)(?:^\\s*|`)flotilla ([a-z][a-z-]+)")
	for _, m := range cmdRe.FindAllStringSubmatch(md, -1) {
		if !cmds[m[1]] {
			t.Errorf("SKILL.md references unknown command %q — drifted from the CLI", m[1])
		}
	}

	// Long flags anywhere in the doc must exist somewhere in the tree.
	flagRe := regexp.MustCompile(`--([a-z][a-z-]+)`)
	for _, m := range flagRe.FindAllStringSubmatch(md, -1) {
		if !flags[m[1]] {
			t.Errorf("SKILL.md references unknown flag --%s — drifted from the CLI", m[1])
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it passes against the current SKILL.md**

Run: `go test ./internal/cli/ -run TestOperatorSkillMatchesCLI -v`
Expected: PASS (every command/flag in the SKILL.md from Task 1 is real).

- [ ] **Step 3: Verify the guard actually catches drift (temporary mutation)**

Temporarily append a bogus reference to the embedded skill, confirm the test fails, then revert:

```bash
printf '\n`flotilla teleport --warp`\n' >> internal/skill/operator/SKILL.md
go test ./internal/cli/ -run TestOperatorSkillMatchesCLI 2>&1 | grep -E 'unknown command "teleport"|unknown flag --warp' && echo "GUARD-WORKS"
git checkout -- internal/skill/operator/SKILL.md
```

Expected: prints failures for `teleport` and `--warp`, then `GUARD-WORKS`. The `git checkout` restores the
real skill (re-run `go test ./internal/cli/ -run TestOperatorSkillMatchesCLI` to confirm green again).

- [ ] **Step 4: Commit**

```bash
git add internal/cli/skill_drift_test.go
git commit -m "test(skill): guard against SKILL.md drifting from the CLI surface"
```

---

### Task 5: Document the skill — CLAUDE.md commit note, README, backlog

**Files:**
- Modify: `CLAUDE.md` (add a skill-accuracy note under `## Development`)
- Modify: `README.md` (add an operator-skill install section)
- Modify: `docs/backlog.md` (mark item #1 built, pointing at this plan)

**Interfaces:** none (docs only).

- [ ] **Step 1: Add the CLAUDE.md commit-time note**

In `CLAUDE.md`, under the `## Development` section (after the existing bullets), add:

```markdown
- **Operator skill stays in sync with the CLI.** The `flotilla-operator` Claude Code skill is embedded
  in the binary (`internal/skill/operator/SKILL.md`) and installed via `flotilla skill install`. Whenever
  you change the CLI surface (commands, flags, or `--json` shapes in `internal/cli`), update that SKILL.md
  in the same commit and re-run `go test ./internal/cli/` — `TestOperatorSkillMatchesCLI` fails if the
  skill references a command/flag that no longer exists. Double-check the skill is still accurate before
  every commit that touches `internal/cli`.
```

- [ ] **Step 2: Add the README install section**

In `README.md`, add a section (near the usage/install docs):

```markdown
## Operator skill (Claude Code)

Flotilla ships a `flotilla-operator` skill that teaches Claude Code to drive the CLI — spawn agents,
supervise them, answer their questions, and submit PRs. Install it once:

    flotilla skill install

It lands in `~/.claude/skills/flotilla-operator/`. The skill is embedded in the binary and
**version-locked** to it: after you upgrade flotilla, the next command you run silently refreshes the
installed skill, so it never drifts from the CLI you actually have.
```

- [ ] **Step 3: Mark the backlog item built**

In `docs/backlog.md`, update the CLI-driver-skill entry (item #1 under "Next plans") to note it's built,
linking this plan alongside the existing spec link.

- [ ] **Step 4: Verify docs build/links and full suite**

Run: `go test ./...`
Expected: PASS. Visually confirm the three docs render (no broken markdown).

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md README.md docs/backlog.md
git commit -m "docs: operator skill install + commit-time accuracy note"
```

---

## Self-Review

**Spec coverage** (against `docs/specs/2026-06-24-flotilla-operator-skill-design.md`):
- §4 command surface → Task 1 Step 1 (SKILL.md reference covers every shipped command).
- §5 status model → Task 1 Step 1 ("The status model" section).
- §6 SKILL.md outline (frontmatter, quick start, reference, status model, examples, gotchas) → Task 1
  Step 1 in full.
- §8 verification (commands/flags exist, JSON shapes real, quick-start runs, trigger check) → Task 4
  (drift guard for commands/flags) + Task 3 Step 2 (live refresh smoke) + the description in Task 1
  Step 1 (trigger phrases). *Note:* live end-to-end spawn→submit and per-command JSON-shape diffs are a
  Docker/`gh`-gated manual verification step, consistent with the repo's self-skipping integration tests;
  the mechanically-checkable claims are automated here.
- §7 out-of-scope (in-container skill, remote addressing) → respected: SKILL.md omits `--host` and the
  in-container shims; install mechanism is operator-only.
- Install mechanism (embed + `flotilla skill install` + auto-refresh) → Tasks 1–3.
- "Run install on every update" requirement → Task 3 (self-healing `RefreshIfStale`).
- "CLAUDE.md note to double-check accuracy on commit" requirement → Task 5 Step 1, made enforceable by
  Task 4.

**Placeholder scan:** no TBD/TODO/"handle edge cases" — every step has concrete code or commands.

**Type consistency:** `skill.Install(dir) (string, error)`, `skill.RefreshIfStale(dir) (bool, error)`,
`skill.Dir(home) string`, `skill.Hash() string`, `skill.Embedded() []byte`, `skill.Name`, and the
marker file `.flotilla-skill-hash` are used identically across Tasks 1–4. `skillCmd()` takes no Fleet,
matching `agentsCmd()`/`doctorCmd()` in `cli.go:20`.
