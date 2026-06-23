# Flotilla Logs & Transcript Mounts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist each agent session's logs to a host dir under `~/.flotilla/logs/` — a live transcript bind-mount, a teed `container.log`, and a daemon-free `status` file — and add `flotilla logs <agent> [-f]`.

**Architecture:** All host-side, around the existing `Fleet.Spawn` flow. New backend seams (`UpOpts.Mounts`, `ReadConfig`, `CopyFrom`, `LabelLogDir`) let the engine resolve the run user's home *before* `devcontainer up` (so the live transcript mount's container-side path is absolute), mount the session dir at a fixed `/flotilla/session`, and have the launch wrapper write `running`/`container.log`/`done`. When the remote user can't be resolved pre-up, the live mount is skipped and the transcript is `docker cp`'d out lazily by the `Fleet.Logs` accessor after the agent exits.

**Tech Stack:** Go 1.26, cobra, `os/exec` shelling to `docker`/`devcontainer`, the in-memory `backend.Fake` for tests.

**Design spec:** [docs/specs/2026-06-23-flotilla-logs-transcript-mounts-design.md](../specs/2026-06-23-flotilla-logs-transcript-mounts-design.md)

## Global Constraints

- **Go:** 1.26.4 (`go` directive in go.mod). cobra v1.10.2, BurntSushi/toml v1.6.0.
- **Container invocation:** `os/exec` only (`docker`, `devcontainer`), matching `internal/backend/docker.go` / `devcontainer.go`.
- **Fixed session mount target** is the constant `/flotilla/session` (user-agnostic; needs no home resolution).
- **Live transcript mount** target is resolved *before* `up` via `devcontainer read-configuration`; if `remoteUser` is empty/unresolved or the profile's `transcript_path` is empty, skip the mount (copy-out fallback instead). The fallback NEVER fails the spawn.
- **Logs persist:** `flotilla rm` and failed spawns leave the host log dir in place.
- **Writable mounts** are created `0777` on the host and `chown -R`'d to the run user after `up` (best-effort).
- **No secret values in committed files**; tests use throwaway local repos in `t.TempDir()`.
- **Test style:** real-git tests build a local bare "remote" (see `internal/fleet/fleet_test.go`'s `bareRepo` helper); fakeable seams use `backend.Fake`. Live `devcontainer`/`docker` paths self-skip when the tool is absent.
- **Commit after each task** (TDD: failing test → implementation → green → commit).

---

### Task 1: Backend seams — `UpOpts.Mounts`, `ReadConfig`, `CopyFrom`, `LabelLogDir`

**Files:**
- Modify: `internal/backend/backend.go` (add `Mounts` field, `ConfigInfo` type, `LabelLogDir`, two interface methods)
- Modify: `internal/backend/devcontainer.go` (render `--mount`, implement `ReadConfig`, add pure helpers)
- Modify: `internal/backend/docker.go` (implement `CopyFrom`)
- Modify: `internal/backend/fake.go` (record/return for `ReadConfig`/`CopyFrom`)
- Modify: `internal/backend/devcontainer_test.go` (unit tests for the two pure helpers)

**Interfaces:**
- Produces:
  - `UpOpts.Mounts []Mount` (rendered as repeatable `devcontainer up --mount type=bind,source=,target=`)
  - `const LabelLogDir = "flotilla.logdir"`
  - `type ConfigInfo struct { RemoteUser string }`
  - `ReadConfig(ctx context.Context, workspaceFolder string) (ConfigInfo, error)` (Backend method)
  - `CopyFrom(ctx context.Context, id, srcPath, hostPath string) error` (Backend method)
  - `func upArgs(o UpOpts) ([]string, error)` (unexported; used by `Up`)
  - `func remoteUserFromConfig(out string) string` (unexported)
  - `Fake.ReadConfigResult ConfigInfo`, `Fake.ReadConfigErr error`, `Fake.CopyFromCalls []CopyCall`

- [ ] **Step 1: Write the failing tests** (append to `internal/backend/devcontainer_test.go`)

```go
func TestUpArgsRendersBindMount(t *testing.T) {
	args, err := upArgs(UpOpts{
		WorkspaceFolder: "/work",
		Mounts:          []Mount{{Source: "/host/sess", Target: "/flotilla/session"}},
	})
	if err != nil {
		t.Fatalf("upArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "up --workspace-folder /work") {
		t.Errorf("missing up/workspace in %q", joined)
	}
	if !strings.Contains(joined, "--mount type=bind,source=/host/sess,target=/flotilla/session") {
		t.Errorf("missing --mount in %q", joined)
	}
}

func TestRemoteUserFromMergedConfig(t *testing.T) {
	out := "build log line\n{\"mergedConfiguration\":{\"remoteUser\":\"ubuntu\"}}\n"
	if got := remoteUserFromConfig(out); got != "ubuntu" {
		t.Errorf("remoteUserFromConfig = %q, want ubuntu", got)
	}
	if got := remoteUserFromConfig("no json here"); got != "" {
		t.Errorf("remoteUserFromConfig(no json) = %q, want empty", got)
	}
}
```

> `internal/backend/devcontainer_test.go` is `package backend` and already imports `testing`. Add `"strings"` to its import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/ -run "TestUpArgs|TestRemoteUser" -v`
Expected: FAIL — `undefined: upArgs` / `undefined: remoteUserFromConfig`.

- [ ] **Step 3: Edit `internal/backend/backend.go`**

Add a label constant in the `const (...)` block (after `LabelProxy`):

```go
	// LabelLogDir records the host session-log dir for an agent so `flotilla
	// logs` can find container.log without date math.
	LabelLogDir = "flotilla.logdir"
```

Add the `Mounts` field to `UpOpts` (after `AdditionalFeatures`):

```go
	Mounts             []Mount        // host->container bind mounts added at `up`
```

Add the config type (after `UpResult`):

```go
// ConfigInfo is the subset of a devcontainer's merged configuration the engine
// needs before `up` (to resolve the live transcript mount target).
type ConfigInfo struct {
	RemoteUser string
}
```

Add two methods to the `Backend` interface (after `Up`):

```go
	ReadConfig(ctx context.Context, workspaceFolder string) (ConfigInfo, error)
	CopyFrom(ctx context.Context, id, srcPath, hostPath string) error
```

- [ ] **Step 4: Edit `internal/backend/devcontainer.go`** — extract `upArgs`, add `ReadConfig` + `remoteUserFromConfig`

Replace the body of `Up` that builds `args` with a call to `upArgs`, and add the new functions. The full `Up` becomes:

```go
func (d *dockerBackend) Up(ctx context.Context, o UpOpts) (UpResult, error) {
	args, err := upArgs(o)
	if err != nil {
		return UpResult{}, err
	}
	out, err := devcontainer(ctx, args...)
	if err != nil {
		return UpResult{}, err
	}
	if res := upResultFromOutput(out); res.ID != "" {
		return res, nil
	}
	// Fallback: resolve by the agent label we just applied (remote user unknown).
	id, err := run(ctx, "ps", "-aq", "--no-trunc", "--filter", "status=running", "--filter", "label="+LabelAgent+"="+o.Labels[LabelAgent])
	if err != nil {
		return UpResult{}, err
	}
	return UpResult{ID: id}, nil
}

// upArgs builds the `devcontainer up` argument list: workspace, optional
// additional-features, bind mounts, and id-labels.
func upArgs(o UpOpts) ([]string, error) {
	args := []string{"up", "--workspace-folder", o.WorkspaceFolder}
	if len(o.AdditionalFeatures) > 0 {
		b, err := json.Marshal(o.AdditionalFeatures)
		if err != nil {
			return nil, fmt.Errorf("marshal additional-features: %w", err)
		}
		args = append(args, "--additional-features", string(b))
	}
	for _, m := range o.Mounts {
		args = append(args, "--mount", "type=bind,source="+m.Source+",target="+m.Target)
	}
	for k, v := range o.Labels {
		args = append(args, "--id-label", k+"="+v)
	}
	return args, nil
}

// ReadConfig reads the devcontainer's merged configuration without starting it,
// so the engine can resolve remoteUser before `up`.
func (d *dockerBackend) ReadConfig(ctx context.Context, workspaceFolder string) (ConfigInfo, error) {
	out, err := devcontainer(ctx, "read-configuration", "--workspace-folder", workspaceFolder, "--include-merged-configuration")
	if err != nil {
		return ConfigInfo{}, err
	}
	return ConfigInfo{RemoteUser: remoteUserFromConfig(out)}, nil
}

// remoteUserFromConfig pulls remoteUser from the trailing JSON line that
// `devcontainer read-configuration` emits (mergedConfiguration first, then the
// top-level field). Best-effort: "" when absent.
func remoteUserFromConfig(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r struct {
			MergedConfiguration struct {
				RemoteUser string `json:"remoteUser"`
			} `json:"mergedConfiguration"`
			RemoteUser string `json:"remoteUser"`
		}
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			if r.MergedConfiguration.RemoteUser != "" {
				return r.MergedConfiguration.RemoteUser
			}
			return r.RemoteUser
		}
	}
	return ""
}
```

- [ ] **Step 5: Edit `internal/backend/docker.go`** — add `CopyFrom` (next to `CopyTo`)

```go
// CopyFrom copies a file/dir out of the container to the host (docker cp).
func (d *dockerBackend) CopyFrom(ctx context.Context, id, srcPath, hostPath string) error {
	_, err := run(ctx, "cp", id+":"+srcPath, hostPath)
	return err
}
```

- [ ] **Step 6: Edit `internal/backend/fake.go`** — add fields + methods

Add `"path/filepath"` to the import block. Add fields to the `Fake` struct (after `RemoteWorkspaceFolder`):

```go
	ReadConfigResult ConfigInfo // returned by ReadConfig (zero → empty RemoteUser → fallback)
	ReadConfigErr    error      // optional error from ReadConfig
	CopyFromCalls    []CopyCall // records CopyFrom invocations
```

Add the two methods (near `Up`/`CopyTo`):

```go
func (f *Fake) ReadConfig(_ context.Context, _ string) (ConfigInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ReadConfigResult, f.ReadConfigErr
}

func (f *Fake) CopyFrom(_ context.Context, id, srcPath, hostPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CopyFromCalls = append(f.CopyFromCalls, CopyCall{ID: id, HostPath: hostPath, DestPath: srcPath})
	// Simulate the copy landing on the host so callers' empty-dir idempotency
	// guards flip on a second call.
	_ = os.MkdirAll(hostPath, 0o777)
	_ = os.WriteFile(filepath.Join(hostPath, "session.jsonl"), []byte("{}"), 0o644)
	return nil
}
```

- [ ] **Step 7: Run tests + build to verify they pass**

Run: `go test ./internal/backend/ -v && go build ./...`
Expected: PASS; clean build (the new interface methods are satisfied by `dockerBackend` and `Fake`).

- [ ] **Step 8: Commit**

```bash
git add internal/backend/
git commit -m "feat(backend): UpOpts.Mounts, ReadConfig, CopyFrom, LabelLogDir + fake support"
```

---

### Task 2: Fleet log-path helpers — slug, session-dir, target expansion

**Files:**
- Create: `internal/fleet/logs.go`
- Create: `internal/fleet/logs_test.go`
- Modify: `internal/fleet/fleet.go` (add `LogRoot` field + `logsRoot()` helper)

**Interfaces:**
- Produces:
  - `const containerSessionDir = "/flotilla/session"`
  - `func repoSlug(repoURL string) string`
  - `func sessionDirName(name string, t time.Time) string`
  - `func transcriptTarget(transcriptPath, home string) string`
  - `Fleet.LogRoot string` + `func (f *Fleet) logsRoot() string`

- [ ] **Step 1: Write the failing test** (`internal/fleet/logs_test.go`)

```go
package fleet

import (
	"testing"
	"time"
)

func TestRepoSlug(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo.git": "owner-repo",
		"https://github.com/owner/repo":     "owner-repo",
		"git@github.com:owner/repo.git":     "owner-repo",
		"ssh://git@github.com/owner/repo":   "owner-repo",
		"":                                  "repo",
	}
	for in, want := range cases {
		if got := repoSlug(in); got != want {
			t.Errorf("repoSlug(%q) = %q, want %q", in, got, want)
		}
	}
	// Unsafe characters collapse to '-'.
	if got := repoSlug("https://h/o w/r$x"); got != "o-w-r-x" {
		t.Errorf("repoSlug(unsafe) = %q, want o-w-r-x", got)
	}
}

func TestSessionDirName(t *testing.T) {
	ts := time.Date(2026, 6, 23, 19, 5, 0, 0, time.UTC)
	if got := sessionDirName("atlas", ts); got != "2026-06-23-1905-atlas" {
		t.Errorf("sessionDirName = %q, want 2026-06-23-1905-atlas", got)
	}
}

func TestTranscriptTarget(t *testing.T) {
	if got := transcriptTarget("~/.claude/projects", "/home/ubuntu"); got != "/home/ubuntu/.claude/projects" {
		t.Errorf("transcriptTarget(~) = %q", got)
	}
	if got := transcriptTarget("~", "/home/ubuntu"); got != "/home/ubuntu" {
		t.Errorf("transcriptTarget(bare ~) = %q", got)
	}
	if got := transcriptTarget("/abs/path", "/home/ubuntu"); got != "/abs/path" {
		t.Errorf("transcriptTarget(abs) = %q", got)
	}
	if got := transcriptTarget("", "/home/ubuntu"); got != "" {
		t.Errorf("transcriptTarget(empty) = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run "TestRepoSlug|TestSessionDirName|TestTranscriptTarget" -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write `internal/fleet/logs.go`**

```go
package fleet

import (
	"path/filepath"
	"strings"
	"time"
)

// containerSessionDir is the fixed, user-agnostic mount point for an agent's
// host session-log dir. The launch wrapper writes container.log + status here,
// so it never needs the run user's home resolved.
const containerSessionDir = "/flotilla/session"

// logsRoot is the host dir holding per-session logs (default ~/.flotilla/logs).
func (f *Fleet) logsRoot() string {
	if f.LogRoot != "" {
		return f.LogRoot
	}
	return filepath.Join(homeDir(), ".flotilla", "logs")
}

// repoSlug turns a repo URL into a filesystem-safe "owner-repo" slug. It strips
// the scheme and a trailing .git, keeps the last two path segments, and replaces
// any char outside [A-Za-z0-9._-] with '-'. Falls back to "repo".
func repoSlug(repoURL string) string {
	s := strings.TrimSuffix(strings.TrimSpace(repoURL), ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.ReplaceAll(s, ":", "/")
	var parts []string
	for _, p := range strings.Split(s, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	var tail []string
	switch {
	case len(parts) >= 2:
		tail = parts[len(parts)-2:]
	case len(parts) == 1:
		tail = parts
	default:
		return "repo"
	}
	slug := sanitizeSlug(strings.Join(tail, "-"))
	if slug == "" {
		return "repo"
	}
	return slug
}

func sanitizeSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// sessionDirName builds "<YYYY-MM-DD-HHMM>-<agent>".
func sessionDirName(name string, t time.Time) string {
	return t.Format("2006-01-02-1504") + "-" + name
}

// transcriptTarget expands a profile's transcript_path against the run user's
// home (replacing a leading ~). Empty in → empty out (no transcript mount).
func transcriptTarget(transcriptPath, home string) string {
	p := strings.TrimSpace(transcriptPath)
	switch {
	case p == "":
		return ""
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return strings.TrimRight(home, "/") + "/" + p[2:]
	default:
		return p
	}
}
```

> Note on the unsafe-input case: `repoSlug("https://h/o w/r$x")` → strip scheme → `h/o w/r$x` → last two segments `o w` + `r$x` joined `o w-r$x` → sanitized `o-w-r-x`. Spaces and `$` both map to `-`.

- [ ] **Step 4: Add the `LogRoot` field** in `internal/fleet/fleet.go`

In the `Fleet` struct, after `WorkRoot`:

```go
	LogRoot        string      // host dir for per-session logs; defaults under ~/.flotilla
```

- [ ] **Step 5: Run test + build to verify they pass**

Run: `go test ./internal/fleet/ -run "TestRepoSlug|TestSessionDirName|TestTranscriptTarget" -v && go build ./...`
Expected: PASS; clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/logs.go internal/fleet/logs_test.go internal/fleet/fleet.go
git commit -m "feat(fleet): log-path helpers (repoSlug, sessionDirName, transcriptTarget)"
```

---

### Task 3: Launch wrapper writes status + tees container.log

**Files:**
- Modify: `internal/fleet/spawn_helpers.go` (`launchScript` signature + body)
- Modify: `internal/fleet/spawn_helpers_test.go` (update existing calls, assert new shape)
- Modify: `internal/fleet/fleet.go:158` (pass `containerSessionDir` to the call site)

**Interfaces:**
- Consumes: `containerSessionDir` (Task 2).
- Produces: `func launchScript(launch, home, workdir, sessionDir string) string` — now writes `running` before, redirects the agent's stdout/stderr to `<sessionDir>/container.log`, and writes `done` after (no `exec`).

- [ ] **Step 1: Update the failing test** (`internal/fleet/spawn_helpers_test.go`)

Replace the existing `launchScript` test(s) with the 4-arg form plus new assertions. The current file calls `launchScript(..., "/home/ubuntu", "/workspaces/atlas")` and `launchScript("x", "/root", "")` — update both call sites and the assertions:

```go
func TestLaunchScriptCdEnvAndWrapUp(t *testing.T) {
	got := launchScript(`claude -p "$FLOTILLA_PROMPT"`, "/home/ubuntu", "/workspaces/atlas", "/flotilla/session")
	if !strings.Contains(got, "cd '/workspaces/atlas'") {
		t.Errorf("missing cd into workspace: %q", got)
	}
	if !strings.Contains(got, "export HOME=/home/ubuntu") {
		t.Errorf("missing HOME export: %q", got)
	}
	if !strings.Contains(got, "echo running > /flotilla/session/status") {
		t.Errorf("missing running status write: %q", got)
	}
	if !strings.Contains(got, "> /flotilla/session/container.log 2>&1") {
		t.Errorf("missing container.log redirect: %q", got)
	}
	if !strings.Contains(got, "echo done > /flotilla/session/status") {
		t.Errorf("missing done status write: %q", got)
	}
	if strings.Contains(got, "exec ") {
		t.Errorf("launch must not exec away the wrapper (status write would be lost): %q", got)
	}
}

func TestLaunchScriptWorkspaceGlobFallback(t *testing.T) {
	g := launchScript("x", "/root", "", "/flotilla/session")
	if !strings.Contains(g, "/workspaces/*/") {
		t.Errorf("missing workspace glob fallback: %q", g)
	}
}
```

> Keep the file's existing package/import lines (`package fleet`, `"strings"`, `"testing"`). If the current test function has different names, replace them entirely with the two above.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestLaunchScript -v`
Expected: FAIL — `launchScript` arity mismatch / missing status strings.

- [ ] **Step 3: Update `launchScript`** in `internal/fleet/spawn_helpers.go`

```go
// launchScript cd's into the mounted workspace (the devcontainer's reported
// remoteWorkspaceFolder, or a /workspaces/* glob fallback), sets HOME, sources
// the injected env-file, loads the prompt into $FLOTILLA_PROMPT, records
// "running" in the session status file, runs the agent with stdout/stderr teed
// to the mounted container.log, then records "done". It must NOT exec away the
// wrapper shell — the post-run status write depends on the shell surviving.
func launchScript(launch, home, workdir, sessionDir string) string {
	cd := `cd "$(ls -d /workspaces/*/ 2>/dev/null | head -1)" 2>/dev/null`
	if workdir != "" {
		cd = fmt.Sprintf("cd '%s' 2>/dev/null", workdir)
	}
	return fmt.Sprintf(
		`%s; export HOME=%s; set -a; . %s 2>/dev/null; set +a; export FLOTILLA_PROMPT="$(cat %s 2>/dev/null)"; echo running > %s/status 2>/dev/null; %s > %s/container.log 2>&1; echo done > %s/status 2>/dev/null`,
		cd, home, agentEnvFile(home), agentPromptFile(home), sessionDir, launch, sessionDir, sessionDir)
}
```

- [ ] **Step 4: Update the call site** in `internal/fleet/fleet.go`

Change the launch line (currently `~158`):

```go
	if err := f.Backend.ExecDetached(ctx, id, runAsUser(user, launchScript(prof.RenderLaunch(), home, res.RemoteWorkspaceFolder, containerSessionDir))); err != nil {
		return fail(fmt.Errorf("launch agent: %w", err))
	}
```

- [ ] **Step 5: Run tests + build to verify they pass**

Run: `go test ./internal/fleet/ -run TestLaunchScript -v && go build ./...`
Expected: PASS; clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/spawn_helpers.go internal/fleet/spawn_helpers_test.go internal/fleet/fleet.go
git commit -m "feat(fleet): launch wrapper tees container.log and writes running/done status"
```

---

### Task 4: Wire session dir, mounts, label, chown, and fallback sentinel into `Spawn`

**Files:**
- Modify: `internal/fleet/fleet.go` (`Spawn`)
- Create: `internal/fleet/spawn_logs_test.go`

**Interfaces:**
- Consumes: `repoSlug`, `sessionDirName`, `transcriptTarget`, `containerSessionDir`, `logsRoot` (Task 2); `backend.Mount`, `backend.LabelLogDir`, `Backend.ReadConfig` (Task 1); existing `homeForUser`.
- Produces: `Spawn` creates `<logsRoot>/<slug>/<ts>-<name>/transcript/`, mounts `<session>→/flotilla/session` (always) + the transcript mount (when resolvable), sets the `flotilla.logdir` label, `chown -R`s the session tree post-up, and writes a `.copy-fallback` sentinel (containing the absolute in-container transcript path) when the live mount was skipped.

- [ ] **Step 1: Write the failing test** (`internal/fleet/spawn_logs_test.go`)

```go
package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

// mountTargets returns the set of mount targets recorded on the fake's only Up.
func mountTargets(t *testing.T, fake *backend.Fake) (string, []string) {
	t.Helper()
	if len(fake.UpCalls) != 1 {
		t.Fatalf("UpCalls = %d, want 1", len(fake.UpCalls))
	}
	up := fake.UpCalls[0]
	var targets []string
	for _, m := range up.Mounts {
		targets = append(targets, m.Target)
	}
	return up.Labels[backend.LabelLogDir], targets
}

func TestSpawnMountsSessionAndLiveTranscript(t *testing.T) {
	fake := backend.NewFake()
	fake.RemoteUser = "ubuntu"
	fake.ReadConfigResult = backend.ConfigInfo{RemoteUser: "ubuntu"}
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), LogRoot: t.TempDir()}
	prof := agent.Profile{Name: "claude", Launch: `echo "{prompt}"`, TranscriptPath: "~/.claude/projects"}

	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	logDir, targets := mountTargets(t, fake)
	if logDir == "" || filepath.Dir(filepath.Dir(logDir)) != f.LogRoot {
		t.Errorf("logdir label = %q, want under %q", logDir, f.LogRoot)
	}
	if !contains(targets, containerSessionDir) {
		t.Errorf("missing fixed session mount %q in %v", containerSessionDir, targets)
	}
	if !contains(targets, "/home/ubuntu/.claude/projects") {
		t.Errorf("missing live transcript mount in %v", targets)
	}
	// Host transcript dir was created.
	if _, err := os.Stat(filepath.Join(logDir, "transcript")); err != nil {
		t.Errorf("host transcript dir missing: %v", err)
	}
	// No copy-fallback sentinel when live-mounted.
	if _, err := os.Stat(filepath.Join(logDir, ".copy-fallback")); !os.IsNotExist(err) {
		t.Errorf(".copy-fallback should be absent on live mount, stat err = %v", err)
	}
}

func TestSpawnCopyFallbackWhenRemoteUserUnresolved(t *testing.T) {
	fake := backend.NewFake()
	fake.RemoteUser = "ubuntu"                              // known post-up (from Up result)
	fake.ReadConfigResult = backend.ConfigInfo{RemoteUser: ""} // unresolved pre-up
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), LogRoot: t.TempDir()}
	prof := agent.Profile{Name: "claude", Launch: `echo "{prompt}"`, TranscriptPath: "~/.claude/projects"}

	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	logDir, targets := mountTargets(t, fake)
	if contains(targets, "/home/ubuntu/.claude/projects") {
		t.Errorf("transcript should NOT be live-mounted when unresolved: %v", targets)
	}
	if !contains(targets, containerSessionDir) {
		t.Errorf("fixed session mount still required: %v", targets)
	}
	b, err := os.ReadFile(filepath.Join(logDir, ".copy-fallback"))
	if err != nil {
		t.Fatalf("expected .copy-fallback sentinel: %v", err)
	}
	if got := string(b); got != "/home/ubuntu/.claude/projects\n" {
		t.Errorf(".copy-fallback = %q, want the resolved abs path", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run "TestSpawnMounts|TestSpawnCopyFallback" -v`
Expected: FAIL — no mounts recorded / no `flotilla.logdir` label / no sentinel.

- [ ] **Step 3: Edit `Spawn`** in `internal/fleet/fleet.go`

(a) After the devcontainer-overlay block (after the `if !hasDevcontainer(dest) {...}` block, ~line 86) and before `f.Backend.Up`, insert:

```go
	// Per-session host log dir: live transcript mount + container.log + status.
	session := filepath.Join(f.logsRoot(), repoSlug(repoURL), sessionDirName(name, time.Now()))
	transcript := filepath.Join(session, "transcript")
	if err := os.MkdirAll(transcript, 0o777); err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("create log dir: %w", err)
	}
	_ = os.Chmod(session, 0o777)
	_ = os.Chmod(transcript, 0o777)

	// Always mount the session dir at a fixed, user-agnostic path (container.log
	// + status ride here). Add the live transcript mount only when we can resolve
	// the run user's home before `up` (Docker needs an absolute container path).
	mounts := []backend.Mount{{Source: session, Target: containerSessionDir}}
	liveMount := false
	if cfg, err := f.Backend.ReadConfig(ctx, dest); err == nil && cfg.RemoteUser != "" {
		if target := transcriptTarget(prof.TranscriptPath, homeForUser(cfg.RemoteUser)); target != "" {
			mounts = append(mounts, backend.Mount{Source: transcript, Target: target})
			liveMount = true
		}
	}
```

(b) Change the `f.Backend.Up(...)` call to add `Mounts` and the log-dir label:

```go
	res, err := f.Backend.Up(ctx, backend.UpOpts{
		Name:               name,
		WorkspaceFolder:    dest,
		AdditionalFeatures: map[string]any{"./flotilla-toolchain": map[string]any{}},
		Mounts:             mounts,
		Labels: map[string]string{
			backend.LabelAgent:   name,
			backend.LabelRepo:    repoURL,
			backend.LabelCreated: time.Now().UTC().Format(time.RFC3339),
			backend.LabelHost:    "local",
			backend.LabelLogDir:  session,
		},
	})
```

(c) After `home := homeForUser(user)` (~line 108) and before `inj := &injector{...}`, insert the chown + sentinel:

```go
	// Make the mounted session tree writable by the run user (uid may differ
	// from the host). Best-effort.
	_ = f.Backend.Exec(ctx, id, []string{"chown", "-R", user, containerSessionDir})

	// If we couldn't live-mount the transcript, record where to copy it from
	// after the agent exits (the real run user is known now, post-up).
	if !liveMount && strings.TrimSpace(prof.TranscriptPath) != "" {
		if target := transcriptTarget(prof.TranscriptPath, home); target != "" {
			_ = os.WriteFile(filepath.Join(session, ".copy-fallback"), []byte(target+"\n"), 0o644)
		}
	}
```

> All referenced packages (`os`, `filepath`, `time`, `strings`, `backend`) are already imported in `fleet.go`. The failure-cleanup `fail` closure is unchanged: it removes the container + clone but intentionally leaves `session` (logs persist even on a failed spawn).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/fleet/ -run "TestSpawn" -v`
Expected: PASS — the two new tests plus all existing `TestSpawn*` (which set no `LogRoot`/`ReadConfigResult`; they hit the default log root and the no-transcript path, unaffected).

- [ ] **Step 5: Full fleet package + build**

Run: `go test ./internal/fleet/ && go build ./...`
Expected: PASS; clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/fleet.go internal/fleet/spawn_logs_test.go
git commit -m "feat(fleet): Spawn mounts session+transcript, sets logdir label, chown, fallback sentinel"
```

---

### Task 5: `Fleet.Logs` accessor + lazy copy-fallback + `Agent.LogDir`

**Files:**
- Modify: `internal/fleet/logs.go` (add `LogInfo`, `Logs`, `maybeCopyTranscript`)
- Modify: `internal/fleet/fleet.go` (`Agent.LogDir` field + populate in `List`)
- Create: `internal/fleet/logs_accessor_test.go`

**Interfaces:**
- Consumes: `f.resolve`, `backend.LabelLogDir`, `Backend.CopyFrom`, `backend.Container` (Tasks 1, existing).
- Produces:
  - `type LogInfo struct { Agent, LogDir, Status, TranscriptPath string }` (JSON-tagged)
  - `func (f *Fleet) Logs(ctx context.Context, name string) (LogInfo, error)`
  - `Agent.LogDir string` populated in `List`.

- [ ] **Step 1: Write the failing test** (`internal/fleet/logs_accessor_test.go`)

```go
package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// seedLoggedAgent registers a fake container labelled name with a logdir whose
// status file says status, and returns the fake + log dir.
func seedLoggedAgent(t *testing.T, fake *backend.Fake, name, status string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "transcript"), 0o777); err != nil {
		t.Fatal(err)
	}
	if status != "" {
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = fake.Create(context.Background(), backend.CreateOpts{Labels: map[string]string{
		backend.LabelAgent:  name,
		backend.LabelRepo:   "r",
		backend.LabelLogDir: dir,
	}})
	return dir
}

func TestLogsResolvesDirAndStatus(t *testing.T) {
	fake := backend.NewFake()
	dir := seedLoggedAgent(t, fake, "atlas", "done")
	f := &Fleet{Backend: fake}
	info, err := f.Logs(context.Background(), "atlas")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if info.LogDir != dir || info.Status != "done" {
		t.Errorf("info = %+v, want dir %q status done", info, dir)
	}
	if info.TranscriptPath != filepath.Join(dir, "transcript") {
		t.Errorf("TranscriptPath = %q", info.TranscriptPath)
	}
}

func TestLogsErrorsWithoutLabel(t *testing.T) {
	fake := backend.NewFake()
	_, _ = fake.Create(context.Background(), backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas", backend.LabelRepo: "r"}})
	f := &Fleet{Backend: fake}
	if _, err := f.Logs(context.Background(), "atlas"); err == nil {
		t.Error("expected error when no logdir label is set")
	}
}

func TestLogsCopyFallbackOnExit(t *testing.T) {
	fake := backend.NewFake()
	dir := seedLoggedAgent(t, fake, "atlas", "done")
	// Flag copy-fallback and mark the container exited.
	if err := os.WriteFile(filepath.Join(dir, ".copy-fallback"), []byte("/home/ubuntu/.claude/projects\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs, _ := fake.List(context.Background(), map[string]string{backend.LabelAgent: "atlas"})
	_ = fake.SetStatus(cs[0].ID, "exited")

	f := &Fleet{Backend: fake}
	if _, err := f.Logs(context.Background(), "atlas"); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(fake.CopyFromCalls) != 1 {
		t.Fatalf("CopyFrom calls = %d, want 1", len(fake.CopyFromCalls))
	}
	if fake.CopyFromCalls[0].DestPath != "/home/ubuntu/.claude/projects/." {
		t.Errorf("CopyFrom src = %q, want trailing /.", fake.CopyFromCalls[0].DestPath)
	}
	// Idempotent: the fake's CopyFrom populated the transcript dir, so a second
	// call must not copy again.
	if _, err := f.Logs(context.Background(), "atlas"); err != nil {
		t.Fatalf("Logs (2nd): %v", err)
	}
	if len(fake.CopyFromCalls) != 1 {
		t.Errorf("CopyFrom should be idempotent, got %d calls", len(fake.CopyFromCalls))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestLogs -v`
Expected: FAIL — `undefined: (*Fleet).Logs` / `undefined: LogInfo`.

- [ ] **Step 3: Add `LogInfo` + `Logs` + `maybeCopyTranscript`** to `internal/fleet/logs.go`

Add `"context"`, `"fmt"`, and `"os"` to the import block, and:

```go
// LogInfo locates an agent's persisted logs.
type LogInfo struct {
	Agent          string `json:"agent"`
	LogDir         string `json:"logDir"`
	Status         string `json:"status"`         // "running" | "done" | "" (unknown)
	TranscriptPath string `json:"transcriptPath"` // host transcript dir
}

// Logs resolves an agent's log dir from its container label, reads the persisted
// status, and (when the agent has exited and its transcript was copy-fallback)
// lazily copies the in-container transcript to the host. Idempotent.
func (f *Fleet) Logs(ctx context.Context, name string) (LogInfo, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return LogInfo{}, err
	}
	dir := c.Labels[backend.LabelLogDir]
	if dir == "" {
		return LogInfo{}, fmt.Errorf("no logs recorded for agent %q", name)
	}
	info := LogInfo{Agent: name, LogDir: dir, TranscriptPath: filepath.Join(dir, "transcript")}
	if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		info.Status = strings.TrimSpace(string(b))
	}
	f.maybeCopyTranscript(ctx, c, dir)
	return info, nil
}

// maybeCopyTranscript performs the copy-out fallback: if the session is flagged
// copy-fallback, the container has exited, and the host transcript dir is still
// empty, docker cp it out of the container. Best-effort + idempotent.
func (f *Fleet) maybeCopyTranscript(ctx context.Context, c backend.Container, dir string) {
	src, err := os.ReadFile(filepath.Join(dir, ".copy-fallback"))
	if err != nil {
		return // live-mounted (or no transcript) — nothing to copy
	}
	if c.Status != "exited" {
		return // still running; the transcript is being written in-container
	}
	host := filepath.Join(dir, "transcript")
	if entries, _ := os.ReadDir(host); len(entries) > 0 {
		return // already copied
	}
	_ = f.Backend.CopyFrom(ctx, c.ID, strings.TrimSpace(string(src))+"/.", host)
}
```

> Add the import `"github.com/mickzijdel/flotilla/internal/backend"` to `logs.go` (it now references `backend.LabelLogDir`/`backend.Container`).

- [ ] **Step 4: Add `Agent.LogDir`** in `internal/fleet/fleet.go`

In the `Agent` struct, after `ID`:

```go
	LogDir  string    `json:"logDir,omitempty"`
```

In `List`, populate it when building each `Agent`:

```go
		out = append(out, Agent{Name: c.Name, Repo: c.Repo, Status: c.Status, Created: c.Created, ID: c.ID, LogDir: c.Labels[backend.LabelLogDir]})
```

- [ ] **Step 5: Run tests + build to verify they pass**

Run: `go test ./internal/fleet/ -run TestLogs -v && go build ./...`
Expected: PASS; clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/logs.go internal/fleet/logs_accessor_test.go internal/fleet/fleet.go
git commit -m "feat(fleet): Logs accessor with lazy copy-fallback + Agent.LogDir"
```

---

### Task 6: `flotilla logs` CLI command

**Files:**
- Create: `internal/cli/logs.go` (the command + follow loop)
- Modify: `internal/cli/cli.go` (register `logsCmd` in `BuildRoot`)
- Create: `internal/cli/logs_test.go`

**Interfaces:**
- Consumes: `f.Logs(ctx, name)` → `fleet.LogInfo` (Task 5).
- Produces: `flotilla logs <agent> [-f|--follow] [--json]`.

- [ ] **Step 1: Write the failing test** (`internal/cli/logs_test.go`)

```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func seedLoggedContainer(t *testing.T, fake *backend.Fake, name, logBody, status string) string {
	t.Helper()
	dir := t.TempDir()
	if logBody != "" {
		if err := os.WriteFile(filepath.Join(dir, "container.log"), []byte(logBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if status != "" {
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = fake.Create(context.Background(), backend.CreateOpts{Labels: map[string]string{
		backend.LabelAgent:  name,
		backend.LabelRepo:   "r",
		backend.LabelLogDir: dir,
	}})
	return dir
}

func TestLogsCmdPrintsContainerLog(t *testing.T) {
	fake := backend.NewFake()
	seedLoggedContainer(t, fake, "atlas", "hello world\n", "done")
	f := &fleet.Fleet{Backend: fake}

	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logs", "atlas"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("logs: %v: %s", err, out.String())
	}
	if out.String() != "hello world\n" {
		t.Errorf("output = %q, want 'hello world\\n'", out.String())
	}
}

func TestLogsCmdJSONEnvelope(t *testing.T) {
	fake := backend.NewFake()
	seedLoggedContainer(t, fake, "atlas", "x\n", "done")
	f := &fleet.Fleet{Backend: fake}

	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logs", "atlas", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("logs --json: %v: %s", err, out.String())
	}
	var info fleet.LogInfo
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if info.Agent != "atlas" || info.Status != "done" {
		t.Errorf("info = %+v", info)
	}
}

func TestLogsCmdFollowDrainsUntilDone(t *testing.T) {
	fake := backend.NewFake()
	// status already "done", so follow drains once and exits immediately.
	seedLoggedContainer(t, fake, "atlas", "line1\nline2\n", "done")
	f := &fleet.Fleet{Backend: fake}

	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logs", "atlas", "-f"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("logs -f: %v: %s", err, out.String())
	}
	if out.String() != "line1\nline2\n" {
		t.Errorf("follow output = %q", out.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestLogsCmd -v`
Expected: FAIL — `logs` is an unknown command (error in output).

- [ ] **Step 3: Write `internal/cli/logs.go`**

```go
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func logsCmd(f *fleet.Fleet) *cobra.Command {
	var follow, asJSON bool
	c := &cobra.Command{
		Use:   "logs <agent>",
		Short: "Stream an agent's container.log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := f.Logs(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
			}
			logPath := filepath.Join(info.LogDir, "container.log")
			if follow {
				return followLog(cmd.Context(), info.LogDir, logPath, cmd.OutOrStdout())
			}
			b, err := os.ReadFile(logPath)
			if err != nil {
				return fmt.Errorf("read log for %q: %w", args[0], err)
			}
			_, err = cmd.OutOrStdout().Write(b)
			return err
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "stream new log output until the agent finishes")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON metadata (logDir, status, transcript)")
	return c
}

// followLog tails container.log, draining new bytes every 200ms until the
// session status file reads "done" (then it drains once more and exits).
func followLog(ctx context.Context, dir, logPath string, out io.Writer) error {
	var offset int64
	for {
		offset = drainFrom(logPath, offset, out)
		if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil && strings.TrimSpace(string(b)) == "done" {
			drainFrom(logPath, offset, out) // final drain
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// drainFrom copies container.log bytes from offset to out, returning the new
// offset. Missing file is treated as "nothing yet" (offset unchanged).
func drainFrom(path string, offset int64, out io.Writer) int64 {
	file, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	n, _ := io.Copy(out, file)
	return offset + n
}

var _ = context.Background // keep context import if unused after edits
```

> Remove the `var _ = context.Background` line if `context` is already referenced (it is, via `cmd.Context()` returning `context.Context` and `ctx.Done()`); it's only a guard in case of trimming. Prefer deleting it once the file compiles.

- [ ] **Step 4: Register the command** in `internal/cli/cli.go`

```go
	root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), submitCmd(f), logsCmd(f), agentsCmd(), doctorCmd())
```

- [ ] **Step 5: Run tests + build to verify they pass**

Run: `go test ./internal/cli/ -run TestLogsCmd -v && go build ./...`
Expected: PASS; clean build. (If `go vet`/compile flags the `var _ = context.Background` guard as redundant, delete that line.)

- [ ] **Step 6: Commit**

```bash
git add internal/cli/logs.go internal/cli/cli.go internal/cli/logs_test.go
git commit -m "feat(cli): flotilla logs command (follow + json)"
```

---

### Task 7: Docs — README + backlog

**Files:**
- Modify: `README.md` (status paragraph + command list)
- Modify: `docs/backlog.md` (mark item #1 done; renumber the remaining list)

- [ ] **Step 1: Update `README.md`**

In the `## Status` section, add that per-session logs now persist under `~/.flotilla/logs/<repo>/<date>-<agent>/` (live transcript mount + `container.log` + `status`), and add `logs` to the documented command list (next to `attach`/`submit`), e.g.:

```
flotilla logs <agent> [-f]    # stream the agent's container.log (-f follows until done)
```

- [ ] **Step 2: Update `docs/backlog.md`**

Change the "Next plans" list so item #1 reads as done and the rest renumber:

```markdown
- ~~**Logs / transcript mounts** — persist per-session logs + the agent transcript to a host dir
  under `~/.flotilla/logs/<repo>/<YYYY-MM-DD-HHMM>-<agent>/` (live transcript bind-mount,
  teed `container.log`, daemon-free `status`), plus `flotilla logs <agent> [-f]`.~~ **Done** — see
  [spec](specs/2026-06-23-flotilla-logs-transcript-mounts-design.md) and
  [plan](plans/2026-06-23-flotilla-logs-transcript-mounts.md).
1. **On-demand fetch/pull** — let a running agent request the engine re-fetch/pull during a session
   (engine-side, no creds in container).
2. **CLI-driver skill** — a skill modelled on playwright-cli so agents can drive `flotilla` (the
   CLI is the primary control surface; the skill sits on top).
3. **VS Code extension** — UI over the CLI for managing multiple agents across repos at once.
4. **Remote backend** — `DOCKER_HOST` over TLS/SSH for multi-machine; the `Backend` interface seam
   is already in place. Docker Sandboxes / `sbx` could be added as an additional backend once it
   lands on Linux (see spec §7).
```

(Keep the surrounding "Roughly in dependency order:" intro and the rest of the file unchanged.)

- [ ] **Step 3: Commit**

```bash
git add README.md docs/backlog.md
git commit -m "docs: logs & transcript mounts shipped (flotilla logs)"
```

---

### Task 8: Full-suite verification

**Files:** none (verification only).

- [ ] **Step 1: Run the whole suite, build, and lint**

Run: `go test ./... && go build ./... && golangci-lint run ./... && golangci-lint fmt --diff`
Expected: all green; no format diff. Ingest the full output — do not tail/head it.

- [ ] **Step 2: If lint flags formatting**, run `golangci-lint fmt && golangci-lint run --fix ./...`, re-run Step 1, and commit any formatting-only changes:

```bash
git add -A
git commit -m "style: gofmt/lint fixes for logs & transcript mounts"
```

(Skip the commit if there were no changes.)

---

## Self-Review

**1. Spec coverage** (each spec section → task):
- §2 layout / `<repo-slug>` / timestamp → Task 2 (`repoSlug`, `sessionDirName`), Task 4 (dir creation).
- §2.1 `flotilla.logdir` label → Task 1 (constant), Task 4 (set on `Up`), Task 5/6 (read it).
- §2.2 persistence (`rm`/failed-spawn keep logs) → Task 4 (cleanup leaves `session`; `Remove` already only touches the container).
- §3 backend mount plumbing (`UpOpts.Mounts`, fake records) → Task 1.
- §4 live transcript mount + pre-up `ReadConfig` → Task 1 (`ReadConfig`/`ConfigInfo`), Task 2 (`transcriptTarget`), Task 4 (resolve + mount).
- §4.1 copy-out fallback (`.copy-fallback` sentinel) → Task 4 (write sentinel), Task 5 (`maybeCopyTranscript` + `CopyFrom`).
- §5 `container.log` (fixed `/flotilla/session` mount + tee) → Task 2 (`containerSessionDir`), Task 3 (wrapper), Task 4 (mount).
- §6 daemon-free `status` (running/done) → Task 3 (wrapper writes both).
- §6.1 lazy copy in `Fleet.Logs` only → Task 5 (`Logs`/`maybeCopyTranscript`; not in `resolve`).
- §7 permissions (0777 + chown) → Task 4 (`MkdirAll 0777`/`Chmod`/`chown -R`).
- §8 `flotilla logs` (`-f`, `--json`, errors) → Task 6.
- §9 `Spawn` wiring order + `Agent.LogDir` → Task 4, Task 5.
- §10 error-handling table → Task 4 (mkdir-fatal, ReadConfig/chown advisory), Task 5 (no-label error, cp advisory), Task 6 (missing file error).
- §11 testing seams (`ReadConfigResult`, records mounts) → Task 1 (fake), Tasks 2–6 (unit/fleet/CLI tests).
- §12 out-of-scope items → intentionally not built.

**2. Placeholder scan:** No "TBD"/"add error handling"/"similar to Task N". Every code step shows full code. The one advisory note (the `var _ = context.Background` guard in Task 6) gives the concrete action (delete it once compiling), not a deferral.

**3. Type consistency:** `ConfigInfo{RemoteUser}` (Task 1) is consumed by `Spawn`'s `f.Backend.ReadConfig` (Task 4). `backend.Mount{Source,Target}` (existing) is used unchanged in `UpOpts.Mounts` (Task 1) and built in `Spawn` (Task 4). `containerSessionDir` (Task 2) is consumed by `launchScript` (Task 3) and `Spawn` (Task 4). `LogInfo{Agent,LogDir,Status,TranscriptPath}` (Task 5) is decoded by the CLI test + `--json` path (Task 6). `LabelLogDir` (Task 1) is set in Task 4 and read in Tasks 5–6. `launchScript`'s new 4th param `sessionDir` (Task 3) matches the call site update in the same task.

**Note for the implementer:** Tasks 1→2→3→4→5→6 are a dependency chain (build in order); Task 7 (docs) and Task 8 (verification) come last. The two CLI test helpers (`seedLoggedContainer` in `internal/cli`, `seedLoggedAgent` in `internal/fleet`) live in different packages — no collision. Existing `TestSpawn*` tests in `fleet_test.go` set no `LogRoot`/`ReadConfigResult`, so they exercise the default log root + no-transcript path and must stay green after Task 4.
