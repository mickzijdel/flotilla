# Flotilla devcontainer + Feature overlay + credential/config injection — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a spawned agent actually runnable — provision the repo's devcontainer with a vendored toolchain Feature, inject the agent's token + config, and launch the agent — while pinning the credential-isolation invariant (git creds never enter the container) with a test.

**Architecture:** `Spawn` moves from "agent-as-PID-1 over a bare image" to the exec-into-idle devcontainer model: `Backend.Up` runs `devcontainer up --additional-features` (idle container), then the engine injects a `0600` secret env-file and config via `docker cp`, runs the profile's `Install`, and launches the agent backgrounded via `ExecDetached`. Three new `Backend` methods (`Up`, `ExecDetached`, `CopyTo`) keep the fake seam so `Spawn` stays unit-testable; the credential-isolation test asserts against what `Spawn` hands the fake.

**Tech Stack:** Go 1.23+ (run as `mise exec -- go ...`), `cobra`, `BurntSushi/toml`, `os/exec` (docker/devcontainer/git), `embed`, stdlib `testing`. Docker + the `@devcontainers/cli` npm package are runtime prerequisites (the latter is **not** currently installed — see Task 1 / preflight).

**Spec:** [docs/specs/2026-06-15-flotilla-devcontainer-injection-design.md](../specs/2026-06-15-flotilla-devcontainer-injection-design.md)

---

## File Structure

```
flotilla/
  internal/
    preflight/
      preflight.go          # NEW: docker/daemon/devcontainer prerequisite checks (testable seams)
      preflight_test.go     # NEW
    feature/
      feature.go            # NEW: //go:embed toolchain + Extract(destDir) -> abs feature path
      feature_test.go       # NEW
      toolchain/
        devcontainer-feature.json  # NEW: the vendored Feature manifest
        install.sh                 # NEW: installs node/git/gh/mise — NO agent CLI, NO creds
    backend/
      backend.go            # MODIFY: add Up/ExecDetached/CopyTo to interface + UpOpts type
      fake.go               # MODIFY: implement new methods; record UpCalls/DetachedCalls/CopyCalls
      fake_test.go          # MODIFY: cover the new fake methods
      devcontainer.go       # NEW: dockerBackend Up/ExecDetached/CopyTo (devcontainer/docker shellouts)
      devcontainer_test.go  # NEW: integration (skips without docker+devcontainer)
    setup/
      setup.go              # NEW: Injector iface, Run dispatch, builtin:claude/codex, declarative
      home.go               # NEW: expandHome/fileExists helpers
      setup_test.go         # NEW
    fleet/
      spawn_helpers.go      # NEW: resolveEnv/envFileContent/launchWrapper/defaultDevcontainerJSON/hasDevcontainer
      spawn_helpers_test.go # NEW
      injector.go           # NEW: adapts Backend+id to setup.Injector (docker cp routing)
      fleet.go              # MODIFY: rewire Spawn to the new flow
      fleet_test.go         # MODIFY: failUpBackend (was failCreateBackend); spawn test still green
      credisolation_test.go # NEW: the marquee invariant test
    agent/
      builtin/claude.toml   # MODIFY: env=CLAUDE_CODE_OAUTH_TOKEN, install=npm
      builtin/codex.toml    # MODIFY: tidy config_mounts (handler owns ~/.codex)
      profile_test.go       # MODIFY: lock claude env/install
    cli/
      cli.go                # MODIFY: add doctor cmd + preflight gate in spawn
  docs/
    notes/2026-06-15-devcontainer-spike.md  # NEW (Task 1 findings)
  README.md                 # MODIFY: status note
  docs/backlog.md           # MODIFY: mark next-plan #1 done; drop resolved items
```

**Type contracts introduced here (names exact, used across tasks):**

- `backend.UpOpts{ Name, WorkspaceFolder, ConfigPath string; AdditionalFeatures map[string]any; Labels map[string]string }`
- `backend.Backend` gains: `Up(ctx, UpOpts) (string, error)`, `ExecDetached(ctx, id string, cmd []string) error`, `CopyTo(ctx, id, hostPath, destPath string) error`
- `backend.CopyCall{ ID, HostPath, DestPath string; Content []byte }` (fake recording)
- `setup.Injector` interface: `Exec(ctx, cmd []string) error`, `CopyTo(ctx, hostPath, destPath string) error`, `WriteFile(ctx, content []byte, destPath string) error`
- `setup.Handler func(ctx, Injector, agent.Profile) error`; `setup.Run(ctx, Injector, agent.Profile) error`
- `feature.Extract(destDir string) (string, error)`
- `preflight.Report{Docker, DockerDaemon, Devcontainer bool}`; `preflight.Deps{Look, Daemon}`; `preflight.Check(ctx, Deps) Report`; `preflight.Real() Deps`
- fleet helpers: `resolveEnv(keys []string, look func(string)(string,bool)) map[string]string`, `envFileContent(map[string]string) []byte`, `launchWrapper(launch string) []string`, `defaultDevcontainerJSON(baseImage string) []byte`, `hasDevcontainer(dir string) bool`, const `agentEnvFile = "/run/flotilla/agent.env"`

---

## Task 1: Spike — verify devcontainer mechanics (manual; de-risks the build)

This is the "only hands-on check still worth doing" from the design spec §7, plus the open items in §13. It is exploratory (not TDD); record outcomes in a committed note. Later tasks ship robust defaults regardless of the result, so this confirms rather than blocks.

**Files:**
- Create: `docs/notes/2026-06-15-devcontainer-spike.md`

- [ ] **Step 1: Ensure prerequisites**

```bash
docker info >/dev/null && echo "docker ok"
command -v devcontainer || npm i -g @devcontainers/cli
devcontainer --version
```
Expected: a version prints (install it if missing).

- [ ] **Step 2: Confirm `--additional-features` injects a local-path Feature**

```bash
tmp=$(mktemp -d); mkdir -p "$tmp/repo" "$tmp/feat/probe"
cat > "$tmp/feat/probe/devcontainer-feature.json" <<'JSON'
{ "id": "probe", "version": "0.0.1", "name": "probe" }
JSON
cat > "$tmp/feat/probe/install.sh" <<'SH'
#!/usr/bin/env bash
set -e; echo "PROBE_FEATURE_RAN" > /probe-marker
SH
chmod +x "$tmp/feat/probe/install.sh"
git -C "$tmp/repo" init -q
devcontainer up --workspace-folder "$tmp/repo" \
  --config "$tmp/cfg.json" \
  --additional-features "{\"$tmp/feat/probe\":{}}" 2>&1 | tail -5
# (write $tmp/cfg.json first if --config-outside-workspace is required; see Step 3)
```
Record: does `devcontainer up` accept `--config` pointing **outside** the workspace folder, or must the config live at `<workspace>/.devcontainer/devcontainer.json`? Capture the exact `containerId` JSON line emitted on the final line of stdout.

- [ ] **Step 3: Confirm the default-config delivery mechanism**

Write a minimal external config and confirm which flag delivers it for a repo with no `.devcontainer/`:
```bash
cat > "$tmp/cfg.json" <<'JSON'
{ "name": "probe-default", "image": "mcr.microsoft.com/devcontainers/base:ubuntu", "overrideCommand": true }
JSON
```
Try `--config "$tmp/cfg.json"`; if rejected, try `--override-config "$tmp/cfg.json"`; if both fail for an external path, note that the fallback is writing into `<clone>/.devcontainer/devcontainer.json`. **Record the winning flag** — Task 4's `Up` and Task 8's Spawn use it.

- [ ] **Step 4: Confirm the secret reaches a detached exec**

```bash
CID=$(docker ps -q --filter "label=devcontainer.local_folder=$tmp/repo" | head -1)
# env-file path (this plan's mechanism):
printf 'PROBE_SECRET=hunter2\n' > "$tmp/agent.env"; chmod 600 "$tmp/agent.env"
docker exec "$CID" mkdir -p /run/flotilla
docker cp "$tmp/agent.env" "$CID:/run/flotilla/agent.env"
docker exec -d "$CID" sh -c 'set -a; . /run/flotilla/agent.env; set +a; echo "$PROBE_SECRET" > /secret-seen'
sleep 1; docker exec "$CID" cat /secret-seen   # want: hunter2
# also note whether `devcontainer up --secrets-file` alone would have reached this detached exec
```
Record whether the env-file mechanism works (expected: yes) and whether `--secrets-file` alone would suffice (decision #3 fallback).

- [ ] **Step 5: Capture headless-Claude config needs (if a token is available)**

If you have a `CLAUDE_CODE_OAUTH_TOKEN`, install Claude in the probe container and run it headless to see what (if any) `~/.claude/settings.json` keys it needs to avoid first-run/trust prompts:
```bash
docker exec "$CID" bash -lc 'npm i -g @anthropic-ai/claude-code'
docker exec -e CLAUDE_CODE_OAUTH_TOKEN="$CLAUDE_CODE_OAUTH_TOKEN" "$CID" \
  bash -lc 'cd /tmp && claude --dangerously-skip-permissions -p "print the word ok" 2>&1 | tail -20'
```
Record any settings/onboarding/trust complaints. These refine Task 5's `claudeSetup` minimal `settings.json` (default ships `{}`).

- [ ] **Step 6: Write findings + commit**

Write `docs/notes/2026-06-15-devcontainer-spike.md` capturing: the config-delivery flag (Step 3), the containerId parse format (Step 2), secret mechanism confirmation (Step 4), and any Claude settings needed (Step 5). Then:
```bash
docker rm -f "$CID" 2>/dev/null; rm -rf "$tmp"
git add docs/notes/2026-06-15-devcontainer-spike.md
git commit -m "docs(spike): verify devcontainer up + feature + secret transport mechanics"
```

> **Orchestrator note:** carry Step 3 (config flag) and Step 5 (settings) findings into Tasks 4 and 5.

---

## Task 2: Preflight checks + `doctor` command + spawn gate

**Files:**
- Create: `internal/preflight/preflight.go`
- Create: `internal/preflight/preflight_test.go`
- Modify: `internal/cli/cli.go`

- [ ] **Step 1: Write the failing test**

`internal/preflight/preflight_test.go`:
```go
package preflight

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestCheckAllPresent(t *testing.T) {
	d := Deps{
		Look:   func(string) (string, error) { return "/usr/bin/x", nil },
		Daemon: func(context.Context) error { return nil },
	}
	r := Check(context.Background(), d)
	if !r.OK() {
		t.Fatalf("want OK, got %+v", r)
	}
}

func TestCheckMissingDevcontainer(t *testing.T) {
	d := Deps{
		Look: func(name string) (string, error) {
			if name == "devcontainer" {
				return "", exec.ErrNotFound
			}
			return "/usr/bin/" + name, nil
		},
		Daemon: func(context.Context) error { return nil },
	}
	r := Check(context.Background(), d)
	if r.OK() {
		t.Fatal("want not OK when devcontainer missing")
	}
	if !strings.Contains(strings.Join(r.Messages(), "\n"), "devcontainers/cli") {
		t.Errorf("messages should hint the install command: %v", r.Messages())
	}
}

func TestCheckDaemonDown(t *testing.T) {
	d := Deps{
		Look:   func(string) (string, error) { return "/usr/bin/x", nil },
		Daemon: func(context.Context) error { return errors.New("cannot connect") },
	}
	if Check(context.Background(), d).DockerDaemon {
		t.Error("DockerDaemon should be false when daemon errors")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/preflight/ -v`
Expected: FAIL — `Deps`, `Check`, `Report` undefined.

- [ ] **Step 3: Write the implementation**

`internal/preflight/preflight.go`:
```go
// Package preflight checks the host prerequisites flotilla needs to spawn
// agents: the docker CLI, a reachable docker daemon, and the devcontainer CLI.
package preflight

import (
	"context"
	"os/exec"
)

// Report is the outcome of the prerequisite checks.
type Report struct {
	Docker       bool
	DockerDaemon bool
	Devcontainer bool
}

// OK reports whether every prerequisite is satisfied.
func (r Report) OK() bool { return r.Docker && r.DockerDaemon && r.Devcontainer }

// Deps are the host seams the checks use (swapped out in tests).
type Deps struct {
	Look   func(string) (string, error)
	Daemon func(context.Context) error
}

// Real wires Deps to the host.
func Real() Deps {
	return Deps{
		Look: exec.LookPath,
		Daemon: func(ctx context.Context) error {
			return exec.CommandContext(ctx, "docker", "info").Run()
		},
	}
}

// Check runs the prerequisite checks.
func Check(ctx context.Context, d Deps) Report {
	var r Report
	if _, err := d.Look("docker"); err == nil {
		r.Docker = true
		if d.Daemon(ctx) == nil {
			r.DockerDaemon = true
		}
	}
	if _, err := d.Look("devcontainer"); err == nil {
		r.Devcontainer = true
	}
	return r
}

// Messages returns one human-readable status line per check.
func (r Report) Messages() []string {
	line := func(ok bool, name, fix string) string {
		if ok {
			return "ok       " + name
		}
		return "MISSING  " + name + " — " + fix
	}
	return []string{
		line(r.Docker, "docker CLI", "install Docker"),
		line(r.DockerDaemon, "docker daemon", "start Docker"),
		line(r.Devcontainer, "devcontainer CLI", "npm i -g @devcontainers/cli"),
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `mise exec -- go test ./internal/preflight/ -v`
Expected: PASS.

- [ ] **Step 5: Wire `doctor` + the spawn gate into the CLI**

In `internal/cli/cli.go`, add imports `"strings"` and `"github.com/mickzijdel/flotilla/internal/preflight"`. Register the new command in `BuildRoot`:
```go
	root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), agentsCmd(), doctorCmd())
```
Add the gate at the top of `spawnCmd`'s `RunE`, before `agent.Builtins()`:
```go
			if rep := preflight.Check(cmd.Context(), preflight.Real()); !rep.OK() {
				return fmt.Errorf("preflight failed (run 'flotilla doctor'): %s", strings.Join(rep.Messages(), "; "))
			}
```
Add the command:
```go
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check prerequisites (docker, docker daemon, devcontainer CLI)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep := preflight.Check(cmd.Context(), preflight.Real())
			for _, m := range rep.Messages() {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), m); err != nil {
					return err
				}
			}
			if !rep.OK() {
				return fmt.Errorf("missing prerequisites")
			}
			return nil
		},
	}
}
```

- [ ] **Step 6: Verify build + existing CLI tests still pass**

Run: `mise exec -- go build ./... && mise exec -- go test ./internal/cli/ ./internal/preflight/ -v`
Expected: PASS (existing `TestListJSONOutput`/`TestAgentsListsBuiltins` don't hit the spawn gate).

- [ ] **Step 7: Commit**

```bash
git add internal/preflight/ internal/cli/cli.go
git commit -m "feat(preflight): docker/devcontainer prerequisite checks + doctor command + spawn gate"
```

---

## Task 3: Vendored toolchain Feature + embed/Extract

**Files:**
- Create: `internal/feature/toolchain/devcontainer-feature.json`
- Create: `internal/feature/toolchain/install.sh`
- Create: `internal/feature/feature.go`
- Create: `internal/feature/feature_test.go`

- [ ] **Step 1: Write the Feature manifest**

`internal/feature/toolchain/devcontainer-feature.json`:
```json
{
  "id": "flotilla-toolchain",
  "version": "0.1.0",
  "name": "Flotilla toolchain",
  "description": "Common tooling for flotilla agents: node, git, gh, mise. Installs no agent CLI and no credentials.",
  "options": {}
}
```

- [ ] **Step 2: Write the Feature install script**

`internal/feature/toolchain/install.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail

# Flotilla toolchain Feature: common tooling ONLY. Installs no agent CLI (that is
# the profile's Install step) and no credentials (those are injected at runtime).
export DEBIAN_FRONTEND=noninteractive

if command -v apt-get >/dev/null 2>&1; then
  apt-get update -y
  apt-get install -y --no-install-recommends ca-certificates curl git gnupg
fi

# Node — needed for npm-based agent CLIs (claude, codex).
if ! command -v node >/dev/null 2>&1; then
  curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
  apt-get install -y --no-install-recommends nodejs
fi

# GitHub CLI — handy in-box for read-only ops (the engine still does all remote git).
if ! command -v gh >/dev/null 2>&1; then
  curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    | gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg
  echo "deb [signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list
  apt-get update -y && apt-get install -y --no-install-recommends gh
fi

# mise — polyglot tool manager (matches the engine's own toolchain story).
if ! command -v mise >/dev/null 2>&1; then
  curl -fsSL https://mise.run | sh
fi

echo "flotilla-toolchain installed"
```

- [ ] **Step 3: Write the failing test**

`internal/feature/feature_test.go`:
```go
package feature

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractWritesFeatureFiles(t *testing.T) {
	dir := t.TempDir()
	path, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	manifest := filepath.Join(path, "devcontainer-feature.json")
	script := filepath.Join(path, "install.sh")
	for _, p := range []string{manifest, script} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
	// install.sh must be executable.
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("install.sh not executable: mode %v", info.Mode())
	}
	// Manifest must parse and carry the expected id.
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest not JSON: %v", err)
	}
	if m.ID != "flotilla-toolchain" {
		t.Errorf("id = %q, want flotilla-toolchain", m.ID)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `mise exec -- go test ./internal/feature/ -v`
Expected: FAIL — `Extract` undefined.

- [ ] **Step 5: Write the implementation**

`internal/feature/feature.go`:
```go
// Package feature embeds the flotilla toolchain Dev Container Feature and
// extracts it to disk so `devcontainer up --additional-features` can reference
// it by absolute path.
package feature

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed toolchain
var toolchainFS embed.FS

// Extract writes the embedded toolchain Feature into destDir/flotilla-toolchain
// and returns its absolute path. install.sh is made executable.
func Extract(destDir string) (string, error) {
	root := filepath.Join(destDir, "flotilla-toolchain")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(toolchainFS, "toolchain")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := toolchainFS.ReadFile("toolchain/" + e.Name())
		if err != nil {
			return "", err
		}
		mode := os.FileMode(0o644)
		if filepath.Ext(e.Name()) == ".sh" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(root, e.Name()), b, mode); err != nil {
			return "", err
		}
	}
	return filepath.Abs(root)
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `mise exec -- go test ./internal/feature/ -v`
Expected: PASS.

- [ ] **Step 7: Commit (preserve the exec bit on install.sh)**

```bash
chmod +x internal/feature/toolchain/install.sh
git add internal/feature/
git update-index --chmod=+x internal/feature/toolchain/install.sh
git commit -m "feat(feature): vendored flotilla-toolchain Dev Container Feature + embed/Extract"
```
(The `exec-bit-scripts` pre-commit hook requires shebang scripts to carry the exec bit in the index.)

---

## Task 4: Backend — Up / ExecDetached / CopyTo across interface, fake, and docker

One cohesive commit so the build stays green (both `Fake` and `dockerBackend` must satisfy the widened interface).

**Files:**
- Modify: `internal/backend/backend.go`
- Modify: `internal/backend/fake.go`
- Modify: `internal/backend/fake_test.go`
- Create: `internal/backend/devcontainer.go`
- Create: `internal/backend/devcontainer_test.go`

- [ ] **Step 1: Write the failing fake test**

Append to `internal/backend/fake_test.go`:
```go
func TestFakeUpRecordsOptsAndRuns(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	id, err := f.Up(ctx, UpOpts{
		Name:               "atlas",
		WorkspaceFolder:    "/work/atlas",
		AdditionalFeatures: map[string]any{"/feat/flotilla-toolchain": map[string]any{}},
		Labels:             map[string]string{LabelAgent: "atlas"},
	})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(f.UpCalls) != 1 || f.UpCalls[0].WorkspaceFolder != "/work/atlas" {
		t.Fatalf("UpCalls = %+v", f.UpCalls)
	}
	got, _ := f.List(ctx, map[string]string{LabelAgent: "atlas"})
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("List = %+v, want one running", got)
	}
}

func TestFakeExecDetachedAndCopyToRecord(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if err := f.ExecDetached(ctx, "fake-1", []string{"sh", "-c", "echo hi"}); err != nil {
		t.Fatalf("ExecDetached: %v", err)
	}
	if len(f.DetachedCalls) != 1 || f.DetachedCalls[0][0] != "fake-1" {
		t.Fatalf("DetachedCalls = %+v", f.DetachedCalls)
	}

	src := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(src, []byte("CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.CopyTo(ctx, "fake-1", src, "/dst/payload"); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if len(f.CopyCalls) != 1 {
		t.Fatalf("CopyCalls = %+v", f.CopyCalls)
	}
	cp := f.CopyCalls[0]
	if cp.DestPath != "/dst/payload" || string(cp.Content) != "CONTENT" {
		t.Errorf("CopyCall = %+v", cp)
	}
}
```
Add imports to `fake_test.go` if absent: `"os"`, `"path/filepath"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/backend/ -run 'TestFakeUp|TestFakeExecDetached' -v`
Expected: FAIL — `Up`, `UpOpts`, `ExecDetached`, `CopyTo`, `CopyCall` undefined.

- [ ] **Step 3: Widen the interface + add UpOpts**

In `internal/backend/backend.go`, add the `UpOpts` type after `CreateOpts`:
```go
// UpOpts describes a devcontainer to provision (build + inject Feature + start,
// idling). It replaces Create+Start for the agent path.
type UpOpts struct {
	Name               string
	WorkspaceFolder    string         // engine clone dir → devcontainer --workspace-folder
	ConfigPath         string         // external default devcontainer.json; "" = auto-discover
	AdditionalFeatures map[string]any // e.g. {"/abs/flotilla-toolchain": {}}
	Labels             map[string]string
}
```
Add three methods to the `Backend` interface (keep the existing ones):
```go
	Up(ctx context.Context, opts UpOpts) (string, error)
	ExecDetached(ctx context.Context, id string, cmd []string) error
	CopyTo(ctx context.Context, id, hostPath, destPath string) error
```

- [ ] **Step 4: Implement on the fake**

In `internal/backend/fake.go`, add `"os"` to imports, add the recording type + fields, and the methods. Add the type above `Fake`:
```go
// CopyCall records a CopyTo for assertions (Content is read from HostPath).
type CopyCall struct {
	ID, HostPath, DestPath string
	Content                []byte
}
```
Extend the `Fake` struct with:
```go
	UpCalls       []UpOpts
	DetachedCalls [][]string
	CopyCalls     []CopyCall
```
Add the methods:
```go
func (f *Fake) Up(_ context.Context, o UpOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UpCalls = append(f.UpCalls, o)
	f.seq++
	id := fmt.Sprintf("fake-%d", f.seq)
	f.items[id] = &Container{
		ID:      id,
		Name:    o.Labels[LabelAgent],
		Repo:    o.Labels[LabelRepo],
		Status:  "running",
		Created: time.Unix(int64(f.seq), 0).UTC(),
		Labels:  o.Labels,
	}
	return id, nil
}

func (f *Fake) ExecDetached(_ context.Context, id string, cmd []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DetachedCalls = append(f.DetachedCalls, append([]string{id}, cmd...))
	return nil
}

func (f *Fake) CopyTo(_ context.Context, id, hostPath, destPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, _ := os.ReadFile(hostPath) // best-effort, for test assertions
	f.CopyCalls = append(f.CopyCalls, CopyCall{ID: id, HostPath: hostPath, DestPath: destPath, Content: content})
	return nil
}
```

- [ ] **Step 5: Implement on the docker backend**

`internal/backend/devcontainer.go`:
```go
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// devcontainer runs the devcontainer CLI and returns combined stdout.
func devcontainer(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "devcontainer", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("devcontainer %s: %w: %s", strings.Join(args, " "), err, errb.String())
	}
	return out.String(), nil
}

// Up provisions the repo's devcontainer (or the supplied default config),
// injecting the additional Features, and returns the container ID.
func (d *dockerBackend) Up(ctx context.Context, o UpOpts) (string, error) {
	args := []string{"up", "--workspace-folder", o.WorkspaceFolder}
	if o.ConfigPath != "" {
		args = append(args, "--config", o.ConfigPath)
	}
	if len(o.AdditionalFeatures) > 0 {
		b, err := json.Marshal(o.AdditionalFeatures)
		if err != nil {
			return "", fmt.Errorf("marshal additional-features: %w", err)
		}
		args = append(args, "--additional-features", string(b))
	}
	for k, v := range o.Labels {
		args = append(args, "--id-label", k+"="+v)
	}
	out, err := devcontainer(ctx, args...)
	if err != nil {
		return "", err
	}
	if id := containerIDFromUp(out); id != "" {
		return id, nil
	}
	// Fallback: resolve by the agent label we just applied.
	return run(ctx, "ps", "-aq", "--no-trunc", "--filter", "label="+LabelAgent+"="+o.Labels[LabelAgent])
}

// containerIDFromUp parses the trailing JSON line devcontainer up emits.
func containerIDFromUp(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r struct {
			ContainerID string `json:"containerId"`
		}
		if err := json.Unmarshal([]byte(line), &r); err == nil && r.ContainerID != "" {
			return r.ContainerID
		}
	}
	return ""
}

// ExecDetached runs cmd in the container without waiting (the backgrounded launch).
func (d *dockerBackend) ExecDetached(ctx context.Context, id string, cmd []string) error {
	_, err := run(ctx, append([]string{"exec", "-d", id}, cmd...)...)
	return err
}

// CopyTo copies a host file/dir into the container (no contents on argv).
func (d *dockerBackend) CopyTo(ctx context.Context, id, hostPath, destPath string) error {
	_, err := run(ctx, "cp", hostPath, id+":"+destPath)
	return err
}
```

> **Orchestrator note:** if Task 1 Step 3 found that an **external** `--config` is rejected, change the `o.ConfigPath != ""` branch to `--override-config`, and the Task 8 Spawn will still pass the same scratch path.

- [ ] **Step 6: Write the docker integration test (skips without deps)**

`internal/backend/devcontainer_test.go`:
```go
package backend

import (
	"context"
	"os/exec"
	"testing"
)

func devcontainerAvailable() bool {
	if !dockerAvailable() {
		return false
	}
	_, err := exec.LookPath("devcontainer")
	return err == nil
}

func TestContainerIDFromUpParsesTrailingJSON(t *testing.T) {
	out := "Building...\nsome log line\n{\"outcome\":\"success\",\"containerId\":\"abc123\",\"remoteUser\":\"root\"}\n"
	if got := containerIDFromUp(out); got != "abc123" {
		t.Errorf("containerIDFromUp = %q, want abc123", got)
	}
	if got := containerIDFromUp("no json here"); got != "" {
		t.Errorf("containerIDFromUp = %q, want empty", got)
	}
}

func TestDockerCopyToIntegration(t *testing.T) {
	if !devcontainerAvailable() {
		t.Skip("docker+devcontainer not available; skipping integration test")
	}
	ctx := context.Background()
	d := NewDocker()
	id, err := d.Create(ctx, CreateOpts{
		Name:   "flotilla-copyto-test",
		Image:  "alpine:3.20",
		Cmd:    []string{"sleep", "60"},
		Labels: map[string]string{LabelAgent: "copyto-test"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer d.Remove(ctx, id) //nolint:errcheck
	if err := d.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	src := t.TempDir() + "/payload"
	if err := writeFile(src, "hi"); err != nil {
		t.Fatal(err)
	}
	if err := d.Exec(ctx, id, []string{"mkdir", "-p", "/run/flotilla"}); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := d.CopyTo(ctx, id, src, "/run/flotilla/payload"); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := d.Exec(ctx, id, []string{"test", "-f", "/run/flotilla/payload"}); err != nil {
		t.Errorf("copied file missing in container: %v", err)
	}
}

func writeFile(path, content string) error {
	return osWriteFile(path, content)
}
```
Add a tiny helper at the bottom of the same file to keep imports minimal:
```go
import "os"

func osWriteFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }
```
(Place the `import "os"` with the other imports; merge into the import block.)

- [ ] **Step 7: Run tests + build**

Run: `mise exec -- go build ./... && mise exec -- go test ./internal/backend/ -v`
Expected: PASS (the `*Integration` test SKIPs without docker+devcontainer; `containerIDFromUp` + fake tests run).

- [ ] **Step 8: Commit**

```bash
git add internal/backend/
git commit -m "feat(backend): Up/ExecDetached/CopyTo for devcontainer provisioning + injection"
```

---

## Task 5: Setup handlers (registry + declarative + builtin:claude/codex)

**Files:**
- Create: `internal/setup/setup.go`
- Create: `internal/setup/home.go`
- Create: `internal/setup/setup_test.go`

- [ ] **Step 1: Write the failing test**

`internal/setup/setup_test.go`:
```go
package setup

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
)

// recInjector records what a handler does.
type recInjector struct {
	execs  [][]string
	copies [][2]string // {hostPath, destPath}
	writes map[string]string
}

func newRec() *recInjector { return &recInjector{writes: map[string]string{}} }

func (r *recInjector) Exec(_ context.Context, cmd []string) error {
	r.execs = append(r.execs, cmd)
	return nil
}
func (r *recInjector) CopyTo(_ context.Context, hostPath, destPath string) error {
	r.copies = append(r.copies, [2]string{hostPath, destPath})
	return nil
}
func (r *recInjector) WriteFile(_ context.Context, content []byte, destPath string) error {
	r.writes[destPath] = string(content)
	return nil
}

func TestClaudeSetupWritesSettingsAndMakesDir(t *testing.T) {
	r := newRec()
	if err := Run(context.Background(), r, agent.Profile{Setup: "builtin:claude"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := r.writes["/root/.claude/settings.json"]; !ok {
		t.Errorf("expected settings.json write, writes=%v", r.writes)
	}
	foundMkdir := false
	for _, c := range r.execs {
		if len(c) >= 3 && c[0] == "mkdir" && c[2] == "/root/.claude" {
			foundMkdir = true
		}
	}
	if !foundMkdir {
		t.Errorf("expected mkdir -p /root/.claude, execs=%v", r.execs)
	}
}

func TestDeclarativeCopiesConfigMounts(t *testing.T) {
	r := newRec()
	prof := agent.Profile{Setup: "declarative", ConfigMounts: []string{"/etc/hostcfg:/root/.cfg"}}
	if err := Run(context.Background(), r, prof); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(r.copies) != 1 || r.copies[0] != [2]string{"/etc/hostcfg", "/root/.cfg"} {
		t.Errorf("copies = %v", r.copies)
	}
}

func TestUnknownSetupHandlerErrors(t *testing.T) {
	err := Run(context.Background(), newRec(), agent.Profile{Setup: "builtin:nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown setup handler") {
		t.Errorf("want unknown-handler error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/setup/ -v`
Expected: FAIL — `Run`, `Injector` undefined.

- [ ] **Step 3: Write the implementation**

`internal/setup/setup.go`:
```go
// Package setup assembles an agent's in-container config home. Built-in handlers
// do smart assembly for first-class agents; declarative uses config_mounts only.
// Handlers never inject secrets — the agent token arrives via the env-file (fleet).
package setup

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mickzijdel/flotilla/internal/agent"
)

// Injector is how a handler assembles config inside the container. WriteFile and
// CopyTo route file content through `docker cp` (never via argv).
type Injector interface {
	Exec(ctx context.Context, cmd []string) error
	CopyTo(ctx context.Context, hostPath, destPath string) error
	WriteFile(ctx context.Context, content []byte, destPath string) error
}

// Handler assembles a specific agent's config home.
type Handler func(ctx context.Context, inj Injector, prof agent.Profile) error

var registry = map[string]Handler{
	"builtin:claude": claudeSetup,
	"builtin:codex":  codexSetup,
}

// Run dispatches to the profile's setup handler. "" or "declarative" copies
// config_mounts only.
func Run(ctx context.Context, inj Injector, prof agent.Profile) error {
	switch prof.Setup {
	case "", "declarative":
		return declarative(ctx, inj, prof)
	default:
		h, ok := registry[prof.Setup]
		if !ok {
			return fmt.Errorf("unknown setup handler %q", prof.Setup)
		}
		return h(ctx, inj, prof)
	}
}

func declarative(ctx context.Context, inj Injector, prof agent.Profile) error {
	for _, m := range prof.ConfigMounts {
		host, dest, ok := strings.Cut(m, ":")
		if !ok {
			return fmt.Errorf("invalid config_mount %q (want host:dest)", m)
		}
		if err := inj.CopyTo(ctx, expandHome(host), dest); err != nil {
			return err
		}
	}
	return nil
}

func claudeSetup(ctx context.Context, inj Injector, _ agent.Profile) error {
	const home = "/root/.claude"
	if err := inj.Exec(ctx, []string{"mkdir", "-p", home}); err != nil {
		return err
	}
	// Minimal, container-safe settings; auth is the headless token (injected by fleet).
	// Task 1's spike may extend this with keys headless Claude needs.
	if err := inj.WriteFile(ctx, []byte("{}\n"), filepath.Join(home, "settings.json")); err != nil {
		return err
	}
	// Carry the global CLAUDE.md if the host has one.
	if md := expandHome("~/.claude/CLAUDE.md"); fileExists(md) {
		if err := inj.CopyTo(ctx, md, filepath.Join(home, "CLAUDE.md")); err != nil {
			return err
		}
	}
	return nil
}

func codexSetup(ctx context.Context, inj Injector, _ agent.Profile) error {
	const home = "/root/.codex"
	if err := inj.Exec(ctx, []string{"mkdir", "-p", home}); err != nil {
		return err
	}
	if err := inj.WriteFile(ctx, []byte("# flotilla-managed codex config\n"), filepath.Join(home, "config.toml")); err != nil {
		return err
	}
	// Carry an existing OAuth auth.json if present; otherwise OPENAI_API_KEY (env) is used.
	if auth := expandHome("~/.codex/auth.json"); fileExists(auth) {
		if err := inj.CopyTo(ctx, auth, filepath.Join(home, "auth.json")); err != nil {
			return err
		}
	}
	return nil
}
```
`internal/setup/home.go`:
```go
package setup

import (
	"os"
	"path/filepath"
	"strings"
)

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `mise exec -- go test ./internal/setup/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/setup/
git commit -m "feat(setup): handler registry + builtin:claude/codex + declarative config_mounts"
```

---

## Task 6: Fleet spawn helpers (pure)

**Files:**
- Create: `internal/fleet/spawn_helpers.go`
- Create: `internal/fleet/spawn_helpers_test.go`

- [ ] **Step 1: Write the failing test**

`internal/fleet/spawn_helpers_test.go`:
```go
package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEnvOnlyPresentKeys(t *testing.T) {
	look := func(k string) (string, bool) {
		if k == "PRESENT" {
			return "v", true
		}
		return "", false
	}
	got := resolveEnv([]string{"PRESENT", "ABSENT"}, look)
	if len(got) != 1 || got["PRESENT"] != "v" {
		t.Errorf("resolveEnv = %v, want {PRESENT:v}", got)
	}
}

func TestEnvFileContentSortedKV(t *testing.T) {
	b := envFileContent(map[string]string{"B": "2", "A": "1"})
	if string(b) != "A=1\nB=2\n" {
		t.Errorf("envFileContent = %q", b)
	}
}

func TestLaunchWrapperSourcesEnvFileThenExecs(t *testing.T) {
	got := launchWrapper(`claude -p "hi"`)
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("launchWrapper shape = %v", got)
	}
	if !strings.Contains(got[2], agentEnvFile) || !strings.Contains(got[2], `exec claude -p "hi"`) {
		t.Errorf("launchWrapper script = %q", got[2])
	}
}

func TestDefaultDevcontainerJSONIsValidWithImage(t *testing.T) {
	b := defaultDevcontainerJSON("ubuntu:24.04")
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	if m["image"] != "ubuntu:24.04" {
		t.Errorf("image = %v", m["image"])
	}
}

func TestHasDevcontainerDetectsConfig(t *testing.T) {
	dir := t.TempDir()
	if hasDevcontainer(dir) {
		t.Fatal("empty dir should have no devcontainer")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasDevcontainer(dir) {
		t.Error("should detect .devcontainer/devcontainer.json")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/fleet/ -run 'TestResolveEnv|TestEnvFile|TestLaunchWrapper|TestDefaultDevcontainer|TestHasDevcontainer' -v`
Expected: FAIL — helpers undefined.

- [ ] **Step 3: Write the implementation**

`internal/fleet/spawn_helpers.go`:
```go
package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// agentEnvFile is where the injected secret env-file lands in the container.
const agentEnvFile = "/run/flotilla/agent.env"

// resolveEnv returns the subset of allowlisted keys present in the environment.
// Only named keys can enter the container — the allowlist is the boundary.
func resolveEnv(keys []string, look func(string) (string, bool)) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := look(k); ok {
			out[k] = v
		}
	}
	return out
}

// envFileContent renders KEY=VALUE lines, sorted for determinism.
func envFileContent(env map[string]string) []byte {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return []byte(b.String())
}

// launchWrapper sources the injected env-file, then execs the agent launch.
func launchWrapper(launch string) []string {
	script := fmt.Sprintf("set -a; . %s 2>/dev/null; set +a; exec %s", agentEnvFile, launch)
	return []string{"sh", "-c", script}
}

// defaultDevcontainerJSON is the bundled config used when a repo ships none.
func defaultDevcontainerJSON(baseImage string) []byte {
	return []byte(fmt.Sprintf("{\n  \"name\": \"flotilla-default\",\n  \"image\": %q,\n  \"overrideCommand\": true\n}\n", baseImage))
}

// hasDevcontainer reports whether dir already ships a devcontainer config.
func hasDevcontainer(dir string) bool {
	for _, p := range []string{
		filepath.Join(dir, ".devcontainer", "devcontainer.json"),
		filepath.Join(dir, ".devcontainer.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `mise exec -- go test ./internal/fleet/ -run 'TestResolveEnv|TestEnvFile|TestLaunchWrapper|TestDefaultDevcontainer|TestHasDevcontainer' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/spawn_helpers.go internal/fleet/spawn_helpers_test.go
git commit -m "feat(fleet): spawn helpers — env allowlist, env-file, launch wrapper, default devcontainer"
```

---

## Task 7: Profile updates (Claude OAuth token + npm install)

**Files:**
- Modify: `internal/agent/builtin/claude.toml`
- Modify: `internal/agent/builtin/codex.toml`
- Modify: `internal/agent/profile_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/profile_test.go`:
```go
func TestClaudeBuiltinUsesOAuthTokenAndNpmInstall(t *testing.T) {
	got, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	c := got["claude"]
	if len(c.Env) != 1 || c.Env[0] != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("claude Env = %v, want [CLAUDE_CODE_OAUTH_TOKEN]", c.Env)
	}
	if c.Install == "" {
		t.Errorf("claude Install is empty; want an npm install command")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/agent/ -run TestClaudeBuiltin -v`
Expected: FAIL — current claude env is `ANTHROPIC_API_KEY`, install is `""`.

- [ ] **Step 3: Update the profiles**

`internal/agent/builtin/claude.toml` (whole file):
```toml
name = "claude"
install = "npm i -g @anthropic-ai/claude-code"
launch = 'claude --dangerously-skip-permissions -p "{prompt}"'
setup = "builtin:claude"
config_mounts = []
env = ["CLAUDE_CODE_OAUTH_TOKEN"]
transcript_path = "~/.claude/projects"
egress_allow = ["api.anthropic.com"]
done_signal = "process-exit"
```
`internal/agent/builtin/codex.toml` (whole file — the `builtin:codex` handler owns `~/.codex`, so drop the now-redundant config_mount):
```toml
name = "codex"
install = "npm i -g @openai/codex"
launch = 'codex exec --dangerously-bypass-approvals-and-sandbox "{prompt}"'
setup = "builtin:codex"
config_mounts = []
env = ["OPENAI_API_KEY"]
transcript_path = "~/.codex/sessions"
egress_allow = ["api.openai.com"]
done_signal = "process-exit"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `mise exec -- go test ./internal/agent/ -v`
Expected: PASS (all profile tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/builtin/claude.toml internal/agent/builtin/codex.toml internal/agent/profile_test.go
git commit -m "feat(agent): claude uses CLAUDE_CODE_OAUTH_TOKEN + npm install; codex config via handler"
```

---

## Task 8: Rewire Fleet.Spawn to the devcontainer flow

**Files:**
- Create: `internal/fleet/injector.go`
- Modify: `internal/fleet/fleet.go`
- Modify: `internal/fleet/fleet_test.go`

- [ ] **Step 1: Write the injector adapter**

`internal/fleet/injector.go`:
```go
package fleet

import (
	"context"
	"os"
	"path"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// injector adapts a Backend + container id to setup.Injector. File content is
// routed through the backend's CopyTo (`docker cp`), never via argv, and the
// destination's parent dir is created first.
type injector struct {
	be backend.Backend
	id string
}

func (j *injector) Exec(ctx context.Context, cmd []string) error {
	return j.be.Exec(ctx, j.id, cmd)
}

func (j *injector) CopyTo(ctx context.Context, hostPath, destPath string) error {
	if err := j.mkdirParent(ctx, destPath); err != nil {
		return err
	}
	return j.be.CopyTo(ctx, j.id, hostPath, destPath)
}

// WriteFile writes generated content to a 0600 host temp file and copies it in.
func (j *injector) WriteFile(ctx context.Context, content []byte, destPath string) error {
	tmp, err := os.CreateTemp("", "flotilla-inject-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := j.mkdirParent(ctx, destPath); err != nil {
		return err
	}
	return j.be.CopyTo(ctx, j.id, tmp.Name(), destPath)
}

func (j *injector) mkdirParent(ctx context.Context, destPath string) error {
	dir := path.Dir(destPath)
	if dir == "" || dir == "." || dir == "/" {
		return nil
	}
	return j.be.Exec(ctx, j.id, []string{"mkdir", "-p", dir})
}
```

- [ ] **Step 2: Rewire Spawn**

Replace the `Spawn` function in `internal/fleet/fleet.go` with:
```go
// Spawn clones repoURL engine-side, provisions a devcontainer with the toolchain
// Feature, injects the agent's token + config, installs the agent CLI, and
// launches it. Git credentials never enter the container.
func (f *Fleet) Spawn(ctx context.Context, repoURL string, prof agent.Profile, prompt string) (Agent, error) {
	existing, err := f.List(ctx)
	if err != nil {
		return Agent{}, err
	}
	taken := map[string]bool{}
	for _, a := range existing {
		taken[a.Name] = true
	}
	name := naming.Pick(taken)

	dest := filepath.Join(f.workRoot(), name)
	if err := gitops.Clone(ctx, repoURL, dest); err != nil {
		return Agent{}, err
	}

	// Scratch holds the extracted Feature + (optional) default config; consumed
	// by `devcontainer up`'s build, then removed. Kept out of the agent's workspace.
	scratch, err := os.MkdirTemp("", "flotilla-"+name+"-")
	if err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	featPath, err := feature.Extract(scratch)
	if err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("extract feature: %w", err)
	}

	configPath := ""
	if !hasDevcontainer(dest) {
		configPath = filepath.Join(scratch, "devcontainer.json")
		if err := os.WriteFile(configPath, defaultDevcontainerJSON(f.BaseImage), 0o644); err != nil {
			_ = os.RemoveAll(dest)
			return Agent{}, fmt.Errorf("write default devcontainer: %w", err)
		}
	}

	id, err := f.Backend.Up(ctx, backend.UpOpts{
		Name:               name,
		WorkspaceFolder:    dest,
		ConfigPath:         configPath,
		AdditionalFeatures: map[string]any{featPath: map[string]any{}},
		Labels: map[string]string{
			backend.LabelAgent:   name,
			backend.LabelRepo:    repoURL,
			backend.LabelCreated: time.Now().UTC().Format(time.RFC3339),
			backend.LabelHost:    "local",
		},
	})
	if err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("provision container: %w", err)
	}

	inj := &injector{be: f.Backend, id: id}

	// 1) Secrets: resolved allowlist → 0600 env-file → container (no git creds).
	env := resolveEnv(prof.Env, os.LookupEnv)
	if err := inj.WriteFile(ctx, envFileContent(env), agentEnvFile); err != nil {
		return Agent{}, fmt.Errorf("inject secrets: %w", err)
	}
	// 2) Config: setup handler / declarative config_mounts.
	if err := setup.Run(ctx, inj, prof); err != nil {
		return Agent{}, fmt.Errorf("setup: %w", err)
	}
	// 3) Install the agent CLI.
	if strings.TrimSpace(prof.Install) != "" {
		if err := f.Backend.Exec(ctx, id, []string{"sh", "-c", prof.Install}); err != nil {
			return Agent{}, fmt.Errorf("install agent: %w", err)
		}
	}
	// 4) Launch the agent, backgrounded (exec-into-idle).
	if err := f.Backend.ExecDetached(ctx, id, launchWrapper(prof.RenderLaunch(prompt))); err != nil {
		return Agent{}, fmt.Errorf("launch agent: %w", err)
	}

	return Agent{Name: name, Repo: repoURL, Status: "running", Created: time.Now().UTC(), ID: id}, nil
}
```
Update the import block of `internal/fleet/fleet.go` to add `"strings"`, `"github.com/mickzijdel/flotilla/internal/feature"`, and `"github.com/mickzijdel/flotilla/internal/setup"` (keep `context`, `fmt`, `os`, `path/filepath`, `time`, `agent`, `backend`, `gitops`, `naming`).

- [ ] **Step 3: Update the failure-path test (Create → Up)**

In `internal/fleet/fleet_test.go`, replace the `failCreateBackend` type and its method with one that fails `Up` (Spawn no longer calls `Create`):
```go
// failUpBackend wraps a Fake but always errors from Up, to exercise Spawn's
// clone-cleanup-on-failure path.
type failUpBackend struct{ *backend.Fake }

func (failUpBackend) Up(context.Context, backend.UpOpts) (string, error) {
	return "", errors.New("boom")
}
```
And in `TestSpawnCleansUpCloneOnBackendFailure`, change the backend construction:
```go
	be := failUpBackend{backend.NewFake()}
```
(The rest of that test is unchanged — it still asserts `WorkRoot` is empty after the failed spawn.)

- [ ] **Step 4: Run the fleet package + build**

Run: `mise exec -- go build ./... && mise exec -- go test ./internal/fleet/ -v`
Expected: PASS — `TestSpawnClonesAndCreatesContainer` (now goes through `Up`), `TestSpawnCleansUpCloneOnBackendFailure` (via `failUpBackend`), and the helper tests.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/injector.go internal/fleet/fleet.go internal/fleet/fleet_test.go
git commit -m "feat(fleet): Spawn provisions devcontainer + injects token/config + launches agent"
```

---

## Task 9: Credential-isolation test (the marquee invariant)

**Files:**
- Create: `internal/fleet/credisolation_test.go`

- [ ] **Step 1: Write the test**

`internal/fleet/credisolation_test.go`:
```go
package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

// gitCredMarkers must never appear in anything Spawn sends into the container
// (env values/keys, exec args, mount/copy paths). The agent's own token may
// enter — only git/GitHub credentials are forbidden.
var gitCredMarkers = []string{
	"github_token", "gh_token", "github_pat", "git_askpass",
	".git-credentials", "/.config/gh", "/.ssh", "/.gitconfig",
	"credential.helper",
}

func TestSpawnInjectsNoGitCredentials(t *testing.T) {
	fake := backend.NewFake()
	builtins, err := agent.Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	if _, err := f.Spawn(context.Background(), bareRepo(t), builtins["claude"], "do it"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Collect every string Spawn handed the backend.
	var blobs []string
	for _, up := range fake.UpCalls {
		blobs = append(blobs, up.WorkspaceFolder, up.ConfigPath)
		for k, v := range up.Labels {
			blobs = append(blobs, k, v)
		}
		for feat := range up.AdditionalFeatures {
			blobs = append(blobs, feat)
		}
	}
	for _, call := range fake.ExecCalls {
		blobs = append(blobs, call...)
	}
	for _, call := range fake.DetachedCalls {
		blobs = append(blobs, call...)
	}
	for _, cp := range fake.CopyCalls {
		blobs = append(blobs, cp.HostPath, cp.DestPath)
		// Only the secret env-file's content is scanned; copied user config
		// (e.g. a global CLAUDE.md) is non-secret and may mention "credential".
		if cp.DestPath == agentEnvFile {
			blobs = append(blobs, string(cp.Content))
		}
	}

	hay := strings.ToLower(strings.Join(blobs, "\n"))
	for _, m := range gitCredMarkers {
		if strings.Contains(hay, m) {
			t.Errorf("git credential marker %q leaked into container inputs", m)
		}
	}

	// Positive control: the only host path that enters is the engine clone.
	if len(fake.UpCalls) == 0 {
		t.Fatal("expected an Up call")
	}
	for _, up := range fake.UpCalls {
		if !strings.HasPrefix(up.WorkspaceFolder, f.WorkRoot) {
			t.Errorf("WorkspaceFolder %q is not under the engine work root %q", up.WorkspaceFolder, f.WorkRoot)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `mise exec -- go test ./internal/fleet/ -run TestSpawnInjectsNoGitCredentials -v`
Expected: PASS — the claude profile injects only `CLAUDE_CODE_OAUTH_TOKEN` (absent in test env → empty env-file), config files, and the workspace clone; no git creds.

- [ ] **Step 3: Sanity-check the test actually guards (temporary mutation)**

Temporarily add a git credential to confirm the test fails, then revert. In `internal/fleet/fleet.go` `Spawn`, just before the `setup.Run` call, add: `_ = f.Backend.Exec(ctx, id, []string{"sh", "-c", "echo GITHUB_TOKEN=x"})`. Run the test; expect FAIL on the `github_token` marker. **Revert the line.** Re-run; expect PASS. (Do not commit the mutation.)

Run: `mise exec -- go test ./internal/fleet/ -run TestSpawnInjectsNoGitCredentials -v`
Expected: PASS after revert.

- [ ] **Step 4: Commit**

```bash
git add internal/fleet/credisolation_test.go
git commit -m "test(fleet): pin credential-isolation invariant — no git creds enter the container"
```

---

## Task 10: Docs — backlog + README status

**Files:**
- Modify: `docs/backlog.md`
- Modify: `README.md`

- [ ] **Step 1: Update the backlog**

In `docs/backlog.md`:
1. Under "## Next plans", replace item 1 (the `devcontainer.json + Feature overlay…` entry) with a one-line note that it is **done**, pointing at this plan + spec, and renumber the remaining items so "Egress firewall" becomes #1.
2. Under "## Known issues / robustness", delete the "**`Env`/`Install`/`config_mounts` declared but not wired**" bullet (resolved by this plan).
3. Under "## Test-coverage gaps to close as features land", delete the "No test pins the credential-isolation invariant…" bullet (now covered by `TestSpawnInjectsNoGitCredentials`).
4. Under "## Known issues / robustness", tighten the "**README oversells \"functional.\"**" bullet: agents are now runnable, so the caveat changes — note that the lifecycle **and** a runnable agent (devcontainer + injection) work, with egress firewall + submission still pending.

- [ ] **Step 2: Update the README status**

In `README.md`, in the `## Status` section, replace the walking-skeleton-only wording with a sentence noting that a spawned agent is now actually runnable: the engine provisions the repo's devcontainer with a vendored toolchain Feature, injects the agent token (via a `0600` env-file) and config, and launches the agent — with git credentials never entering the container. Note that the egress firewall and submission/PR flow are still pending (next plans).

- [ ] **Step 3: Verify the full suite is green**

Run: `mise exec -- go build ./... && mise exec -- go test ./...`
Expected: PASS across packages (integration tests SKIP without docker+devcontainer).

- [ ] **Step 4: Commit**

```bash
git add docs/backlog.md README.md
git commit -m "docs: mark devcontainer/injection plan done; tighten status and known issues"
```

---

## Task 11: End-to-end smoke (manual; requires docker + devcontainer + a token)

**Files:** none (manual verification — satisfies the "Always Works" bar: the agent actually runs and carries no git creds).

- [ ] **Step 1: Build + doctor**

```bash
mise exec -- go build -o bin/flotilla .
./bin/flotilla doctor    # if devcontainer MISSING: npm i -g @devcontainers/cli
```
Expected: all three checks `ok`.

- [ ] **Step 2: Provide the Claude token (OAuth, headless)**

```bash
# one-time on the host if not already minted:
#   claude setup-token   → store in fnox, then:
export CLAUDE_CODE_OAUTH_TOKEN="$(fnox get CLAUDE_CODE_OAUTH_TOKEN 2>/dev/null || echo "$CLAUDE_CODE_OAUTH_TOKEN")"
test -n "$CLAUDE_CODE_OAUTH_TOKEN" && echo "token present" || echo "NO TOKEN — set it first"
```

- [ ] **Step 3: Spawn a Claude agent on a tiny public repo**

```bash
./bin/flotilla spawn https://github.com/octocat/Hello-World.git \
  --agent claude --prompt "create a file HELLO.txt containing the word hi, then stop"
./bin/flotilla list
```
Expected: `spawn` prints `name status id`; `list` shows the agent `running`.

- [ ] **Step 4: Verify the credential-isolation invariant in the REAL container**

```bash
NAME=$(./bin/flotilla list --json | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["name"])')
CID=$(docker ps -q --filter "label=flotilla.agent=$NAME")
# No git credentials in the container env:
docker exec "$CID" sh -c 'env | grep -iE "GITHUB|GH_TOKEN|GIT_ASKPASS|GIT_" || echo NO_GIT_ENV'
# No git credential files:
docker exec "$CID" sh -c 'ls -la ~/.git-credentials ~/.ssh ~/.config/gh 2>/dev/null || echo NO_GIT_CRED_FILES'
# The agent token IS present, as a 0600 env-file:
docker exec "$CID" sh -c 'test -f /run/flotilla/agent.env && echo "TOKEN_FILE $(stat -c %a /run/flotilla/agent.env)"'
```
Expected: `NO_GIT_ENV`, `NO_GIT_CRED_FILES`, and `TOKEN_FILE 600`.

- [ ] **Step 5: Confirm the agent actually ran**

```bash
# Claude was installed by the profile and launched; check the workspace for its work:
docker exec "$CID" sh -c 'ls -la /workspaces/*/ 2>/dev/null | head; cat /workspaces/*/HELLO.txt 2>/dev/null || echo "no HELLO.txt yet"'
```
Expected: the agent CLI is present and (token permitting) produced `HELLO.txt`. If the agent is still working, re-check after a moment. Record the observed result.

- [ ] **Step 6: Clean up**

```bash
./bin/flotilla stop "$NAME"
./bin/flotilla rm "$NAME"
./bin/flotilla list   # empty
rm -f bin/flotilla
```
Expected: after `rm`, `list` is empty.

- [ ] **Step 7: (No commit)** — record smoke results in the PR/commit description or the spike note.

---

## Self-Review (completed during authoring)

- **Spec coverage:** §3 run model → Task 8 (exec-into-idle Spawn). §4 backend seam → Task 4 (`Up`/`ExecDetached`/`CopyTo` + `UpOpts`). §5 devcontainer.json handling → Task 6 (`hasDevcontainer`, `defaultDevcontainerJSON`) + Task 8 (auto-discover vs `--config`). §6 vendored Feature → Task 3. §7 injection (`docker cp` env-file + config) → Task 5 (handlers) + Task 6 (env-file) + Task 8 (injector wiring). §8 setup handlers → Task 5. §9 cred-isolation invariant + test → Task 9 (+ real-container check in Task 11). §10 preflight + `doctor` → Task 2. §11 out-of-scope items untouched. §12 testing strategy → unit (Tasks 2/3/5/6/9) + integration (Task 4) + manual (Tasks 1/11). §13 spike → Task 1.
- **Placeholder scan:** none — every code step has complete code; the only "may extend later" is `claudeSetup`'s `{}` settings, which ships a valid default and is explicitly refined by Task 1's spike (not a blocking placeholder).
- **Type consistency:** `UpOpts`, `CopyCall`, `Backend` methods, `setup.Injector`/`Handler`/`Run`, `feature.Extract`, `preflight.{Report,Deps,Check,Real}`, and the fleet helpers (`resolveEnv`/`envFileContent`/`launchWrapper`/`defaultDevcontainerJSON`/`hasDevcontainer`/`agentEnvFile`) are defined once and referenced with identical names/signatures across Tasks 4–9. `Spawn` switches from `Create`+`Start` to `Up`; the failure test moves from `failCreateBackend` to `failUpBackend` accordingly (Task 8).
- **Build-green ordering:** the interface widens and both implementers (`Fake`, `dockerBackend`) gain the methods in the same commit (Task 4), so `go build ./...` stays green after every task.
```
