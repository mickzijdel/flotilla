# Flotilla Daemon (supervisor) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional, long-running `flotilla daemon` that watches agents and reacts to events — its anchor feature being **auto-submit a PR when an agent finishes** — plus an operator **inbox**, a **state mirror**, and the **request-handler seam** that on-demand fetch and the future question channel plug into.

**Architecture:** Additive supervisor (Option A): the CLI is untouched and keeps talking directly to Docker + `~/.flotilla`; the daemon is a *second consumer* of the same substrates that reacts to events. It is built over the **same `fleet.Fleet`** the CLI constructs in `main.go`. The done-signal is the logs-spec `status` file flipping to `done` (a secondary Docker `die`/`stop` event covers crashes). Detection is by **polling** (a periodic scan), matching the existing `flotilla logs -f` 200 ms poll loop — no new `fsnotify` dependency. Single-instance via `syscall.Flock`; self-daemonizing by re-spawning `flotilla daemon run` detached.

**Tech Stack:** Go 1.26, cobra, `os/exec` shelling to `docker`/`git`, `syscall` (Flock, Setsid, Exec, Signal), the existing in-memory `backend.Fake` for tests.

**Design spec:** [docs/specs/2026-06-23-flotilla-daemon-design.md](../specs/2026-06-23-flotilla-daemon-design.md)

## Design notes / deviations from the spec

- **Polling, not `fsnotify` (§5, §9, §10).** The spec says "host-side `fsnotify`". This plan uses a periodic scan instead, for three reasons: (1) the repo already polls for the same class of signal in `flotilla logs -f` (`internal/cli/logs.go`, 200 ms loop); (2) it avoids a new third-party dependency, which the project minimises (`go.mod` has only cobra + toml); (3) the daemon already needs a periodic tick for the re-exec self-check and SHA dedup. A 2 s scan latency on "agent done → PR" is irrelevant for a background supervisor. The watch is still a well-defined seam; swapping in `fsnotify` later is a drop-in change behind `Supervisor.scanOnce`.
- **Agent enumeration via `Fleet.List` + the `flotilla.logdir` label, not a blind tree walk.** `List` is the authoritative agent set and already carries `LogDir` (see `internal/fleet/fleet.go:218`), so the supervisor reads `<LogDir>/status` per agent and never has to parse the agent name back out of a session-dir name. The exec-into-idle container stays `running` after the agent exits, so a finished agent is always still in `List`.
- **Dedup is persisted-record-driven (no separate in-memory set).** Each scan loads the per-agent record; `HEAD == LastHandledSHA` ⇒ skip silently. This is correct across daemon restarts (covers §13 "agent finished while daemon was down" and "crash mid-submit") without re-emitting events every tick.

## Global Constraints

- **Go:** 1.26.4 (`go` directive in go.mod). cobra v1.10.2, BurntSushi/toml v1.6.0. **No new third-party dependencies** — `syscall` (stdlib) only for flock/daemonize.
- **Git invocation:** `os/exec` only, via the existing unexported `gitops.git` helper; every call already injects `-C dir -c safe.directory=dir`. Read-only plumbing for inspection (never `git status`).
- **Docker invocation:** via the existing `backend.run` / streaming `exec.CommandContext` pattern in `internal/backend/docker.go`.
- **All daemon state lives under `~/.flotilla`** (user-owned). Paths: `daemon.pid`, `daemon.lock`, `daemon.log`, `inbox.jsonl`, `daemon/version`, `daemon/agents/<name>.json`. Atomic writes (temp + rename) for the state mirror.
- **Auto-submit reuses `Fleet.Submit(ctx, name, force=true)` verbatim** — no duplicated push/PR logic. `force` bypasses only the container-status gate; the strict dirty/zero-commit checks still apply.
- **Branch pushed is always `flotilla/<agent>`** with `--force-with-lease` (inherited from `Submit`); the daemon never force-commits and never touches a base branch.
- **Test style:** Docker-free where possible — `backend.Fake` + `forge.Fake` + a real local git "remote" in `t.TempDir()` (see `internal/gitops/inspect_test.go`'s `cloneWithCommits` / `mustRun` helpers and `internal/fleet/submit_test.go`'s `fakeForge`). External-tool paths (`docker events`) self-skip when the tool is absent, like the existing backend integration test.
- **Every command stays `--json`-capable** for the future VS Code extension.
- **Commit after each task** (TDD: failing test → minimal implementation → green → commit). Conventional-commit messages matching the repo's history (`feat(daemon): …`, `feat(backend): …`, `feat(cli): …`).

## File structure

New package `internal/daemon`:
- `paths.go` — `Paths` value type: resolves all `~/.flotilla` daemon file locations.
- `inbox.go` — `InboxEvent`, `AppendEvent`, `ReadEvents` (append-only JSONL).
- `state.go` — `AgentRecord` + atomic read/write; `WriteVersion`/`ReadVersion`; binary stamp.
- `requests.go` — `Request`/`Response` envelope, `Registry`, `dispatchRequests` scan.
- `supervisor.go` — `Supervisor` struct, `handle`, `scanOnce`, `drainEvents`, `Run`.
- `lifecycle.go` — flock, pidfile, `IsRunning`, `RunForeground`, `EnsureRunning`, `Stop`, `Status`, `shouldReexec`.
- per-file `_test.go`.

Modified:
- `internal/backend/backend.go` — `Event` type + `Events` interface method.
- `internal/backend/docker.go` — `Events` impl (`docker events`).
- `internal/backend/fake.go` — pushable event channel.
- `internal/gitops/head.go` (new) — `HeadSHA`.
- `internal/fleet/fleet.go` — exported `LogsDir()` and `HeadSHA(ctx, name)` accessors.
- `internal/cli/daemon.go` (new) — `flotilla daemon start|stop|status|run`.
- `internal/cli/inbox.go` (new) — `flotilla inbox`.
- `internal/cli/cli.go` — register `daemonCmd`/`inboxCmd`; `spawn` best-effort auto-start; `doctor` advisory.
- `main.go` — pass the resolved `~/.flotilla` root into the daemon wiring (via a `cli` field already on `Fleet`).

---

### Task 1: `gitops.HeadSHA` — resolve a clone's current HEAD

**Files:**
- Create: `internal/gitops/head.go`
- Create: `internal/gitops/head_test.go`

**Interfaces:**
- Consumes: existing unexported `git(ctx, dir, args...)` in `internal/gitops/inspect.go`.
- Produces: `func HeadSHA(ctx context.Context, dir string) (string, error)` — full 40-char SHA of `HEAD`.

- [x] **Step 1: Write the failing test**

```go
// internal/gitops/head_test.go
package gitops

import (
	"context"
	"testing"
)

func TestHeadSHA(t *testing.T) {
	dir := cloneWithCommits(t, 2) // helper from inspect_test.go: bare remote + clone + N commits
	sha, err := HeadSHA(context.Background(), dir)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("want 40-char sha, got %q (len %d)", sha, len(sha))
	}
	// Stable across calls.
	sha2, _ := HeadSHA(context.Background(), dir)
	if sha != sha2 {
		t.Fatalf("non-deterministic: %q vs %q", sha, sha2)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gitops/ -run TestHeadSHA -v`
Expected: FAIL — `undefined: HeadSHA`.

- [x] **Step 3: Write minimal implementation**

```go
// internal/gitops/head.go
package gitops

import "context"

// HeadSHA returns the full commit SHA of HEAD in dir. Read-only.
func HeadSHA(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "rev-parse", "HEAD")
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gitops/ -run TestHeadSHA -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/gitops/head.go internal/gitops/head_test.go
git commit -m "feat(gitops): HeadSHA resolves a clone's current HEAD"
```

---

### Task 2: `Backend.Events` seam (type + Fake)

The secondary done-trigger (§10): a stream of container lifecycle events. This task adds the type, the interface method, and the **Fake** implementation (a pushable channel) so the supervisor is testable with no Docker. The Docker impl is Task 3.

**Files:**
- Modify: `internal/backend/backend.go` (add `Event` + interface method)
- Modify: `internal/backend/fake.go` (pushable channel)
- Test: `internal/backend/fake_test.go` (append a test)

**Interfaces:**
- Produces:
  - `type Event struct { Type string; ID string; Labels map[string]string }`
  - `Events(ctx context.Context) (<-chan Event, error)` on `Backend`.
  - On `*Fake`: `func (f *Fake) PushEvent(e Event)` — sends to the channel returned by `Events`.

- [x] **Step 1: Write the failing test**

```go
// internal/backend/fake_test.go  (add to the existing file)
func TestFakeEvents(t *testing.T) {
	f := NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := f.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	f.PushEvent(Event{Type: "die", ID: "fake-1", Labels: map[string]string{LabelAgent: "brave-otter"}})
	select {
	case e := <-ch:
		if e.Type != "die" || e.Labels[LabelAgent] != "brave-otter" {
			t.Fatalf("unexpected event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should close on ctx cancel")
	}
}
```

(Ensure `context` and `time` are imported in `fake_test.go`.)

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/ -run TestFakeEvents -v`
Expected: FAIL — `f.Events undefined` / `PushEvent undefined`.

- [x] **Step 3: Add the type + interface method**

In `internal/backend/backend.go`, add after the `Container` type:

```go
// Event is a container lifecycle event for a flotilla-labelled container.
type Event struct {
	Type   string // "die" | "stop" | "start" (docker action)
	ID     string
	Labels map[string]string
}
```

And add to the `Backend` interface (after `ContainerNetworks`):

```go
	// Events streams container lifecycle events for flotilla-labelled containers
	// until ctx is cancelled. The channel closes on ctx.Done or a fatal stream error.
	Events(ctx context.Context) (<-chan Event, error)
```

- [x] **Step 4: Implement on Fake**

In `internal/backend/fake.go`, add a field to the `Fake` struct:

```go
	events chan Event // lazily created by Events; PushEvent sends here
```

And methods:

```go
// Events returns a channel of pushed events, closed when ctx is cancelled.
func (f *Fake) Events(ctx context.Context) (<-chan Event, error) {
	f.mu.Lock()
	if f.events == nil {
		f.events = make(chan Event, 16)
	}
	ch := f.events
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		if f.events != nil {
			close(f.events)
			f.events = nil
		}
		f.mu.Unlock()
	}()
	return ch, nil
}

// PushEvent delivers an event to a live Events channel (no-op if none).
func (f *Fake) PushEvent(e Event) {
	f.mu.Lock()
	ch := f.events
	f.mu.Unlock()
	if ch != nil {
		ch <- e
	}
}
```

- [x] **Step 5: Run test to verify it passes**

Run: `go test ./internal/backend/ -run TestFakeEvents -v`
Expected: PASS.

- [x] **Step 6: Verify the whole backend package still compiles & passes**

Run: `go test ./internal/backend/ 2>&1 | tail -5`
Expected: PASS (the new interface method is satisfied by Fake; Docker impl lands in Task 3 — if `go build ./...` complains that `dockerBackend` doesn't implement `Backend`, that is expected and resolved in Task 3, so run only the backend *test* here).

- [x] **Step 7: Commit**

```bash
git add internal/backend/backend.go internal/backend/fake.go internal/backend/fake_test.go
git commit -m "feat(backend): Event type + Backend.Events seam with pushable Fake channel"
```

---

### Task 3: `Backend.Events` Docker implementation

**Files:**
- Modify: `internal/backend/docker.go`
- Test: `internal/backend/docker_test.go` (self-skipping integration test)

**Interfaces:**
- Consumes: `Event` (Task 2), the existing `parseLabels` helper.
- Produces: `func (d *dockerBackend) Events(ctx context.Context) (<-chan Event, error)`.

- [x] **Step 1: Write the failing (self-skipping) test**

```go
// internal/backend/docker_test.go  (add)
func TestDockerEventsDecodes(t *testing.T) {
	if !dockerAvailable(t) { // existing helper used by the integration test
		t.Skip("docker not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := &dockerBackend{}
	ch, err := d.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	// We can't easily trigger a flotilla-labelled container here; assert the
	// stream is established and closes cleanly on ctx timeout.
	for range ch { //nolint:revive // drain until close
	}
}
```

If the existing test file has no `dockerAvailable` helper, reuse whatever guard the existing integration test uses (grep `t.Skip` in `internal/backend/docker_test.go`) and mirror it.

- [x] **Step 2: Run test to verify it fails / skips**

Run: `go test ./internal/backend/ -run TestDockerEventsDecodes -v`
Expected: FAIL to compile — `d.Events undefined` (or SKIP once implemented if no Docker).

- [x] **Step 3: Implement Events**

```go
// internal/backend/docker.go  (add imports: "bufio", "os/exec")
// dockerEventLine is the subset of `docker events --format '{{json .}}'` we read.
type dockerEventLine struct {
	Status string            `json:"status"` // "die" | "stop" | "start" | ...
	ID     string            `json:"id"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"` // includes labels + "name"
	} `json:"Actor"`
}

func (d *dockerBackend) Events(ctx context.Context) (<-chan Event, error) {
	cmd := exec.CommandContext(ctx, "docker", "events",
		"--format", "{{json .}}",
		"--filter", "label="+LabelAgent,
		"--filter", "event=die",
		"--filter", "event=stop",
		"--filter", "event=start",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		defer func() { _ = cmd.Wait() }()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			var l dockerEventLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				continue
			}
			ev := Event{Type: l.Status, ID: l.ID, Labels: map[string]string{}}
			for k, v := range l.Actor.Attributes {
				ev.Labels[k] = v
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
```

(`docker events` puts container labels into `Actor.Attributes`, so `LabelAgent` is read directly from there — no extra `inspect` call.)

- [x] **Step 4: Run test to verify it passes/skips**

Run: `go test ./internal/backend/ -run TestDockerEventsDecodes -v`
Expected: PASS or SKIP (no Docker). Also `go build ./...` now succeeds (interface fully satisfied).

- [x] **Step 5: Commit**

```bash
git add internal/backend/docker.go internal/backend/docker_test.go
git commit -m "feat(backend): docker events stream for Backend.Events"
```

---

### Task 4: Daemon paths

**Files:**
- Create: `internal/daemon/paths.go`
- Create: `internal/daemon/paths_test.go`

**Interfaces:**
- Produces:
  - `type Paths struct { Root string }` (Root = `~/.flotilla`)
  - `func DefaultPaths() Paths` (Root from `os.UserHomeDir()` + `.flotilla`)
  - Methods (all return absolute paths under Root): `Pid()`, `Lock()`, `Log()`, `Inbox()`, `StateDir()`, `Version()`, `AgentsDir()`, `AgentRecord(name string)`, `LogsRoot()`.

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/paths_test.go
package daemon

import (
	"path/filepath"
	"testing"
)

func TestPaths(t *testing.T) {
	p := Paths{Root: "/home/u/.flotilla"}
	cases := map[string]string{
		p.Pid():                 "/home/u/.flotilla/daemon.pid",
		p.Lock():                "/home/u/.flotilla/daemon.lock",
		p.Log():                 "/home/u/.flotilla/daemon.log",
		p.Inbox():               "/home/u/.flotilla/inbox.jsonl",
		p.StateDir():            "/home/u/.flotilla/daemon",
		p.Version():             "/home/u/.flotilla/daemon/version",
		p.AgentsDir():           "/home/u/.flotilla/daemon/agents",
		p.AgentRecord("otter"):  "/home/u/.flotilla/daemon/agents/otter.json",
		p.LogsRoot():            "/home/u/.flotilla/logs",
	}
	for got, want := range cases {
		if filepath.Clean(got) != filepath.Clean(want) {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestPaths -v`
Expected: FAIL — package/symbols undefined.

- [x] **Step 3: Write implementation**

```go
// internal/daemon/paths.go
package daemon

import (
	"os"
	"path/filepath"
)

// Paths resolves every daemon file under the ~/.flotilla root.
type Paths struct{ Root string }

// DefaultPaths roots the daemon under ~/.flotilla (or "." if home is unknown).
func DefaultPaths() Paths {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return Paths{Root: filepath.Join(home, ".flotilla")}
}

func (p Paths) Pid() string       { return filepath.Join(p.Root, "daemon.pid") }
func (p Paths) Lock() string      { return filepath.Join(p.Root, "daemon.lock") }
func (p Paths) Log() string       { return filepath.Join(p.Root, "daemon.log") }
func (p Paths) Inbox() string     { return filepath.Join(p.Root, "inbox.jsonl") }
func (p Paths) StateDir() string  { return filepath.Join(p.Root, "daemon") }
func (p Paths) Version() string   { return filepath.Join(p.StateDir(), "version") }
func (p Paths) AgentsDir() string { return filepath.Join(p.StateDir(), "agents") }
func (p Paths) AgentRecord(name string) string {
	return filepath.Join(p.AgentsDir(), name+".json")
}
func (p Paths) LogsRoot() string { return filepath.Join(p.Root, "logs") }
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestPaths -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/daemon/paths.go internal/daemon/paths_test.go
git commit -m "feat(daemon): Paths resolves ~/.flotilla daemon file locations"
```

---

### Task 5: Inbox (append-only JSONL)

**Files:**
- Create: `internal/daemon/inbox.go`
- Create: `internal/daemon/inbox_test.go`

**Interfaces:**
- Produces:
  - `type InboxEvent struct { TS time.Time; Agent string; Type string; Message string; Data map[string]any }` with JSON tags `ts,agent,type,message,data` (`data` omitempty).
  - Event-type constants: `EventAgentDone="agent_done"`, `EventPROpened="pr_opened"`, `EventPRUpdated="pr_updated"`, `EventSubmitSkipped="submit_skipped"`.
  - `func AppendEvent(path string, e InboxEvent) error` — create-on-first-write, append a JSON line, `0600`. Uses `O_APPEND` so concurrent writers don't interleave (single `Write` of one line < PIPE_BUF).
  - `func ReadEvents(path string, since time.Time) ([]InboxEvent, error)` — parse all lines; if `since` is non-zero, keep only `TS.After(since)`; a missing file returns `(nil, nil)`.

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/inbox_test.go
package daemon

import (
	"path/filepath"
	"testing"
	"time"
)

func TestInboxAppendAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.jsonl")
	t0 := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC)
	must := func(e InboxEvent) {
		if err := AppendEvent(path, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	must(InboxEvent{TS: t0, Agent: "a", Type: EventAgentDone, Message: "done"})
	must(InboxEvent{TS: t0.Add(time.Minute), Agent: "a", Type: EventPROpened, Message: "opened", Data: map[string]any{"prURL": "u"}})

	all, err := ReadEvents(path, time.Time{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 events, got %d", len(all))
	}
	if all[1].Type != EventPROpened || all[1].Data["prURL"] != "u" {
		t.Fatalf("bad second event: %+v", all[1])
	}

	since, err := ReadEvents(path, t0)
	if err != nil {
		t.Fatalf("read since: %v", err)
	}
	if len(since) != 1 || since[0].Type != EventPROpened {
		t.Fatalf("since filter: got %+v", since)
	}
}

func TestReadEventsMissingFile(t *testing.T) {
	got, err := ReadEvents(filepath.Join(t.TempDir(), "nope.jsonl"), time.Time{})
	if err != nil || got != nil {
		t.Fatalf("want (nil,nil), got (%v,%v)", got, err)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestInbox -v`
Expected: FAIL — symbols undefined.

- [x] **Step 3: Write implementation**

```go
// internal/daemon/inbox.go
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Inbox event types (open set; later handlers add their own).
const (
	EventAgentDone     = "agent_done"
	EventPROpened      = "pr_opened"
	EventPRUpdated     = "pr_updated"
	EventSubmitSkipped = "submit_skipped"
)

// InboxEvent is one operator-facing notable event.
type InboxEvent struct {
	TS      time.Time      `json:"ts"`
	Agent   string         `json:"agent"`
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// AppendEvent appends e as one JSON line to path (created 0600 on first write).
func AppendEvent(path string, e InboxEvent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(line, '\n'))
	return err
}

// ReadEvents parses every line; if since is non-zero, keeps only newer events.
// A missing file is not an error (returns nil, nil).
func ReadEvents(path string, since time.Time) ([]InboxEvent, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []InboxEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e InboxEvent
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip malformed lines rather than failing the whole read
		}
		if !since.IsZero() && !e.TS.After(since) {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestInbox -v && go test ./internal/daemon/ -run TestReadEvents -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/daemon/inbox.go internal/daemon/inbox_test.go
git commit -m "feat(daemon): append-only inbox.jsonl with since-filter reader"
```

---

### Task 6: State mirror (per-agent records + version stamp)

**Files:**
- Create: `internal/daemon/state.go`
- Create: `internal/daemon/state_test.go`

**Interfaces:**
- Produces:
  - `type AgentRecord struct { Name string; LastStatus string; LastHandledSHA string; LastSubmittedSHA string; LastEventTS time.Time }` (JSON tags `name,lastStatus,lastHandledSHA,lastSubmittedSHA,lastEventTS`).
  - `func (p Paths) LoadAgent(name string) (AgentRecord, error)` — missing file ⇒ zero record + nil error.
  - `func (p Paths) SaveAgent(r AgentRecord) error` — atomic (temp + rename) under `AgentsDir()`.
  - `func (p Paths) ListAgentRecords() ([]AgentRecord, error)` — all records (for `status`); missing dir ⇒ nil.
  - `func (p Paths) WriteVersion(stamp string) error`, `func (p Paths) ReadVersion() string`.
  - `func BinaryStamp(exePath string) string` — `"<size>-<modunixnano>"`; `""` on stat error.

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/state_test.go
package daemon

import (
	"testing"
	"time"
)

func TestAgentRecordRoundTrip(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	if r, err := p.LoadAgent("ghost"); err != nil || r.Name != "" {
		t.Fatalf("missing record should be zero: %+v %v", r, err)
	}
	rec := AgentRecord{Name: "otter", LastStatus: "done", LastHandledSHA: "abc", LastSubmittedSHA: "abc", LastEventTS: time.Unix(100, 0).UTC()}
	if err := p.SaveAgent(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := p.LoadAgent("otter")
	if err != nil || got.LastHandledSHA != "abc" || got.LastStatus != "done" {
		t.Fatalf("round-trip mismatch: %+v %v", got, err)
	}
	all, err := p.ListAgentRecords()
	if err != nil || len(all) != 1 || all[0].Name != "otter" {
		t.Fatalf("list: %+v %v", all, err)
	}
}

func TestVersionStamp(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	if v := p.ReadVersion(); v != "" {
		t.Fatalf("missing version should be empty, got %q", v)
	}
	if err := p.WriteVersion("123-456"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if v := p.ReadVersion(); v != "123-456" {
		t.Fatalf("got %q", v)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestAgentRecord|TestVersion' -v`
Expected: FAIL — symbols undefined.

- [x] **Step 3: Write implementation**

```go
// internal/daemon/state.go
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AgentRecord is the daemon's per-agent bookkeeping (the state mirror).
type AgentRecord struct {
	Name             string    `json:"name"`
	LastStatus       string    `json:"lastStatus"`
	LastHandledSHA   string    `json:"lastHandledSHA"`   // HEAD at last done-handling (any outcome)
	LastSubmittedSHA string    `json:"lastSubmittedSHA"` // HEAD at last successful submit
	LastEventTS      time.Time `json:"lastEventTS"`
}

// LoadAgent reads a per-agent record; a missing file yields a zero record.
func (p Paths) LoadAgent(name string) (AgentRecord, error) {
	b, err := os.ReadFile(p.AgentRecord(name))
	if errors.Is(err, fs.ErrNotExist) {
		return AgentRecord{}, nil
	}
	if err != nil {
		return AgentRecord{}, err
	}
	var r AgentRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return AgentRecord{}, fmt.Errorf("parse agent record %s: %w", name, err)
	}
	return r, nil
}

// SaveAgent atomically writes a per-agent record (temp + rename).
func (p Paths) SaveAgent(r AgentRecord) error {
	if err := os.MkdirAll(p.AgentsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(p.AgentRecord(r.Name), b, 0o600)
}

// ListAgentRecords returns every saved record (missing dir ⇒ nil).
func (p Paths) ListAgentRecords() ([]AgentRecord, error) {
	entries, err := os.ReadDir(p.AgentsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AgentRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		r, err := p.LoadAgent(name)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// WriteVersion stamps the running binary's identity into the state dir.
func (p Paths) WriteVersion(stamp string) error {
	if err := os.MkdirAll(p.StateDir(), 0o700); err != nil {
		return err
	}
	return atomicWrite(p.Version(), []byte(stamp), 0o600)
}

// ReadVersion returns the stamped binary identity ("" if unset).
func (p Paths) ReadVersion() string {
	b, err := os.ReadFile(p.Version())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// BinaryStamp identifies a binary by size + mod time ("" on stat error).
func BinaryStamp(exePath string) string {
	fi, err := os.Stat(exePath)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// atomicWrite writes via a temp file + rename in the same dir.
func atomicWrite(path string, b []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run 'TestAgentRecord|TestVersion' -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/daemon/state.go internal/daemon/state_test.go
git commit -m "feat(daemon): state mirror — per-agent records + version stamp (atomic writes)"
```

---

### Task 7: Fleet accessors for the supervisor

The supervisor needs the resolved logs root and a per-agent HEAD SHA without re-deriving Fleet's path logic. Add two thin exported wrappers.

**Files:**
- Modify: `internal/fleet/fleet.go`
- Modify: `internal/fleet/logs.go` (export the logs root) — or add to `fleet.go`
- Test: `internal/fleet/fleet_test.go` (add) — but the SHA path needs a real clone; put the SHA test in `submit_test.go` style. Use an existing helper.

**Interfaces:**
- Produces:
  - `func (f *Fleet) LogsDir() string` — exported alias for the unexported `logsRoot()`.
  - `func (f *Fleet) HeadSHA(ctx context.Context, name string) (string, error)` — `gitops.HeadSHA(ctx, f.workDir(name))`.

- [x] **Step 1: Write the failing test**

```go
// internal/fleet/fleet_test.go  (add)
func TestFleetLogsDirRespectsLogRoot(t *testing.T) {
	f := &Fleet{LogRoot: "/custom/logs"}
	if f.LogsDir() != "/custom/logs" {
		t.Fatalf("got %q", f.LogsDir())
	}
}
```

For `HeadSHA`, add to `submit_test.go` (it already builds real clones under a temp WorkRoot). Find the helper there that creates an agent clone (grep `WorkRoot` in `internal/fleet/submit_test.go`) and assert:

```go
// internal/fleet/submit_test.go  (add, reusing that file's clone-building helper)
func TestFleetHeadSHA(t *testing.T) {
	f, name := newFleetWithClone(t, 1) // adapt to the actual helper name in submit_test.go
	sha, err := f.HeadSHA(context.Background(), name)
	if err != nil || len(sha) != 40 {
		t.Fatalf("HeadSHA: %q err=%v", sha, err)
	}
}
```

> If `submit_test.go` has no reusable clone helper, write a small local one mirroring `internal/gitops/inspect_test.go`'s `cloneWithCommits`, placing the clone at `filepath.Join(f.WorkRoot, name)`.

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run 'TestFleetLogsDir|TestFleetHeadSHA' -v`
Expected: FAIL — methods undefined.

- [x] **Step 3: Write implementation**

In `internal/fleet/fleet.go` (after `workRoot`):

```go
// LogsDir returns the resolved per-session logs root (exported for the daemon).
func (f *Fleet) LogsDir() string { return f.logsRoot() }

// HeadSHA returns the current HEAD SHA of agent name's engine-side clone.
func (f *Fleet) HeadSHA(ctx context.Context, name string) (string, error) {
	return gitops.HeadSHA(ctx, f.workDir(name))
}
```

(`gitops` is already imported in `fleet.go`.)

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run 'TestFleetLogsDir|TestFleetHeadSHA' -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/fleet/fleet.go internal/fleet/fleet_test.go internal/fleet/submit_test.go
git commit -m "feat(fleet): LogsDir + HeadSHA accessors for the daemon"
```

---

### Task 8: Supervisor — auto-submit handler (`handle`)

The core reaction. Given an agent name, dedup by SHA, submit (force), and write inbox events + state.

**Files:**
- Create: `internal/daemon/supervisor.go`
- Create: `internal/daemon/supervisor_test.go`

**Interfaces:**
- Consumes: `fleet.Fleet` (via a small interface so tests don't need a full Fleet), `Paths`, inbox/state from Tasks 5–6.
- Produces:
  - ```go
    type submitter interface {
        Submit(ctx context.Context, name string, force bool) (fleet.Submission, error)
        HeadSHA(ctx context.Context, name string) (string, error)
    }
    type Supervisor struct {
        Fleet  submitter
        Paths  Paths
        Now    func() time.Time // injectable clock; nil ⇒ time.Now
    }
    func (s *Supervisor) handle(ctx context.Context, name string) // never returns; logs internally
    ```

Use a narrow `submitter` interface (satisfied by `*fleet.Fleet`) so the supervisor unit test can use a fake without constructing Docker/forge. The real wiring (Task 12) passes the concrete `*fleet.Fleet`.

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/supervisor_test.go
package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mickzijdel/flotilla/internal/fleet"
)

// fakeSubmitter records Submit calls and returns scripted results per agent.
type fakeSubmitter struct {
	heads   map[string]string
	results map[string]fleet.Submission
	errs    map[string]error
	calls   []string
}

func (f *fakeSubmitter) HeadSHA(_ context.Context, name string) (string, error) {
	return f.heads[name], nil
}
func (f *fakeSubmitter) Submit(_ context.Context, name string, force bool) (fleet.Submission, error) {
	if !force {
		return fleet.Submission{}, errors.New("daemon must force-submit")
	}
	f.calls = append(f.calls, name)
	if e := f.errs[name]; e != nil {
		return fleet.Submission{}, e
	}
	return f.results[name], nil
}

func newSup(t *testing.T, fs *fakeSubmitter) *Supervisor {
	t.Helper()
	return &Supervisor{Fleet: fs, Paths: Paths{Root: t.TempDir()}, Now: func() time.Time { return time.Unix(1000, 0).UTC() }}
}

func TestHandleCleanTreeOpensPRAndRecordsSHA(t *testing.T) {
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Agent: "otter", Branch: "flotilla/otter", PRURL: "http://pr/1", Created: true}},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "otter")

	if len(fs.calls) != 1 {
		t.Fatalf("want exactly 1 submit, got %d", len(fs.calls))
	}
	rec, _ := s.Paths.LoadAgent("otter")
	if rec.LastSubmittedSHA != "sha1" || rec.LastHandledSHA != "sha1" {
		t.Fatalf("record not updated: %+v", rec)
	}
	evs, _ := ReadEvents(s.Paths.Inbox(), time.Time{})
	types := eventTypes(evs)
	if !types[EventAgentDone] || !types[EventPROpened] {
		t.Fatalf("missing inbox events, got %v", types)
	}
}

func TestHandleDirtyTreeSkipsSubmit(t *testing.T) {
	fs := &fakeSubmitter{
		heads: map[string]string{"finch": "sha9"},
		errs:  map[string]error{"finch": errors.New(`agent "finch" has uncommitted changes; commit them inside the container first`)},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "finch")

	rec, _ := s.Paths.LoadAgent("finch")
	if rec.LastSubmittedSHA != "" {
		t.Fatalf("dirty tree must not record a submitted SHA: %+v", rec)
	}
	if rec.LastHandledSHA != "sha9" {
		t.Fatalf("handled SHA should be recorded even on skip: %+v", rec)
	}
	evs, _ := ReadEvents(s.Paths.Inbox(), time.Time{})
	types := eventTypes(evs)
	if !types[EventSubmitSkipped] || !types[EventAgentDone] {
		t.Fatalf("want agent_done+submit_skipped, got %v", types)
	}
}

func TestHandleDedupBySHA(t *testing.T) {
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Created: true, PRURL: "u"}},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "otter")
	s.handle(context.Background(), "otter") // same HEAD ⇒ no second submit, no new events
	if len(fs.calls) != 1 {
		t.Fatalf("want 1 submit after dedup, got %d", len(fs.calls))
	}
	evs, _ := ReadEvents(s.Paths.Inbox(), time.Time{})
	if len(evs) != 2 { // agent_done + pr_opened from the first handle only
		t.Fatalf("dedup should suppress repeat events, got %d", len(evs))
	}
}

func TestHandleAdvancedHEADResubmits(t *testing.T) {
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Created: true, PRURL: "u"}},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "otter")
	fs.heads["otter"] = "sha2"
	fs.results["otter"] = fleet.Submission{Created: false, PRURL: "u"} // existing PR ⇒ pr_updated
	s.handle(context.Background(), "otter")
	if len(fs.calls) != 2 {
		t.Fatalf("advanced HEAD should re-submit, got %d calls", len(fs.calls))
	}
}

// eventTypes collapses events to a set of present types.
func eventTypes(evs []InboxEvent) map[string]bool {
	m := map[string]bool{}
	for _, e := range evs {
		m[e.Type] = true
	}
	return m
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestHandle -v`
Expected: FAIL — `Supervisor`/`handle` undefined.

- [x] **Step 3: Write implementation**

```go
// internal/daemon/supervisor.go
package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/mickzijdel/flotilla/internal/fleet"
)

// submitter is the slice of *fleet.Fleet the supervisor reacts with.
type submitter interface {
	Submit(ctx context.Context, name string, force bool) (fleet.Submission, error)
	HeadSHA(ctx context.Context, name string) (string, error)
}

// Supervisor reacts to agent done-signals: it auto-submits and records events.
type Supervisor struct {
	Fleet submitter
	Paths Paths
	Now   func() time.Time // injectable clock (tests); nil ⇒ time.Now
}

func (s *Supervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Supervisor) emit(name, typ, msg string, data map[string]any) {
	_ = AppendEvent(s.Paths.Inbox(), InboxEvent{
		TS: s.now(), Agent: name, Type: typ, Message: msg, Data: data,
	})
}

// handle reacts to a done-signal for agent name: dedup by SHA, then force-submit,
// always recording an agent_done event plus the submit outcome. Best-effort: all
// failures are surfaced as inbox events, never returned.
func (s *Supervisor) handle(ctx context.Context, name string) {
	sha, _ := s.Fleet.HeadSHA(ctx, name) // "" on error → still handled once
	rec, _ := s.Paths.LoadAgent(name)

	// Dedup: same HEAD already handled (covers per-tick rescans and restarts).
	if sha != "" && sha == rec.LastHandledSHA {
		return
	}

	rec.Name = name
	rec.LastStatus = "done"
	rec.LastEventTS = s.now()

	s.emit(name, EventAgentDone, "agent finished", nil)

	sub, err := s.Fleet.Submit(ctx, name, true)
	if err != nil {
		s.emit(name, EventSubmitSkipped, err.Error(), nil)
		rec.LastHandledSHA = sha
		_ = s.Paths.SaveAgent(rec)
		return
	}

	typ, msg := EventPRUpdated, "updated existing PR"
	if sub.Created || sub.PushOnly {
		typ, msg = EventPROpened, "opened PR"
	}
	data := map[string]any{"branch": sub.Branch, "prURL": sub.PRURL}
	if strings.TrimSpace(sub.Note) != "" {
		data["note"] = sub.Note
	}
	s.emit(name, typ, msg, data)

	rec.LastHandledSHA = sha
	rec.LastSubmittedSHA = sha
	_ = s.Paths.SaveAgent(rec)
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestHandle -v`
Expected: PASS (all four handle tests).

- [x] **Step 5: Commit**

```bash
git add internal/daemon/supervisor.go internal/daemon/supervisor_test.go
git commit -m "feat(daemon): auto-submit handler — SHA dedup, force-submit, inbox events"
```

---

### Task 9: Supervisor — scan, Events drain, and Run loop

Wire the triggers: a `scanOnce` that lists agents and handles any whose `status` file reads `done`; a `drainEvents` that handles `die`/`stop` events; and a `Run(ctx)` ticker that calls both and re-checks the binary version.

**Files:**
- Modify: `internal/daemon/supervisor.go`
- Modify: `internal/daemon/supervisor_test.go`

**Interfaces:**
- Consumes: `backend.Backend.Events` (Task 2), `backend.LabelAgent`, `backend.LabelLogDir`.
- Produces (extend `Supervisor`):
  - Add a `lister` capability to the supervisor's deps:
    ```go
    type lister interface {
        List(ctx context.Context) ([]fleet.Agent, error)
    }
    ```
    Widen the `Supervisor.Fleet` field type to a combined interface:
    ```go
    type fleetAPI interface {
        Submit(ctx context.Context, name string, force bool) (fleet.Submission, error)
        HeadSHA(ctx context.Context, name string) (string, error)
        List(ctx context.Context) ([]fleet.Agent, error)
    }
    ```
    (Replace the `submitter` field type with `fleetAPI`; `*fleet.Fleet` satisfies it.)
  - `Events backend.Backend` field (or just `Backend backend.Backend`) for the event stream.
  - `func statusOf(logDir string) string` — reads `<logDir>/status`, trimmed ("" if absent).
  - `func (s *Supervisor) scanOnce(ctx context.Context)` — for each agent with `status == "done"`, call `handle`.
  - `func (s *Supervisor) Run(ctx context.Context, interval time.Duration) error` — startup `scanOnce`, then a ticker loop also draining `Backend.Events`; returns on `ctx.Done`.

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/supervisor_test.go  (add)
// fakeFleet satisfies fleetAPI by delegating to a fakeSubmitter plus a static list.
type fakeFleet struct {
	*fakeSubmitter
	agents []fleet.Agent
}

func (f *fakeFleet) List(_ context.Context) ([]fleet.Agent, error) { return f.agents, nil }

func writeStatus(t *testing.T, logDir, status string) {
	t.Helper()
	if err := os.MkdirAll(logDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "status"), []byte(status+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanOnceHandlesDoneAgents(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-otter")
	writeStatus(t, logDir, "done")
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Created: true, PRURL: "u"}},
	}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter", Status: "running", LogDir: logDir}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}, Now: func() time.Time { return time.Unix(1, 0).UTC() }}

	s.scanOnce(context.Background())
	if len(fs.calls) != 1 {
		t.Fatalf("done agent should be submitted once, got %d", len(fs.calls))
	}
}

func TestScanOnceIgnoresRunningAgents(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-busy")
	writeStatus(t, logDir, "running")
	fs := &fakeSubmitter{heads: map[string]string{"busy": "sha1"}}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "busy", LogDir: logDir}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}}
	s.scanOnce(context.Background())
	if len(fs.calls) != 0 {
		t.Fatalf("running agent must not be submitted, got %d", len(fs.calls))
	}
}

func TestRunStartupScanThenCancel(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-otter")
	writeStatus(t, logDir, "done")
	fs := &fakeSubmitter{heads: map[string]string{"otter": "sha1"}, results: map[string]fleet.Submission{"otter": {Created: true}}}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter", LogDir: logDir}}}
	be := backend.NewFake()
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}, Backend: be}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, 10*time.Millisecond) }()
	// Give the startup scan time to run, then trigger an event + cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("Run returned %v", err)
	}
	if len(fs.calls) < 1 {
		t.Fatalf("startup scan should have submitted, got %d", len(fs.calls))
	}
}

func TestDrainEventsHandlesDie(t *testing.T) {
	tmp := t.TempDir()
	fs := &fakeSubmitter{heads: map[string]string{"otter": "sha1"}, results: map[string]fleet.Submission{"otter": {Created: true}}}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter"}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}}
	ev := backend.Event{Type: "die", Labels: map[string]string{backend.LabelAgent: "otter"}}
	s.handleEvent(context.Background(), ev)
	if len(fs.calls) != 1 {
		t.Fatalf("die event should trigger handle, got %d", len(fs.calls))
	}
}
```

(Add imports: `os`, `path/filepath`, `github.com/mickzijdel/flotilla/internal/backend`.)

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestScanOnce|TestRun|TestDrainEvents' -v`
Expected: FAIL — `scanOnce`/`Run`/`handleEvent`/`Backend` field undefined.

- [x] **Step 3: Implement**

Edit `internal/daemon/supervisor.go`: replace the `submitter` interface + field with the combined API, add the `Backend` field and the scan/run methods.

```go
// (replace the submitter interface with:)
import (
	// ... existing ...
	"os"
	"path/filepath"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// fleetAPI is the slice of *fleet.Fleet the supervisor reacts with.
type fleetAPI interface {
	Submit(ctx context.Context, name string, force bool) (fleet.Submission, error)
	HeadSHA(ctx context.Context, name string) (string, error)
	List(ctx context.Context) ([]fleet.Agent, error)
}

// Supervisor reacts to agent done-signals: it auto-submits and records events.
type Supervisor struct {
	Fleet   fleetAPI
	Backend backend.Backend // for the secondary die/stop event trigger (may be nil)
	Paths   Paths
	Now     func() time.Time
}

// statusOf reads the launch-wrapper status file in an agent's log dir.
func statusOf(logDir string) string {
	if logDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(logDir, "status"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// scanOnce handles every agent whose status file reads "done".
func (s *Supervisor) scanOnce(ctx context.Context) {
	agents, err := s.Fleet.List(ctx)
	if err != nil {
		return
	}
	for _, a := range agents {
		if statusOf(a.LogDir) == "done" {
			s.handle(ctx, a.Name)
		}
	}
}

// handleEvent reacts to a die/stop container event as a done-signal fallback.
func (s *Supervisor) handleEvent(ctx context.Context, ev backend.Event) {
	name := ev.Labels[backend.LabelAgent]
	if name == "" {
		return
	}
	switch ev.Type {
	case "die", "stop":
		s.handle(ctx, name)
	}
}

// Run scans on startup, then ticks every interval (re-scanning + draining the
// Backend event stream) until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context, interval time.Duration) error {
	s.scanOnce(ctx) // catch agents that finished while the daemon was down

	var events <-chan backend.Event
	if s.Backend != nil {
		if ch, err := s.Backend.Events(ctx); err == nil {
			events = ch
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.scanOnce(ctx)
		case ev, ok := <-events:
			if !ok {
				events = nil // stream closed; keep ticking on status files
				continue
			}
			s.handleEvent(ctx, ev)
		}
	}
}
```

Remove the now-unused `submitter` interface.

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run 'TestScanOnce|TestRun|TestDrainEvents|TestHandle' -v`
Expected: PASS (all supervisor tests).

- [x] **Step 5: Commit**

```bash
git add internal/daemon/supervisor.go internal/daemon/supervisor_test.go
git commit -m "feat(daemon): supervisor scan + Run loop + die/stop event fallback"
```

---

### Task 10: Request-handler seam (scaffolding)

The dispatch loop + envelope that on-demand fetch / question plug into. This slice ships only the scaffolding + a test handler — no real fetch/question.

**Files:**
- Create: `internal/daemon/requests.go`
- Create: `internal/daemon/requests_test.go`
- Modify: `internal/daemon/supervisor.go` (call `dispatchRequests` from the tick loop, with a `Registry` field)

**Interfaces:**
- Produces:
  - `type Request struct { ID string; Type string; Data map[string]any }` (JSON `id,type,data`).
  - `type Response struct { Status string; Message string; Data map[string]any }` (JSON `status,message,data`).
  - `type Handler func(ctx context.Context, agent string, req Request) Response`.
  - `type Registry struct { ... }` with `func NewRegistry() *Registry`, `func (r *Registry) Register(typ string, h Handler)`, `func (r *Registry) dispatch(ctx, agent string, req Request) Response`.
  - `func dispatchRequests(ctx context.Context, reg *Registry, agent, sessionDir string)` — scans `<sessionDir>/requests/*.json`, and for each id without a matching `<sessionDir>/responses/<id>.json`, dispatches and writes the response atomically.
- Consumes: `Supervisor.Registry *Registry` field; `Supervisor.scanOnce` also drives request dispatch per agent.

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/requests_test.go
package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDispatchRequestsWritesResponse(t *testing.T) {
	sess := t.TempDir()
	reqDir := filepath.Join(sess, "requests")
	if err := os.MkdirAll(reqDir, 0o777); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "abc", Type: "ping", Data: map[string]any{"x": "y"}}
	b, _ := json.Marshal(req)
	if err := os.WriteFile(filepath.Join(reqDir, "abc.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	var gotAgent string
	reg.Register("ping", func(_ context.Context, agent string, r Request) Response {
		gotAgent = agent
		return Response{Status: "ok", Message: "pong", Data: map[string]any{"echo": r.Data["x"]}}
	})

	dispatchRequests(context.Background(), reg, "otter", sess)

	if gotAgent != "otter" {
		t.Fatalf("handler got agent %q", gotAgent)
	}
	respPath := filepath.Join(sess, "responses", "abc.json")
	rb, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("no response written: %v", err)
	}
	var resp Response
	_ = json.Unmarshal(rb, &resp)
	if resp.Status != "ok" || resp.Data["echo"] != "y" {
		t.Fatalf("bad response: %+v", resp)
	}

	// Idempotent: a second dispatch with the response present is a no-op (no re-call).
	called := false
	reg2 := NewRegistry()
	reg2.Register("ping", func(_ context.Context, _ string, _ Request) Response {
		called = true
		return Response{Status: "ok"}
	})
	dispatchRequests(context.Background(), reg2, "otter", sess)
	if called {
		t.Fatal("already-answered request must not be re-dispatched")
	}
}

func TestDispatchUnknownType(t *testing.T) {
	sess := t.TempDir()
	reqDir := filepath.Join(sess, "requests")
	_ = os.MkdirAll(reqDir, 0o777)
	b, _ := json.Marshal(Request{ID: "z", Type: "nope"})
	_ = os.WriteFile(filepath.Join(reqDir, "z.json"), b, 0o644)

	dispatchRequests(context.Background(), NewRegistry(), "otter", sess)

	rb, err := os.ReadFile(filepath.Join(sess, "responses", "z.json"))
	if err != nil {
		t.Fatalf("unknown type should still get an error response: %v", err)
	}
	var resp Response
	_ = json.Unmarshal(rb, &resp)
	if resp.Status != "error" {
		t.Fatalf("want error status, got %+v", resp)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestDispatch -v`
Expected: FAIL — symbols undefined.

- [x] **Step 3: Write implementation**

```go
// internal/daemon/requests.go
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Request is an agent→daemon control message (filesystem-mediated).
type Request struct {
	ID   string         `json:"id"`
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// Response is the daemon's reply to a Request.
type Response struct {
	Status  string         `json:"status"` // "ok" | "error"
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// Handler reacts to one request type for a given agent.
type Handler func(ctx context.Context, agent string, req Request) Response

// Registry maps request types to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry { return &Registry{handlers: map[string]Handler{}} }

func (r *Registry) Register(typ string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[typ] = h
}

func (r *Registry) dispatch(ctx context.Context, agent string, req Request) Response {
	r.mu.RLock()
	h, ok := r.handlers[req.Type]
	r.mu.RUnlock()
	if !ok {
		return Response{Status: "error", Message: fmt.Sprintf("unknown request type %q", req.Type)}
	}
	return h(ctx, agent, req)
}

// dispatchRequests scans <sessionDir>/requests/*.json and answers any whose
// <sessionDir>/responses/<id>.json does not yet exist. Best-effort + idempotent.
func dispatchRequests(ctx context.Context, reg *Registry, agent, sessionDir string) {
	reqDir := filepath.Join(sessionDir, "requests")
	respDir := filepath.Join(sessionDir, "responses")
	entries, err := os.ReadDir(reqDir)
	if err != nil {
		return // no requests dir yet
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		respPath := filepath.Join(respDir, id+".json")
		if _, err := os.Stat(respPath); err == nil {
			continue // already answered
		}
		b, err := os.ReadFile(filepath.Join(reqDir, e.Name()))
		if err != nil {
			continue
		}
		var req Request
		if err := json.Unmarshal(b, &req); err != nil {
			continue
		}
		if req.ID == "" {
			req.ID = id
		}
		resp := reg.dispatch(ctx, agent, req)
		rb, _ := json.Marshal(resp)
		if err := os.MkdirAll(respDir, 0o777); err != nil {
			continue
		}
		_ = atomicWrite(respPath, rb, 0o644)
	}
}
```

- [x] **Step 4: Wire into the supervisor**

In `internal/daemon/supervisor.go`, add a field `Registry *Registry` to `Supervisor`, and dispatch requests for each agent during `scanOnce` (so the seam is live even though no real handler is registered yet):

```go
// in scanOnce, inside the agent loop, after the done check:
		if s.Registry != nil && a.LogDir != "" {
			dispatchRequests(ctx, s.Registry, a.Name, a.LogDir)
		}
```

(The session dir IS the agent's `LogDir` — the host side of the `/flotilla/session` mount, per `internal/fleet/logs.go:17` and `fleet.go:103`.)

- [x] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run 'TestDispatch|TestScanOnce' -v`
Expected: PASS. Then full package: `go test ./internal/daemon/`.

- [x] **Step 6: Commit**

```bash
git add internal/daemon/requests.go internal/daemon/requests_test.go internal/daemon/supervisor.go
git commit -m "feat(daemon): request-handler seam (envelope + registry + dispatch loop)"
```

---

### Task 11: Lifecycle — flock, pidfile, IsRunning, Status, re-exec decision

The single-instance + process-management primitives. Side-effecting syscalls (Setsid spawn, Exec) are thin and isolated; the testable logic (flock acquire/reject, pidfile, IsRunning, shouldReexec, Status assembly) is tested directly.

**Files:**
- Create: `internal/daemon/lifecycle.go`
- Create: `internal/daemon/lifecycle_test.go`

**Interfaces:**
- Produces:
  - `func acquireLock(path string) (*os.File, error)` — opens path `0600`, `syscall.Flock(fd, LOCK_EX|LOCK_NB)`; returns the held file (keep open for lifetime) or an error if already locked.
  - `func writePidFile(path string, pid int) error`, `func readPidFile(path string) (int, error)`.
  - `func IsRunning(p Paths) bool` — pidfile present + `process.Signal(syscall.Signal(0))` succeeds.
  - `type Status struct { Running bool; PID int; Version string; WatchedAgents int; Recent []InboxEvent }`
  - `func ReadStatus(p Paths, recent int) Status` — assembles from pidfile + state mirror + last `recent` inbox events.
  - `func shouldReexec(stored, current string) bool` — `stored != "" && current != "" && stored != current`.
  - `func StopDaemon(p Paths, wait time.Duration) error` — SIGTERM the pid, poll until gone.

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/lifecycle_test.go
package daemon

import (
	"os"
	"testing"
	"time"
)

func TestAcquireLockRejectsSecond(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	f1, err := acquireLock(p.Lock())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = f1.Close() }()
	if _, err := acquireLock(p.Lock()); err == nil {
		t.Fatal("second acquire must fail while lock is held")
	}
	_ = f1.Close()
	f2, err := acquireLock(p.Lock())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = f2.Close()
}

func TestPidFileRoundTrip(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	if err := writePidFile(p.Pid(), 4242); err != nil {
		t.Fatalf("write: %v", err)
	}
	pid, err := readPidFile(p.Pid())
	if err != nil || pid != 4242 {
		t.Fatalf("got %d, %v", pid, err)
	}
}

func TestIsRunningSelf(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	if IsRunning(p) {
		t.Fatal("no pidfile ⇒ not running")
	}
	_ = writePidFile(p.Pid(), os.Getpid()) // our own pid is alive
	if !IsRunning(p) {
		t.Fatal("live pid ⇒ running")
	}
	_ = writePidFile(p.Pid(), 9999999) // unlikely-live pid
	if IsRunning(p) {
		t.Fatal("dead pid ⇒ not running")
	}
}

func TestShouldReexec(t *testing.T) {
	cases := []struct {
		stored, current string
		want            bool
	}{
		{"a", "b", true},
		{"a", "a", false},
		{"", "b", false},
		{"a", "", false},
	}
	for _, c := range cases {
		if got := shouldReexec(c.stored, c.current); got != c.want {
			t.Errorf("shouldReexec(%q,%q)=%v want %v", c.stored, c.current, got, c.want)
		}
	}
}

func TestReadStatus(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = writePidFile(p.Pid(), os.Getpid())
	_ = p.WriteVersion("42-7")
	_ = p.SaveAgent(AgentRecord{Name: "otter", LastStatus: "done"})
	_ = AppendEvent(p.Inbox(), InboxEvent{TS: time.Unix(1, 0).UTC(), Agent: "otter", Type: EventAgentDone})

	st := ReadStatus(p, 10)
	if !st.Running || st.PID != os.Getpid() || st.Version != "42-7" {
		t.Fatalf("status basics: %+v", st)
	}
	if st.WatchedAgents != 1 || len(st.Recent) != 1 {
		t.Fatalf("status counts: %+v", st)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestAcquireLock|TestPidFile|TestIsRunning|TestShouldReexec|TestReadStatus' -v`
Expected: FAIL — symbols undefined.

- [x] **Step 3: Write implementation**

```go
// internal/daemon/lifecycle.go
package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// acquireLock takes an exclusive, non-blocking flock on path. The returned file
// must stay open for as long as the lock is needed; closing it releases the lock.
func acquireLock(path string) (*os.File, error) {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("daemon already running (lock held): %w", err)
	}
	return f, nil
}

func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
}

func writePidFile(path string, pid int) error {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return err
	}
	return atomicWrite(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func readPidFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// IsRunning reports whether a live daemon process is recorded in the pidfile.
func IsRunning(p Paths) bool {
	pid, err := readPidFile(p.Pid())
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// shouldReexec is true when both stamps are known and they differ.
func shouldReexec(stored, current string) bool {
	return stored != "" && current != "" && stored != current
}

// Status is the daemon's externally-visible state (for `flotilla daemon status`).
type Status struct {
	Running       bool         `json:"running"`
	PID           int          `json:"pid"`
	Version       string       `json:"version"`
	WatchedAgents int          `json:"watchedAgents"`
	Recent        []InboxEvent `json:"recent"`
}

// ReadStatus assembles daemon status from the pidfile + state mirror + inbox.
func ReadStatus(p Paths, recent int) Status {
	st := Status{Running: IsRunning(p), Version: p.ReadVersion()}
	if pid, err := readPidFile(p.Pid()); err == nil {
		st.PID = pid
	}
	if recs, err := p.ListAgentRecords(); err == nil {
		st.WatchedAgents = len(recs)
	}
	if evs, err := ReadEvents(p.Inbox(), time.Time{}); err == nil {
		if len(evs) > recent {
			evs = evs[len(evs)-recent:]
		}
		st.Recent = evs
	}
	return st
}

// StopDaemon SIGTERMs the recorded pid and polls until it exits (or wait elapses).
func StopDaemon(p Paths, wait time.Duration) error {
	pid, err := readPidFile(p.Pid())
	if err != nil {
		return fmt.Errorf("daemon not running (no pidfile)")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if proc.Signal(syscall.Signal(0)) != nil {
			return nil // gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon (pid %d) did not exit within %s", pid, wait)
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run 'TestAcquireLock|TestPidFile|TestIsRunning|TestShouldReexec|TestReadStatus' -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/daemon/lifecycle.go internal/daemon/lifecycle_test.go
git commit -m "feat(daemon): lifecycle primitives — flock, pidfile, IsRunning, status, re-exec decision"
```

---

### Task 12: Foreground run + daemonize spawn + EnsureRunning + re-exec wiring

Tie the supervisor and lifecycle together: `RunForeground` (the body of `flotilla daemon run`), `Start`/`EnsureRunning` (the detached spawn used by `flotilla daemon start` and by `spawn`), and the periodic re-exec self-check.

**Files:**
- Modify: `internal/daemon/lifecycle.go`
- Modify: `internal/daemon/supervisor.go` (re-exec check inside the Run loop)
- Test: `internal/daemon/lifecycle_test.go` (add a single-instance `RunForeground` test)

**Interfaces:**
- Produces:
  - `func RunForeground(ctx context.Context, sup *Supervisor, p Paths, exePath string, interval time.Duration) error` — acquire lock (return nil with a friendly message if already held), write pidfile + version stamp, install SIGTERM/SIGINT cancel, run `sup.Run`, clean up pidfile on exit.
  - `func Start(p Paths, exePath string) error` — if `IsRunning`, no-op; else spawn `exePath daemon run` detached (`Setsid`, stdout/stderr → `p.Log()`).
  - `func EnsureRunning(p Paths, exePath string) error` — alias of `Start` used by `spawn` (best-effort).
- Consumes: `Supervisor.Run` (Task 9); `BinaryStamp`/version (Task 6).

- [x] **Step 1: Write the failing test**

```go
// internal/daemon/lifecycle_test.go  (add)
func TestRunForegroundSingleInstance(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	// Hold the lock as if a daemon were already running.
	held, err := acquireLock(p.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	sup := &Supervisor{Paths: p}
	// Already-locked ⇒ RunForeground returns promptly without error.
	err = RunForeground(context.Background(), sup, p, "/bin/true", time.Second)
	if err != nil {
		t.Fatalf("expected clean no-op when already running, got %v", err)
	}
}

func TestRunForegroundWritesPidThenCleansUp(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	// A nil-Fleet supervisor: scanOnce no-ops if List is nil, so give it a fake.
	fs := &fakeSubmitter{}
	sup := &Supervisor{Fleet: &fakeFleet{fakeSubmitter: fs}, Paths: p}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunForeground(ctx, sup, p, "/bin/true", 10*time.Millisecond) }()
	// Wait until the pidfile appears.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := readPidFile(p.Pid()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid, err := readPidFile(p.Pid()); err != nil || pid != os.Getpid() {
		t.Fatalf("pidfile not written with our pid: %d %v", pid, err)
	}
	if p.ReadVersion() == "" {
		t.Fatal("version stamp not written")
	}
	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("RunForeground: %v", err)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRunForeground -v`
Expected: FAIL — `RunForeground` undefined.

- [x] **Step 3: Implement RunForeground / Start / EnsureRunning**

```go
// internal/daemon/lifecycle.go  (add imports: "context", "errors", "os/exec", "os/signal")
// RunForeground is the body of `flotilla daemon run`: single-instanced, it writes
// the pidfile + version stamp, traps SIGTERM/SIGINT, and runs the supervisor.
func RunForeground(ctx context.Context, sup *Supervisor, p Paths, exePath string, interval time.Duration) error {
	lock, err := acquireLock(p.Lock())
	if err != nil {
		// Already running — a clean, non-error no-op (matches `start` semantics).
		if pid, e := readPidFile(p.Pid()); e == nil {
			fmt.Fprintf(os.Stderr, "daemon already running, pid %d\n", pid)
		}
		return nil
	}
	defer func() { _ = lock.Close() }()

	if err := writePidFile(p.Pid(), os.Getpid()); err != nil {
		return err
	}
	defer func() { _ = os.Remove(p.Pid()) }()
	_ = p.WriteVersion(BinaryStamp(exePath))

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	sup.ExePath = exePath // enable the re-exec self-check (Step 4)
	err = sup.Run(ctx, interval)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Start spawns a detached `<exe> daemon run` if no daemon is running.
func Start(p Paths, exePath string) error {
	if IsRunning(p) {
		return nil
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(p.Log(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logf.Close() }()
	cmd := exec.Command(exePath, "daemon", "run")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from our session
	return cmd.Start()                                    // do not Wait — it lives on
}

// EnsureRunning is Start, used by `spawn` as a best-effort auto-start.
func EnsureRunning(p Paths, exePath string) error { return Start(p, exePath) }
```

- [x] **Step 4: Add the re-exec self-check to the supervisor loop**

In `internal/daemon/supervisor.go`, add field `ExePath string` to `Supervisor`, and inside `Run`'s ticker case, after `s.scanOnce(ctx)`:

```go
			if s.ExePath != "" {
				stored := s.Paths.ReadVersion()
				if cur := BinaryStamp(s.ExePath); shouldReexec(stored, cur) {
					// Binary changed under us: re-exec the new image. Release the
					// flock first (RunForeground holds it via a deferred Close that
					// won't run across Exec), so close fds on exec by relying on the
					// new process re-acquiring. Best-effort.
					_ = syscall.Exec(s.ExePath, []string{s.ExePath, "daemon", "run"}, os.Environ())
				}
			}
```

> Note on the lock across re-exec: `syscall.Exec` replaces the process image, so deferred `Close()` calls do **not** run; the lock fd stays open and is inherited (flock is associated with the open file description, which survives `execve` unless `O_CLOEXEC`). The re-exec'd `daemon run` therefore inherits the held lock — but `acquireLock` opens a *new* fd and `LOCK_NB` would fail against the inherited lock, making the new image exit as "already running." To avoid that, `acquireLock` failure in `RunForeground` is a clean no-op, but here we must ensure the new image actually takes over. **Simplest correct approach:** before `Exec`, unlock explicitly. Implement by storing the lock `*os.File` on the supervisor and calling `syscall.Flock(fd, LOCK_UN)` + `Close()` right before `Exec`. Add a `LockFile *os.File` field set by `RunForeground` and used here:

Refine — set `sup.LockFile = lock` in `RunForeground` after acquiring, and in the re-exec branch:

```go
				if s.LockFile != nil {
					_ = syscall.Flock(int(s.LockFile.Fd()), syscall.LOCK_UN)
					_ = s.LockFile.Close()
				}
				_ = os.Remove(s.Paths.Pid())
				_ = syscall.Exec(s.ExePath, []string{s.ExePath, "daemon", "run"}, os.Environ())
```

Add `LockFile *os.File` to the `Supervisor` struct (import `os` already present).

- [x] **Step 5: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestRunForeground -v && go test ./internal/daemon/`
Expected: PASS (whole package).

- [x] **Step 6: Commit**

```bash
git add internal/daemon/lifecycle.go internal/daemon/supervisor.go internal/daemon/lifecycle_test.go
git commit -m "feat(daemon): RunForeground + detached Start/EnsureRunning + re-exec on upgrade"
```

---

### Task 13: CLI — `flotilla daemon start|stop|status|run`

**Files:**
- Create: `internal/cli/daemon.go`
- Modify: `internal/cli/cli.go` (register `daemonCmd(f)`)
- Test: `internal/cli/daemon_test.go`

**Interfaces:**
- Consumes: `daemon.DefaultPaths`, `daemon.Start`, `daemon.StopDaemon`, `daemon.ReadStatus`, `daemon.RunForeground`, `daemon.NewRegistry`, and a `*daemon.Supervisor` built over the CLI's `*fleet.Fleet`.
- Produces: `func daemonCmd(f *fleet.Fleet) *cobra.Command` with subcommands `start`, `stop`, `status` (`--json`), `run`.
- The Supervisor is built with `Fleet: f, Backend: f.Backend, Paths: daemon.DefaultPaths(), Registry: daemon.NewRegistry()`. The logs root the supervisor scans is `f.LogsDir()` — but the supervisor reads each agent's `LogDir` from `List`, so no extra wiring is needed; `Paths.Root` only needs to match where Fleet writes (`~/.flotilla`). When `f.LogRoot`/`WorkRoot` are defaults, `DefaultPaths().Root` == `~/.flotilla` aligns automatically.

- [x] **Step 1: Write the failing test**

```go
// internal/cli/daemon_test.go
package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func TestDaemonStatusJSONWhenStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.flotilla
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"daemon", "status", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var st struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("bad json %q: %v", out.String(), err)
	}
	if st.Running {
		t.Fatal("no daemon started ⇒ running should be false")
	}
}

func TestDaemonStatusTextWhenStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"daemon", "status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "not running") {
		t.Fatalf("want 'not running' in %q", out.String())
	}
}

func TestDaemonStopWhenStoppedErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"daemon", "stop"})
	if err := root.Execute(); err == nil {
		t.Fatal("stop with no daemon should error")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestDaemon -v`
Expected: FAIL — `daemon` command unknown.

- [x] **Step 3: Write implementation**

```go
// internal/cli/daemon.go
package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mickzijdel/flotilla/internal/daemon"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

// scanInterval is the supervisor's poll cadence (status files + events + re-exec).
const scanInterval = 2 * time.Second

func currentExe() string {
	if exe, err := osExecutable(); err == nil {
		return exe
	}
	return "flotilla"
}

func daemonCmd(f *fleet.Fleet) *cobra.Command {
	c := &cobra.Command{Use: "daemon", Short: "Run the optional supervisor (auto-submit + inbox)"}
	c.AddCommand(daemonStartCmd(f), daemonStopCmd(), daemonStatusCmd(), daemonRunCmd(f))
	return c
}

func daemonStartCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := daemon.DefaultPaths()
			if daemon.IsRunning(p) {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon already running")
				return err
			}
			if err := daemon.Start(p, currentExe()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon started")
			return err
		},
	}
}

func daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := daemon.StopDaemon(daemon.DefaultPaths(), 5*time.Second); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			return err
		},
	}
}

func daemonStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := daemon.ReadStatus(daemon.DefaultPaths(), 5)
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
			}
			out := cmd.OutOrStdout()
			if !st.Running {
				_, err := fmt.Fprintln(out, "daemon: not running")
				return err
			}
			if _, err := fmt.Fprintf(out, "daemon: running (pid %d), %d watched agent(s)\n", st.PID, st.WatchedAgents); err != nil {
				return err
			}
			for _, e := range st.Recent {
				if _, err := fmt.Fprintf(out, "  %s  %s  %s\n", e.TS.Format(time.RFC3339), e.Agent, e.Type); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

func daemonRunCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:    "run",
		Short:  "Run the daemon in the foreground (for systemd)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := daemon.DefaultPaths()
			sup := &daemon.Supervisor{
				Fleet:    f,
				Backend:  f.Backend,
				Paths:    p,
				Registry: daemon.NewRegistry(),
			}
			return daemon.RunForeground(cmd.Context(), sup, p, currentExe(), scanInterval)
		},
	}
}
```

Add a tiny indirection so tests don't shell to the real binary path:

```go
// internal/cli/daemon.go  (top-level var for testability)
var osExecutable = os.Executable // overridable in tests
```

(Import `os`.)

- [x] **Step 4: Register in cli.go**

In `internal/cli/cli.go`, extend the `AddCommand` line:

```go
	root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), submitCmd(f), logsCmd(f), daemonCmd(f), inboxCmd(f), agentsCmd(), doctorCmd())
```

(`inboxCmd` lands in Task 14; if implementing strictly in order, add `daemonCmd(f)` here now and `inboxCmd(f)` in Task 14.)

- [x] **Step 5: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestDaemon -v`
Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/cli/daemon.go internal/cli/cli.go internal/cli/daemon_test.go
git commit -m "feat(cli): flotilla daemon start|stop|status|run"
```

---

### Task 14: CLI — `flotilla inbox`

**Files:**
- Create: `internal/cli/inbox.go`
- Modify: `internal/cli/cli.go` (register `inboxCmd(f)` if not already)
- Test: `internal/cli/inbox_test.go`

**Interfaces:**
- Consumes: `daemon.DefaultPaths`, `daemon.ReadEvents`, `daemon.InboxEvent`.
- Produces: `func inboxCmd(f *fleet.Fleet) *cobra.Command` with `--json`, `--since <ts>`, `--watch`.
- `--watch` reuses the poll-and-print loop pattern from `logs.go`'s `followLog` (read new lines every 200 ms; track the count already printed). Mutually exclusive with `--json` (matching `logs`).

- [x] **Step 1: Write the failing test**

```go
// internal/cli/inbox_test.go
package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/daemon"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func seedInbox(t *testing.T) {
	t.Helper()
	p := daemon.DefaultPaths()
	_ = daemon.AppendEvent(p.Inbox(), daemon.InboxEvent{TS: time.Unix(100, 0).UTC(), Agent: "otter", Type: daemon.EventAgentDone, Message: "done"})
	_ = daemon.AppendEvent(p.Inbox(), daemon.InboxEvent{TS: time.Unix(200, 0).UTC(), Agent: "otter", Type: daemon.EventPROpened, Message: "opened"})
}

func TestInboxText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedInbox(t)
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"inbox"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "agent_done") || !strings.Contains(s, "pr_opened") {
		t.Fatalf("missing events in %q", s)
	}
}

func TestInboxJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedInbox(t)
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"inbox", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Two JSONL lines.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl lines, got %d: %q", len(lines), out.String())
	}
}

func TestInboxSince(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedInbox(t)
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"inbox", "--since", time.Unix(150, 0).UTC().Format(time.RFC3339)})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	s := out.String()
	if strings.Contains(s, "agent_done") || !strings.Contains(s, "pr_opened") {
		t.Fatalf("since filter wrong: %q", s)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestInbox -v`
Expected: FAIL — `inbox` command unknown.

- [x] **Step 3: Write implementation**

```go
// internal/cli/inbox.go
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/mickzijdel/flotilla/internal/daemon"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func inboxCmd(_ *fleet.Fleet) *cobra.Command {
	var asJSON, watch bool
	var since string
	c := &cobra.Command{
		Use:   "inbox",
		Short: "Show daemon events (agent done, PR opened, submit skipped)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := daemon.DefaultPaths()
			var sinceT time.Time
			if since != "" {
				t, err := time.Parse(time.RFC3339, since)
				if err != nil {
					return fmt.Errorf("invalid --since %q (want RFC3339): %w", since, err)
				}
				sinceT = t
			}
			if watch {
				return watchInbox(cmd.Context(), p.Inbox(), sinceT, cmd.OutOrStdout())
			}
			evs, err := daemon.ReadEvents(p.Inbox(), sinceT)
			if err != nil {
				return err
			}
			return printEvents(cmd.OutOrStdout(), evs, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSONL")
	c.Flags().BoolVar(&watch, "watch", false, "stream new events as they arrive")
	c.Flags().StringVar(&since, "since", "", "only events after this RFC3339 timestamp")
	c.MarkFlagsMutuallyExclusive("json", "watch")
	return c
}

func printEvents(out io.Writer, evs []daemon.InboxEvent, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		for _, e := range evs {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range evs {
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", e.TS.Format(time.RFC3339), e.Agent, e.Type, e.Message); err != nil {
			return err
		}
	}
	return nil
}

// watchInbox prints existing events, then polls for new ones every 200ms.
func watchInbox(ctx context.Context, path string, since time.Time, out io.Writer) error {
	printed := 0
	for {
		evs, err := daemon.ReadEvents(path, since)
		if err != nil {
			return err
		}
		for _, e := range evs[min(printed, len(evs)):] {
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", e.TS.Format(time.RFC3339), e.Agent, e.Type, e.Message); err != nil {
				return err
			}
		}
		printed = len(evs)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}
```

(Add `"context"` to imports. `min` is a Go 1.21+ builtin.)

- [x] **Step 4: Ensure registration**

Confirm `inboxCmd(f)` is in the `root.AddCommand(...)` list in `cli.go` (added in Task 13's edit).

- [x] **Step 5: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestInbox -v`
Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/cli/inbox.go internal/cli/cli.go internal/cli/inbox_test.go
git commit -m "feat(cli): flotilla inbox (json + since + watch)"
```

---

### Task 15: `spawn` best-effort auto-start + `doctor` advisory

**Files:**
- Modify: `internal/cli/cli.go` (spawnCmd + doctorCmd)
- Test: `internal/cli/cli_test.go` (add)

**Interfaces:**
- Consumes: `daemon.EnsureRunning`, `daemon.DefaultPaths`, `daemon.IsRunning`, `currentExe`.
- Produces: after a successful `f.Spawn`, call `daemon.EnsureRunning(daemon.DefaultPaths(), currentExe())` and ignore the error (advisory). `doctor` prints whether the daemon is running.

- [x] **Step 1: Write the failing test**

```go
// internal/cli/cli_test.go  (add)
func TestDoctorReportsDaemonStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"doctor"})
	_ = root.Execute() // may error on missing docker; we only assert the daemon line
	if !strings.Contains(strings.ToLower(out.String()), "daemon") {
		t.Fatalf("doctor should mention the daemon, got %q", out.String())
	}
}
```

(Ensure `bytes`, `strings`, `backend`, `fleet` are imported in `cli_test.go`.)

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestDoctorReportsDaemon -v`
Expected: FAIL — no "daemon" line.

- [x] **Step 3: Implement**

In `spawnCmd`'s `RunE`, after the successful spawn print (`fmt.Fprintf(... a.Name ...)`), add:

```go
				// Best-effort: bring up the daemon so auto-submit + inbox "just work".
				// Failure is advisory — the spawn already succeeded.
				_ = daemon.EnsureRunning(daemon.DefaultPaths(), currentExe())
```

In `doctorCmd`'s `RunE`, before the final `if !rep.OK()` block, add:

```go
			if daemon.IsRunning(daemon.DefaultPaths()) {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "ok: daemon running (auto-submit + inbox active)"); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "advisory: daemon not running — start it with 'flotilla daemon start' for auto-submit/inbox (everything works without it)"); err != nil {
					return err
				}
			}
```

Add `"github.com/mickzijdel/flotilla/internal/daemon"` to `cli.go` imports.

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestDoctorReportsDaemon -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat(cli): spawn best-effort daemon auto-start + doctor daemon advisory"
```

---

### Task 16: Full-suite verification, lint, format, and docs

**Files:**
- Modify: `README.md` (document the daemon + inbox commands)
- Modify: `docs/backlog.md` (mark the daemon item shipped)
- Modify: this plan (check off completed tasks)

- [x] **Step 1: Run the full test suite with race**

Run: `go test -race ./... 2>&1 | tail -30`
Expected: all packages PASS (ingest full output; do not tail-truncate errors — re-run without `| tail` if anything fails).

- [x] **Step 2: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no output / no errors.

- [x] **Step 3: Lint + format**

Run: `golangci-lint run ./... && golangci-lint fmt --diff`
Expected: clean. If `fmt --diff` shows changes, run `golangci-lint fmt` and re-stage.

- [x] **Step 4: Manual smoke test (real binary, no Docker needed for inbox/status)**

```bash
go build -o /tmp/flotilla-daemon-smoke .
export HOME=$(mktemp -d)
/tmp/flotilla-daemon-smoke daemon status            # → "daemon: not running"
/tmp/flotilla-daemon-smoke daemon start             # → "daemon started"
sleep 1
/tmp/flotilla-daemon-smoke daemon status --json     # → {"running":true,...}
/tmp/flotilla-daemon-smoke inbox                     # → (empty; no events yet)
/tmp/flotilla-daemon-smoke daemon stop              # → "daemon stopped"
/tmp/flotilla-daemon-smoke daemon status            # → "daemon: not running"
```

Expected: the status transitions match the comments; `daemon.log` exists under `$HOME/.flotilla/`. Verify with your own eyes (per "Always Works").

- [x] **Step 5: Update README**

Add a "Daemon" section documenting `flotilla daemon start|stop|status` and `flotilla inbox`, the auto-submit-on-done behaviour, and that it's optional (everything works without it). Match the README's existing tone/format.

- [x] **Step 6: Update backlog**

In `docs/backlog.md`, mark the daemon (supervisor / auto-submit / inbox) item as shipped, linking this plan + the design spec, mirroring how the logs/submission items were marked done.

- [x] **Step 7: Commit docs**

```bash
git add README.md docs/backlog.md docs/plans/2026-06-23-flotilla-daemon.md
git commit -m "docs: daemon (supervisor / auto-submit / inbox) shipped"
```

---

## Self-review against the spec

- §1 goal (auto-submit + inbox + request seam): Tasks 8–10, 13–15. ✓
- §2 decisions: additive (no CLI↔daemon RPC) — supervisor reads Docker+FS only ✓; self-daemonizing + flock (Task 11–12) ✓; spawn auto-start (Task 15) ✓; auto-submit safe-gated + SHA dedup (Task 8) ✓; filesystem inbox (Task 5) ✓; done-signal = status file (Task 9) ✓; state mirror to files + `--json` (Tasks 6, 13) ✓; version skew re-exec (Task 12) ✓.
- §4 lifecycle (start/stop/status/run, pidfile, flock, auto-start, re-exec): Tasks 11–13, 15. ✓
- §5 done-signal (status-file watch + startup scan): Task 9 (`scanOnce` on startup + tick). ✓
- §6 auto-submit (dedup by SHA, force submit, strict checks, agent_done always): Task 8. ✓
- §7 inbox (jsonl, `inbox` cmd, `--json/--watch/--since`, open schema): Tasks 5, 14. ✓
- §8 state mirror (`daemon.pid`, `version`, `agents/<name>.json`): Tasks 4, 6, 11. ✓
- §9 request seam (envelope + dispatch + watch, no real handler): Task 10. ✓
- §10 backend seam (`Event` + `Events`, docker impl, pushable Fake): Tasks 2–3. ✓
- §11 CLI surface (daemon, inbox, spawn auto-start, doctor advisory): Tasks 13–15. `flotilla answer` is explicitly deferred by the spec — not implemented. ✓
- §12 trust/safety (push only to `flotilla/<agent>`, `~/.flotilla` 0600/0700): inherited from `Submit` + `0600`/`0700` perms throughout. ✓
- §13 failure modes: daemon-down (no auto-actions, CLI works) ✓; crash mid-submit (startup scan + SHA dedup, Task 9/8) ✓; double-start (flock, Task 11) ✓; finished-while-down (startup scan, Task 9) ✓; container-killed (Events fallback, Task 9) ✓; binary upgraded (re-exec, Task 12) ✓; strict refusal (submit_skipped, Task 8) ✓.
- §14 testing: every bullet maps to a test in Tasks 2–15. Docker integration self-skips (Task 3). ✓
- §15 sequencing: logs prerequisite already merged (commit `03d73ce`); fetch handler explicitly deferred (Task 10 leaves the seam). ✓
- §16 out of scope (socket, question channel, fetch handler, queue semantics, auto-fetch): none implemented. ✓
