# Flotilla On-demand fetch/pull Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a running, credential-less agent ask the engine to `git fetch origin` into its bind-mounted clone mid-session, so it can pick up base-branch changes without restarting.

**Architecture:** One git primitive (`gitops.Fetch`) reached by two triggers that both run host-side where the credentials live: (b) a direct CLI op `flotilla fetch <agent>` → `Fleet.Fetch`, daemon-agnostic; and (a) an in-container `flotilla-fetch` shim that drops a request file on the daemon's existing request-handler seam, which the daemon's `fetch` handler services by calling the same `Fleet.Fetch`. Because the clone is bind-mounted, the refreshed `origin/*` refs are live inside the container the instant the engine fetches. Fetch-only is the whole primitive — the engine never touches the working tree, HEAD, or any local branch; the agent integrates locally.

**Tech Stack:** Go 1.26, cobra CLI, the in-memory `backend.Fake` for unit tests, real-git temp dirs for `gitops` tests.

## Global Constraints

- Go 1.26.4; cobra v1.10.2; BurntSushi/toml v1.6.0 (copied from CLAUDE.md — do not add deps).
- The container **never** holds git credentials; only the engine (CLI for path b, daemon for path a) reaches `origin`. Fetch only writes `refs/remotes/origin/*`, `FETCH_HEAD`, and new objects — never index/HEAD/local branches.
- All git invocations go through the existing `gitops.git()` helper, which scopes `-c safe.directory=<dir>` for container-written `.git` files.
- Tests are Docker-free where possible (real-git temp dirs + `backend.Fake`); the one live-Docker integration test self-skips when Docker is unavailable, matching the existing pattern in `internal/backend`.
- Daemon request/response envelope and filesystem channel already exist (`internal/daemon/requests.go`): requests at `<sessionDir>/requests/<id>.json`, responses at `<sessionDir>/responses/<id>.json`; the `Handler` signature is `func(ctx, agent string, req Request) Response` and the supervisor dispatches per-agent with that agent's `LogDir` as `sessionDir`.
- **Terminal handler contract (reconciled with the shipped seam, spec §6 / commit `0c1dcbe`):** the `fetch` handler *returns* a `daemon.Response`; the dispatch loop (`dispatchRequests`, scanning `requests/` each supervisor tick) *writes* `responses/<id>.json` from that return value and is idempotent on an already-present response. Success is `Response{Status: "ok"}`; failure is `Response{Status: "error", Message: "<git stderr>"}` — the envelope's error field is **`Message`**, never a bespoke `error` field. The `deferred`-status seam change introduced in the same commit belongs to the **agent question/answer channel** (its spec §4.1), NOT to this fetch slice — `fetch` is purely terminal (ok/error) and must not touch `dispatchRequests`.

---

### Task 1: `gitops.Fetch` — the git primitive

**Files:**
- Create: `internal/gitops/fetch.go`
- Test: `internal/gitops/fetch_test.go`

**Interfaces:**
- Produces: `func Fetch(ctx context.Context, dir string) error` — runs `git -C dir -c safe.directory=dir fetch --prune origin`.
- Consumes: the unexported `git()` helper in `internal/gitops/inspect.go`; the test helpers `makeBareRepo`, `mustRun`, `cloneWithCommits` already in `internal/gitops/*_test.go`.

- [ ] **Step 1: Write the failing test**

```go
// internal/gitops/fetch_test.go
package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestFetchUpdatesRemoteTrackingRefWithoutTouchingWorkTree proves Fetch advances
// origin/<base> after the remote moves, while leaving HEAD, the index, and a
// dirty working tree completely untouched (the non-disruptive guarantee).
func TestFetchUpdatesRemoteTrackingRefWithoutTouchingWorkTree(t *testing.T) {
	bare := makeBareRepo(t)
	// Engine-side clone the agent works in.
	clone := filepath.Join(t.TempDir(), "clone")
	mustRun(t, "", "git", "clone", "-q", bare, clone)

	// HEAD and origin/main coincide right after clone.
	headBefore := revParse(t, clone, "HEAD")
	originBefore := revParse(t, clone, "refs/remotes/origin/main")
	if headBefore != originBefore {
		t.Fatalf("precondition: HEAD %s != origin/main %s", headBefore, originBefore)
	}

	// Advance the remote: a second clone commits and pushes to the bare repo.
	other := filepath.Join(t.TempDir(), "other")
	mustRun(t, "", "git", "clone", "-q", bare, other)
	mustRun(t, other, "git", "config", "user.email", "o@example.com")
	mustRun(t, other, "git", "config", "user.name", "o")
	if err := os.WriteFile(filepath.Join(other, "new.txt"), []byte("upstream"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, other, "git", "add", ".")
	mustRun(t, other, "git", "commit", "-q", "-m", "upstream change")
	mustRun(t, other, "git", "push", "-q", "origin", "main")

	// Dirty the agent's working tree to prove fetch leaves it alone.
	if err := os.WriteFile(filepath.Join(clone, "wip.txt"), []byte("in progress"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Fetch(context.Background(), clone); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// origin/main moved...
	originAfter := revParse(t, clone, "refs/remotes/origin/main")
	if originAfter == originBefore {
		t.Fatalf("origin/main did not advance: still %s", originAfter)
	}
	// ...but HEAD did not, and the untracked dirty file is still there.
	if got := revParse(t, clone, "HEAD"); got != headBefore {
		t.Fatalf("HEAD moved: %s != %s", got, headBefore)
	}
	if _, err := os.Stat(filepath.Join(clone, "wip.txt")); err != nil {
		t.Fatalf("dirty working-tree file disturbed by fetch: %v", err)
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := execCommand(dir, "git", "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return string(out)
}
```

- [ ] **Step 2: Add the small `execCommand` test helper if not already present**

`mustRun` discards output; this test needs captured stdout. Add to `fetch_test.go`:

```go
func execCommand(dir, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd
}
```

Add `"os/exec"` to the test file imports.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/gitops/ -run TestFetchUpdatesRemoteTrackingRef -v`
Expected: FAIL — `undefined: Fetch`.

- [ ] **Step 4: Write minimal implementation**

```go
// internal/gitops/fetch.go
package gitops

import "context"

// Fetch updates origin's remote-tracking refs in the engine-side clone. It is
// read/write-neutral on the working tree: it writes only refs/remotes/origin/*,
// FETCH_HEAD, and new objects — never the index, HEAD, or any local branch — so
// it is safe to run while the agent has uncommitted work. The engine holds the
// credentials; the container never does. --prune drops refs deleted upstream.
func Fetch(ctx context.Context, dir string) error {
	_, err := git(ctx, dir, "fetch", "--prune", "origin")
	return err
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/gitops/ -v`
Expected: PASS (all gitops tests).

- [ ] **Step 6: Commit**

```bash
git add internal/gitops/fetch.go internal/gitops/fetch_test.go
git commit -m "feat(gitops): Fetch primitive (fetch --prune origin, working-tree-neutral)"
```

---

### Task 2: `Fleet.Fetch` — host/orchestrator path

**Files:**
- Create: `internal/fleet/fetch.go`
- Test: `internal/fleet/fetch_test.go`

**Interfaces:**
- Produces: `func (f *Fleet) Fetch(ctx context.Context, name string) error` — resolves the agent (excluding the proxy sidecar via the existing `resolve`), confirms the clone exists at `workDir(name)`, runs `gitops.Fetch`.
- Consumes: `gitops.Fetch` (Task 1); existing `f.resolve`, `f.workDir`, `f.workRoot`.

- [ ] **Step 1: Write the failing test**

```go
// internal/fleet/fetch_test.go
package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// TestFetchRefreshesAgentClone runs the real Fleet.Fetch against a real clone an
// agent container "owns" (modelled with the fake backend) and asserts origin/main
// advances after the upstream moves.
func TestFetchRefreshesAgentClone(t *testing.T) {
	bare := bareRepo(t) // helper in fleet tests: a bare remote on main
	work := t.TempDir()
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, WorkRoot: work}

	// Model a running agent named "otter" whose clone lives at workDir("otter").
	dest := f.workDir("otter")
	mustRunCmd(t, "", "git", "clone", "-q", bare, dest)
	registerAgent(t, fake, "otter")

	originBefore := revParseDir(t, dest, "refs/remotes/origin/main")
	advanceRemote(t, bare) // commit+push a new commit to the bare remote

	if err := f.Fetch(context.Background(), "otter"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := revParseDir(t, dest, "refs/remotes/origin/main"); got == originBefore {
		t.Fatalf("origin/main did not advance after Fetch")
	}
}

func TestFetchUnknownAgent(t *testing.T) {
	f := &Fleet{Backend: backend.NewFake(), WorkRoot: t.TempDir()}
	err := f.Fetch(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), `no agent named "ghost"`) {
		t.Fatalf("want no-agent error, got %v", err)
	}
}

func TestFetchMissingClone(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, WorkRoot: t.TempDir()}
	registerAgent(t, fake, "otter") // agent exists, but no clone on disk
	err := f.Fetch(context.Background(), "otter")
	if err == nil || !strings.Contains(err.Error(), "no workspace clone for agent") {
		t.Fatalf("want missing-clone error, got %v", err)
	}
}

// registerAgent makes the fake backend report a running agent container of the
// given name (so f.resolve finds it).
func registerAgent(t *testing.T, fake *backend.Fake, name string) {
	t.Helper()
	if _, err := fake.Up(context.Background(), backend.UpOpts{
		Labels: map[string]string{backend.LabelAgent: name},
	}); err != nil {
		t.Fatal(err)
	}
}

func advanceRemote(t *testing.T, bare string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	mustRunCmd(t, "", "git", "clone", "-q", bare, other)
	mustRunCmd(t, other, "git", "config", "user.email", "o@example.com")
	mustRunCmd(t, other, "git", "config", "user.name", "o")
	if err := os.WriteFile(filepath.Join(other, "up.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunCmd(t, other, "git", "add", ".")
	mustRunCmd(t, other, "git", "commit", "-q", "-m", "upstream")
	mustRunCmd(t, other, "git", "push", "-q", "origin", "main")
}
```

Reuse/define the small git helpers `mustRunCmd` and `revParseDir` in this file if the fleet test package lacks them (check `internal/fleet/*_test.go` for an existing `bareRepo` and a run helper first; if `bareRepo` already exists, do not redefine it):

```go
import "os/exec"

func mustRunCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
}

func revParseDir(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return string(out)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestFetch -v`
Expected: FAIL — `f.Fetch undefined` (and possibly redefinition errors if a helper already exists — if so, drop the duplicate and reuse the existing one).

- [ ] **Step 3: Write minimal implementation**

```go
// internal/fleet/fetch.go
package fleet

import (
	"context"
	"fmt"
	"os"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// Fetch asks the engine to re-fetch origin into a running agent's engine-side
// clone, so the agent (which holds no credentials) can pick up base-branch
// changes mid-session. Host-side only: no container is touched, and it never
// merges, rebases, or writes the working tree — the agent integrates locally.
// Mirrors Submit's host-side resolve+clone-check model.
func (f *Fleet) Fetch(ctx context.Context, name string) error {
	if _, err := f.resolve(ctx, name); err != nil {
		return err
	}
	dir := f.workDir(name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no workspace clone for agent %q at %s (was it removed?)", name, dir)
	}
	return gitops.Fetch(ctx, dir)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run TestFetch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/fetch.go internal/fleet/fetch_test.go
git commit -m "feat(fleet): Fetch — host-side on-demand origin fetch for a running agent"
```

---

### Task 3: `flotilla fetch` CLI command

**Files:**
- Create: `internal/cli/fetch.go`
- Modify: `internal/cli/cli.go` (add `fetchCmd(f)` to `root.AddCommand(...)`)
- Test: `internal/cli/fetch_test.go`

**Interfaces:**
- Produces: `func fetchCmd(f *fleet.Fleet) *cobra.Command` — `flotilla fetch <agent> [--json]`.
- Consumes: `Fleet.Fetch` (Task 2). Human output `Fetched origin for <agent>`; `--json` output `{"agent":"<name>","fetched":true}`.

- [ ] **Step 1: Write the failing test**

```go
// internal/cli/fetch_test.go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func TestFetchCmdHumanOutput(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet(t, fake) // helper below resolves a Fleet with a registered agent+clone
	cmd := fetchCmd(f)
	cmd.SetArgs([]string{"otter"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "Fetched origin for otter") {
		t.Fatalf("human output = %q", out.String())
	}
}

func TestFetchCmdJSONOutput(t *testing.T) {
	fake := backend.NewFake()
	f := Fleet(t, fake)
	cmd := fetchCmd(f)
	cmd.SetArgs([]string{"otter", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got struct {
		Agent   string `json:"agent"`
		Fetched bool   `json:"fetched"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (out=%q)", err, out.String())
	}
	if got.Agent != "otter" || !got.Fetched {
		t.Fatalf("json output = %+v", got)
	}
}
```

`Fleet(t, fake)` is a tiny helper that builds a `*fleet.Fleet` with a registered agent named `otter` and a real clone at its workDir (so `Fleet.Fetch` succeeds). Put it in `fetch_test.go`; reuse the gitops `bareRepo`/clone pattern (a bare remote in a temp dir, `git clone` into `workDir("otter")`, and a fake `Up` with the agent label). If `internal/cli` tests already have a fleet-construction helper, use that instead and only add the clone+agent registration:

```go
func Fleet(t *testing.T, fake *backend.Fake) *fleet.Fleet {
	t.Helper()
	work := t.TempDir()
	f := &fleet.Fleet{Backend: fake, WorkRoot: work}
	// bare remote + clone into workDir("otter")
	bare := t.TempDir() + "/remote.git"
	mustRun(t, "", "git", "init", "-q", "-b", "main", "--bare", bare)
	seed := t.TempDir() + "/seed"
	mustRun(t, "", "git", "clone", "-q", bare, seed)
	mustRun(t, seed, "git", "config", "user.email", "s@example.com")
	mustRun(t, seed, "git", "config", "user.name", "s")
	mustRun(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "init")
	mustRun(t, seed, "git", "push", "-q", "origin", "main")
	mustRun(t, "", "git", "clone", "-q", bare, work+"/otter")
	if _, err := fake.Up(context.Background(), backend.UpOpts{Labels: map[string]string{backend.LabelAgent: "otter"}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
}
```

Add `"os/exec"` to the imports. (If `mustRun` already exists in the `cli` test package, drop this copy.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestFetchCmd -v`
Expected: FAIL — `undefined: fetchCmd`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/cli/fetch.go
package cli

import (
	"encoding/json"
	"fmt"

	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func fetchCmd(f *fleet.Fleet) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "fetch <agent>",
		Short: "Re-fetch origin into a running agent's clone (it has no git credentials)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := f.Fetch(cmd.Context(), name); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"agent": name, "fetched": true,
				})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Fetched origin for %s\n", name)
			return err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}
```

- [ ] **Step 4: Wire into the root command**

In `internal/cli/cli.go`, add `fetchCmd(f)` to the `root.AddCommand(...)` list (next to `submitCmd(f)`):

```go
root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), submitCmd(f), fetchCmd(f), logsCmd(f), daemonCmd(f), inboxCmd(f), agentsCmd(), doctorCmd())
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/fetch.go internal/cli/fetch_test.go internal/cli/cli.go
git commit -m "feat(cli): flotilla fetch <agent> (human + --json), daemon-independent"
```

---

### Task 4: Daemon `fetch` handler + `fleetAPI.Fetch` + `fetch_done` inbox event

**Files:**
- Modify: `internal/daemon/inbox.go` (add `EventFetchDone`)
- Modify: `internal/daemon/supervisor.go` (extend `fleetAPI`, add `fetchHandler`, register it)
- Modify: `internal/cli/daemon.go` is unchanged — the registry is created in `daemonRunCmd` and the supervisor registers handlers itself.
- Test: `internal/daemon/supervisor_test.go` (extend `fakeSubmitter` with `Fetch`, add handler tests)

**Interfaces:**
- Produces: `EventFetchDone = "fetch_done"`; `func (s *Supervisor) fetchHandler(ctx context.Context, agent string, req Request) Response`; `func (s *Supervisor) registerHandlers()` (called from `Run`). `fleetAPI` gains `Fetch(ctx context.Context, name string) error`.
- Consumes: `Fleet.Fetch` (Task 2) via the `fleetAPI` interface; the existing `s.emit`, `Registry.Register`, `dispatchRequests`.

- [ ] **Step 1: Add the inbox event constant**

In `internal/daemon/inbox.go`, extend the const block:

```go
const (
	EventAgentDone     = "agent_done"
	EventPROpened      = "pr_opened"
	EventPRUpdated     = "pr_updated"
	EventSubmitSkipped = "submit_skipped"
	EventFetchDone     = "fetch_done"
)
```

- [ ] **Step 2: Write the failing test**

Extend `fakeSubmitter` in `internal/daemon/supervisor_test.go` with fetch tracking, and add the handler tests:

```go
// add fields to fakeSubmitter:
//   fetches  []string
//   fetchErr map[string]error

func (f *fakeSubmitter) Fetch(_ context.Context, name string) error {
	f.fetches = append(f.fetches, name)
	if f.fetchErr != nil {
		return f.fetchErr[name]
	}
	return nil
}

func TestFetchHandlerFetchesAndNotifies(t *testing.T) {
	fs := &fakeSubmitter{}
	s := newSup(t, fs)
	s.Registry = NewRegistry()
	s.registerHandlers()

	resp := s.fetchHandler(context.Background(), "otter", Request{ID: "1", Type: "fetch"})
	if resp.Status != "ok" {
		t.Fatalf("want ok, got %+v", resp)
	}
	if len(fs.fetches) != 1 || fs.fetches[0] != "otter" {
		t.Fatalf("Fetch not called for otter: %v", fs.fetches)
	}
	types := eventTypes(mustRead(t, s.Paths.Inbox()))
	if !types[EventFetchDone] {
		t.Fatalf("missing fetch_done inbox event, got %v", types)
	}
}

func TestFetchHandlerSurfacesError(t *testing.T) {
	fs := &fakeSubmitter{fetchErr: map[string]error{"otter": errors.New(`no workspace clone for agent "otter"`)}}
	s := newSup(t, fs)
	resp := s.fetchHandler(context.Background(), "otter", Request{ID: "1", Type: "fetch"})
	if resp.Status != "error" || !strings.Contains(resp.Message, "no workspace clone") {
		t.Fatalf("want error response, got %+v", resp)
	}
	// Still notes the (failed) attempt in the inbox.
	if !eventTypes(mustRead(t, s.Paths.Inbox()))[EventFetchDone] {
		t.Fatalf("fetch_done should be emitted even on failure")
	}
}

// End-to-end through the seam: a request file in the agent's session dir gets a
// response written and triggers a fetch, via scanOnce's dispatch.
func TestScanOnceServicesFetchRequest(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-otter")
	if err := os.MkdirAll(filepath.Join(logDir, "requests"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "requests", "req1.json"),
		[]byte(`{"type":"fetch","id":"req1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSubmitter{}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter", Status: "running", LogDir: logDir}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}, Registry: NewRegistry(), Now: func() time.Time { return time.Unix(1, 0).UTC() }}
	s.registerHandlers()

	s.scanOnce(context.Background())

	if len(fs.fetches) != 1 {
		t.Fatalf("want exactly 1 fetch via the seam, got %d", len(fs.fetches))
	}
	rb, err := os.ReadFile(filepath.Join(logDir, "responses", "req1.json"))
	if err != nil {
		t.Fatalf("no response written: %v", err)
	}
	if !strings.Contains(string(rb), `"status":"ok"`) {
		t.Fatalf("response not ok: %s", rb)
	}
}
```

Add `"strings"`, `"os"`, `"path/filepath"`, `"time"` to the test imports if not already present (most already are).

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'Fetch|ScanOnceServices' -v`
Expected: FAIL — `fakeSubmitter` does not satisfy `fleetAPI` (missing `Fetch`) / `s.fetchHandler undefined` / `s.registerHandlers undefined`. (The `Fetch` method added in Step 2 makes the fake satisfy the new interface once the interface is extended in Step 4.)

- [ ] **Step 4: Write minimal implementation**

In `internal/daemon/supervisor.go`, extend the interface and add the handler + registration:

```go
// fleetAPI is the slice of *fleet.Fleet the supervisor reacts with.
type fleetAPI interface {
	Submit(ctx context.Context, name string, force bool) (fleet.Submission, error)
	HeadSHA(ctx context.Context, name string) (string, error)
	List(ctx context.Context) ([]fleet.Agent, error)
	Fetch(ctx context.Context, name string) error
}

// registerHandlers wires the supervisor's request handlers onto the §9 seam.
// No-op when there is no registry (the supervisor still does auto-submit).
func (s *Supervisor) registerHandlers() {
	if s.Registry == nil {
		return
	}
	s.Registry.Register("fetch", s.fetchHandler)
}

// fetchHandler services an agent-initiated fetch request: it re-fetches origin
// into that agent's engine-side clone (Fleet.Fetch) and notes the outcome in the
// inbox. The action is fixed — fetch that one agent's repo — never arbitrary exec.
func (s *Supervisor) fetchHandler(ctx context.Context, agent string, _ Request) Response {
	if err := s.Fleet.Fetch(ctx, agent); err != nil {
		s.emit(agent, EventFetchDone, "fetch failed: "+err.Error(), nil)
		return Response{Status: "error", Message: err.Error()}
	}
	s.emit(agent, EventFetchDone, "fetched origin", nil)
	// Terminal ok per spec §6: the dispatch loop writes {"status":"ok"} to
	// responses/<id>.json; the shim substring-matches "status":"ok".
	return Response{Status: "ok"}
}
```

Then call `registerHandlers` once at the start of `Run`, before the startup scan:

```go
func (s *Supervisor) Run(ctx context.Context, interval time.Duration) error {
	s.registerHandlers()
	s.scanOnce(ctx) // catch agents that finished while the daemon was down
	// ... unchanged ...
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -v`
Expected: PASS (existing supervisor tests still green; the fake now implements `Fetch`).

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/inbox.go internal/daemon/supervisor.go internal/daemon/supervisor_test.go
git commit -m "feat(daemon): fetch request handler (Fleet.Fetch + fetch_done inbox event)"
```

---

### Task 5: `flotilla-fetch` shim injected at spawn

**Files:**
- Create: `internal/fleet/fetchshim.go`
- Modify: `internal/fleet/fleet.go` (call `installFetchShim` in `Spawn`, after the agent-CLI install step)
- Test: `internal/fleet/fetchshim_test.go`

**Interfaces:**
- Produces: `const fetchShimPath = "/usr/local/bin/flotilla-fetch"`; `const fetchShim string` (the POSIX-sh script); `func installFetchShim(ctx context.Context, be backend.Backend, id string) error`.
- Consumes: `backend.Backend.CopyTo` and `backend.Backend.Exec`; the `containerSessionDir` constant is *referenced by the shim text only as the literal `/flotilla/session`* (the shim is a string, so keep them in sync — see the test).

- [ ] **Step 1: Write the failing test**

```go
// internal/fleet/fetchshim_test.go
package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// TestInstallFetchShimCopiesAndChmods asserts the shim is copied to the on-PATH
// path and made executable.
func TestInstallFetchShimCopiesAndChmods(t *testing.T) {
	fake := backend.NewFake()
	id, err := fake.Up(context.Background(), backend.UpOpts{Labels: map[string]string{backend.LabelAgent: "otter"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := installFetchShim(context.Background(), fake, id.ID); err != nil {
		t.Fatalf("installFetchShim: %v", err)
	}

	var copied *backend.CopyCall
	for i := range fake.CopyCalls {
		if fake.CopyCalls[i].DestPath == fetchShimPath {
			copied = &fake.CopyCalls[i]
			break
		}
	}
	if copied == nil {
		t.Fatalf("shim not copied to %s; copies=%v", fetchShimPath, fake.CopyCalls)
	}
	if !strings.Contains(string(copied.Content), "flotilla-fetch") {
		t.Errorf("shim content missing marker; got %q", copied.Content)
	}

	var chmodded bool
	for _, c := range fake.ExecCalls {
		if len(c) >= 3 && c[1] == "chmod" && c[len(c)-1] == fetchShimPath {
			chmodded = true
		}
	}
	if !chmodded {
		t.Errorf("shim not chmod'd executable; execs=%v", fake.ExecCalls)
	}
}

// TestFetchShimTargetsSessionMount guards the shim's hard-coded /flotilla/session
// against drift from containerSessionDir.
func TestFetchShimTargetsSessionMount(t *testing.T) {
	if !strings.Contains(fetchShim, containerSessionDir) {
		t.Fatalf("shim must reference the session mount %q", containerSessionDir)
	}
}

// TestSpawnInstallsFetchShim proves Spawn wires installFetchShim in.
func TestSpawnInstallsFetchShim(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	prof := agentProfileStub() // any minimal profile; reuse the stub from wrapup_test if present
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do it"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	var found bool
	for _, cp := range fake.CopyCalls {
		if cp.DestPath == fetchShimPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("Spawn did not install the fetch shim")
	}
}
```

`agentProfileStub()` — use the same minimal profile shape the existing wrapup test uses (`agent.Profile{Name: "stub", Launch: ` + "`echo \"{prompt}\"`" + `}`), or `agent.Builtins()["claude"]`. If a helper already exists in the fleet test package, use it; otherwise inline the profile literal.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run 'FetchShim|InstallFetchShim' -v`
Expected: FAIL — `undefined: installFetchShim` / `fetchShimPath` / `fetchShim`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/fleet/fetchshim.go
package fleet

import (
	"context"
	"os"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// fetchShimPath is where the in-container `flotilla-fetch` command lands. It is
// on the default PATH, so no launch-wrapper change is needed.
const fetchShimPath = "/usr/local/bin/flotilla-fetch"

// fetchShim is the POSIX-sh `flotilla-fetch` command. The credential-less agent
// runs it to ask the engine (via the daemon's request channel on the session
// mount) to fetch origin into its clone, then integrates locally. It writes the
// request atomically (tmp + mv) and blocks until the daemon writes the response,
// capped so it can't hang forever if the daemon is down. The /flotilla/session
// path MUST match containerSessionDir (guarded by a test).
const fetchShim = `#!/bin/sh
# Ask the engine to fetch origin into this agent's clone (we have no git creds).
set -e
sess=/flotilla/session
id="$(date +%s%N)-$$"
mkdir -p "$sess/requests" "$sess/responses"
printf '{"type":"fetch","id":"%s"}' "$id" > "$sess/requests/.$id.tmp"
mv "$sess/requests/.$id.tmp" "$sess/requests/$id.json"
i=0
while [ ! -f "$sess/responses/$id.json" ]; do
  i=$((i+1)); [ "$i" -gt 120 ] && { echo "flotilla-fetch: timed out (is the daemon running?)" >&2; exit 1; }
  sleep 1
done
resp="$(cat "$sess/responses/$id.json")"
case "$resp" in
  *'"status":"ok"'*) echo "flotilla-fetch: origin fetched"; exit 0 ;;
  *) echo "flotilla-fetch: $resp" >&2; exit 1 ;;
esac
`

// installFetchShim writes the shim to a host temp file, copies it into the
// container at fetchShimPath, and marks it executable. Done as part of the
// root-capable install step (CopyTo preserves nothing we depend on; chmod makes
// it runnable by the agent regardless of the copied uid).
func installFetchShim(ctx context.Context, be backend.Backend, id string) error {
	tmp, err := os.CreateTemp("", "flotilla-fetch-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(fetchShim); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := be.CopyTo(ctx, id, tmp.Name(), fetchShimPath); err != nil {
		return err
	}
	return be.Exec(ctx, id, []string{"chmod", "0755", fetchShimPath})
}
```

- [ ] **Step 4: Wire into `Spawn`**

In `internal/fleet/fleet.go`, after step 3 (the `prof.Install` agent-CLI install) and before the egress-firewall step, add:

```go
	// 3.1) Install the flotilla-fetch shim (root step, on PATH): lets the
	// credential-less agent ask the engine to fetch origin via the daemon's
	// request channel. Independent of the agent profile.
	if err := installFetchShim(ctx, f.Backend, id); err != nil {
		return fail(fmt.Errorf("install fetch shim: %w", err))
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/fleet/ -v`
Expected: PASS (new shim tests + existing spawn tests).

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/fetchshim.go internal/fleet/fetchshim_test.go internal/fleet/fleet.go
git commit -m "feat(fleet): inject flotilla-fetch shim at spawn (on-PATH, session-mount channel)"
```

---

### Task 6: Fetch-awareness prompt preamble

**Files:**
- Modify: `internal/agent/wrapup.go` (add `FetchHint` const + `PromptWithFetchHint`)
- Modify: `internal/fleet/fleet.go` (compose the fetch hint into the injected prompt)
- Modify: `internal/fleet/wrapup_test.go` (update `TestSpawnDisabledWrapUpOmitsContract` for the now-always-present fetch hint)
- Test: `internal/agent/wrapup_test.go` (unit-test `PromptWithFetchHint`)

**Interfaces:**
- Produces: `const agent.FetchHint string`; `func agent.PromptWithFetchHint(prompt string) string` — appends a delimited `[Flotilla on-demand fetch]` block.
- Consumes: existing `agent.PromptWithWrapUp`. Spawn composes: `PromptWithFetchHint(PromptWithWrapUp(prompt, prof.WrapUpText()))`.

- [ ] **Step 1: Write the failing unit test**

```go
// internal/agent/wrapup_test.go — add:
func TestPromptWithFetchHintAppendsBlock(t *testing.T) {
	got := PromptWithFetchHint("do the task")
	if !strings.Contains(got, "do the task") {
		t.Errorf("user prompt dropped: %q", got)
	}
	if !strings.Contains(got, "flotilla-fetch") {
		t.Errorf("fetch hint missing the command name: %q", got)
	}
	if !strings.Contains(got, "[Flotilla on-demand fetch]") {
		t.Errorf("fetch hint block marker missing: %q", got)
	}
}
```

(Ensure `"strings"` and `"testing"` are imported in that test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run PromptWithFetchHint -v`
Expected: FAIL — `undefined: PromptWithFetchHint`.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/wrapup.go`, add:

```go
// FetchHint tells the credential-less agent how to pull in upstream base-branch
// changes mid-session. It is a constant preamble, not per-agent code.
const FetchHint = "You have no git credentials inside this container. To pull in " +
	"the latest changes from the base branch, run `flotilla-fetch` — the engine " +
	"fetches `origin` for you — then integrate locally with `git merge origin/<base>` " +
	"or `git rebase origin/<base>` as you see fit."

// PromptWithFetchHint appends the on-demand-fetch awareness note to the prompt as
// a clearly delimited block, mirroring PromptWithWrapUp's format.
func PromptWithFetchHint(prompt string) string {
	return prompt + "\n\n---\n[Flotilla on-demand fetch]\n" + FetchHint + "\n"
}
```

- [ ] **Step 4: Compose it into Spawn**

In `internal/fleet/fleet.go`, change the prompt injection from:

```go
	if err := inj.WriteFile(ctx, []byte(agent.PromptWithWrapUp(prompt, prof.WrapUpText())), agentPromptFile(home)); err != nil {
```

to:

```go
	fullPrompt := agent.PromptWithFetchHint(agent.PromptWithWrapUp(prompt, prof.WrapUpText()))
	if err := inj.WriteFile(ctx, []byte(fullPrompt), agentPromptFile(home)); err != nil {
```

- [ ] **Step 5: Update the existing disabled-wrap-up spawn test**

The fetch hint is now always appended, so `TestSpawnDisabledWrapUpOmitsContract` in `internal/fleet/wrapup_test.go` can no longer assert the prompt equals `"do the task"` exactly. Update its body to assert the wrap-up contract is absent but the user prompt and fetch hint are present:

```go
	for _, cp := range fake.CopyCalls {
		if strings.HasSuffix(cp.DestPath, ".flotilla/prompt") {
			content := string(cp.Content)
			if strings.Contains(content, "Flotilla submission contract") {
				t.Errorf("wrap-up contract present despite '-' sentinel; got: %q", content)
			}
			if !strings.Contains(content, "do the task") {
				t.Errorf("user prompt dropped; got: %q", content)
			}
			if !strings.Contains(content, "flotilla-fetch") {
				t.Errorf("fetch hint should always be present; got: %q", content)
			}
			return
		}
	}
	t.Fatal("no CopyCall for the agent prompt file")
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agent/ ./internal/fleet/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/wrapup.go internal/agent/wrapup_test.go internal/fleet/fleet.go internal/fleet/wrapup_test.go
git commit -m "feat(agent): inject flotilla-fetch awareness into every agent prompt"
```

---

### Task 7: Shim script-level behaviour test

**Files:**
- Test: `internal/fleet/fetchshim_script_test.go`

**Interfaces:**
- Consumes: the `fetchShim` constant (Task 5). Runs the shim with `sh` against a fake session dir, overriding the hard-coded `/flotilla/session` path so the test is host-local.

The shim hard-codes `sess=/flotilla/session`. To exercise it host-side without that absolute path, run the script through `sh -c` with the `sess=` line rewritten to a temp dir. Keep this robust by replacing the literal assignment.

- [ ] **Step 1: Write the test**

```go
// internal/fleet/fetchshim_script_test.go
package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runShim runs the fetch shim with its session dir pointed at sessDir (by
// rewriting the hard-coded sess= assignment) and returns combined output + err.
func runShim(t *testing.T, sessDir string) (string, error) {
	t.Helper()
	script := strings.Replace(fetchShim, "sess=/flotilla/session", "sess="+sessDir, 1)
	cmd := exec.Command("sh", "-c", script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestShimSucceedsOnOkResponse: a pre-seeded ok response makes the shim exit 0.
// It must first write a request file atomically (we observe the resulting file).
func TestShimSucceedsOnOkResponse(t *testing.T) {
	sess := t.TempDir()
	// Pre-create responses with an answer for whatever id the shim picks: we
	// can't know the id ahead of time, so run a watcher goroutine that mirrors
	// any request into an ok response.
	done := mirrorRequestsToOk(t, sess)
	defer close(done)

	out, err := runShim(t, sess)
	if err != nil {
		t.Fatalf("shim should succeed, got err=%v out=%q", err, out)
	}
	if !strings.Contains(out, "origin fetched") {
		t.Fatalf("want success message, got %q", out)
	}
}

// TestShimReportsErrorResponse: an error response makes the shim exit non-zero
// and print the payload.
func TestShimReportsErrorResponse(t *testing.T) {
	sess := t.TempDir()
	done := mirrorRequestsTo(t, sess, `{"status":"error","message":"boom"}`)
	defer close(done)

	out, err := runShim(t, sess)
	if err == nil {
		t.Fatalf("shim should fail on error response; out=%q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("want error payload echoed, got %q", out)
	}
}

// mirrorRequestsToOk answers every appearing request with a status:ok response.
func mirrorRequestsToOk(t *testing.T, sess string) chan struct{} {
	return mirrorRequestsTo(t, sess, `{"status":"ok"}`)
}

// mirrorRequestsTo polls sess/requests and writes resp into sess/responses for
// each request id until the returned channel is closed.
func mirrorRequestsTo(t *testing.T, sess, resp string) chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		reqDir := filepath.Join(sess, "requests")
		respDir := filepath.Join(sess, "responses")
		for {
			select {
			case <-done:
				return
			default:
			}
			entries, _ := os.ReadDir(reqDir)
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".json") {
					continue // skip the .tmp atomic-write staging file
				}
				id := strings.TrimSuffix(e.Name(), ".json")
				_ = os.MkdirAll(respDir, 0o777)
				_ = os.WriteFile(filepath.Join(respDir, id+".json"), []byte(resp), 0o644)
			}
		}
	}()
	return done
}
```

- [ ] **Step 2: Run to verify it passes**

Run: `go test ./internal/fleet/ -run Shim -v`
Expected: PASS. (The shim's 1s poll loop means each test takes ~1s; acceptable. If the responder mirrors the request within the first second, the shim returns promptly.)

- [ ] **Step 3: Commit**

```bash
git add internal/fleet/fetchshim_script_test.go
git commit -m "test(fleet): script-level flotilla-fetch shim ok/error paths"
```

---

### Task 8: Live Docker integration test (self-skipping)

**Files:**
- Test: add to the existing self-skipping Docker integration path. Prefer `internal/backend` if that is where the live `devcontainer up` test lives; otherwise a new `internal/fleet/fetch_integration_test.go` guarded by the same Docker-availability check used elsewhere.

**Interfaces:**
- Consumes: the full `Fleet.Spawn` + daemon `fetchHandler` path. Mirrors the existing live-Docker test's skip guard (find it: `grep -rn "Skip" internal/backend internal/fleet | grep -i docker`).

- [ ] **Step 1: Write the self-skipping test**

Model it on the existing live integration test's structure (same skip guard). The assertion: spawn a real agent against a real (local bare) repo, advance the remote, run the agent's `flotilla-fetch` (via `docker exec` of the shim, or by driving `Fleet.Fetch`), and assert `origin/<base>` advanced inside the container (`git -C <workspace> rev-parse refs/remotes/origin/<base>` changed). Because this requires Docker + the devcontainer CLI, gate it behind the same availability check and `t.Skip` when unavailable.

```go
//go:build integration_or_existing_tag

// Use whatever build tag / runtime skip the existing live test uses — do not
// invent a new mechanism. The body:
//   1. start a bare local remote with one commit on main
//   2. Fleet.Spawn an agent on it
//   3. commit+push a new commit to the bare remote (engine-side)
//   4. Fleet.Fetch(name)  (path b — exercises the same primitive the daemon uses)
//   5. docker exec: git -C <workspaceFolder> rev-parse origin/main == new SHA
```

- [ ] **Step 2: Run it (skips without Docker, runs in CI)**

Run: `go test ./... ` (skips locally without Docker) and `go test -race ./...` in CI.
Expected: PASS or SKIP locally; PASS in the Docker CI job.

- [ ] **Step 3: Commit**

```bash
git add internal/<pkg>/fetch_integration_test.go
git commit -m "test: live Docker round-trip — engine fetch lands inside the container"
```

---

### Task 9: Docs — backlog, spec status, README

**Files:**
- Modify: `docs/backlog.md` (move "On-demand fetch/pull" from "Next plans" to done, with links)
- Modify: `docs/specs/2026-06-23-flotilla-on-demand-fetch-design.md` (Status: Draft → Implemented; link the plan)
- Modify: `README.md` (`## Status` / command list: add `flotilla fetch` and the `flotilla-fetch` shim)

- [ ] **Step 1: Update the backlog**

In `docs/backlog.md`, strike through item 1 ("On-demand fetch/pull") and mark it Done with links to the spec and this plan, matching the format used for the daemon/submission entries above it. Renumber the remaining "Next plans" items (agent question channel becomes #1, etc.).

- [ ] **Step 2: Flip the spec status**

In `docs/specs/2026-06-23-flotilla-on-demand-fetch-design.md`, change `**Status:** Draft for review` to `**Status:** Implemented (2026-06-24)` and add a line linking to `docs/plans/2026-06-24-flotilla-on-demand-fetch.md`.

- [ ] **Step 3: Update README**

Add `flotilla fetch <agent>` to the command list and a sentence under `## Status` noting on-demand fetch (the credential-less agent runs `flotilla-fetch`; the engine fetches origin; the operator can also `flotilla fetch <agent>`).

- [ ] **Step 4: Commit**

```bash
git add docs/backlog.md docs/specs/2026-06-23-flotilla-on-demand-fetch-design.md README.md
git commit -m "docs: on-demand fetch (flotilla fetch + flotilla-fetch shim) shipped"
```

---

## Final verification

- [ ] `go build ./...` — clean.
- [ ] `go test ./...` — all green (live Docker test self-skips).
- [ ] `golangci-lint run ./...` and `golangci-lint fmt --diff` — clean (run `golangci-lint fmt` / `--fix` if needed).
- [ ] `hk run check` — full pre-commit suite green.
- [ ] Manual smoke (if Docker available): `flotilla spawn <repo>`, then from inside the container `flotilla-fetch`, and `flotilla inbox` shows a `fetch_done` event; `flotilla fetch <agent>` from the host prints `Fetched origin for <agent>`.

## Self-review notes (spec coverage)

- Spec §4 `gitops.Fetch` → Task 1. §5 `Fleet.Fetch` + `flotilla fetch` → Tasks 2–3. §6 daemon `fetch` handler + `fetch_done` inbox → Task 4. §7 shim + prompt preamble → Tasks 5–6. §9 concurrency/ownership — covered by surfacing git stderr verbatim (no masking) in `gitops.git`; no extra code. §10 trust boundary — preserved structurally: the handler receives the agent name from the supervisor's per-agent dispatch and acts only on that agent's clone (Task 4). §11 error handling — Tasks 2 (unknown agent / missing clone), 4 (error response + inbox note), 7 (shim timeout/error). §12 testing — Tasks 1–8. §13 sequencing — prerequisites (logs mounts, daemon) already shipped.
