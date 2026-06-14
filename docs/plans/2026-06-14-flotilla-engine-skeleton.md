# Flotilla Engine Walking Skeleton — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go CLI (`flotilla`) that spawns an agent-agnostic Docker container on a fresh engine-side clone of a repo, lists the fleet from Docker labels, prints an attach target, and stops/removes agents.

**Architecture:** A `Backend` interface abstracts the compute substrate (local Docker impl shells out to `docker`); a fake backend makes the orchestration logic (`fleet`) unit-testable without Docker. Agents are described by declarative `Profile` structs loaded from TOML (built-ins embedded). The engine clones the repo itself (no creds in the container) and renders the profile's launch command as the container command. State is derived from Docker labels — no daemon.

**Tech Stack:** Go 1.23+, `github.com/spf13/cobra` (CLI), `github.com/BurntSushi/toml` (profiles), `os/exec` (docker/git), stdlib `testing`.

**Scope (this plan):** project scaffolding · profile model + loader · agent naming · `Backend` interface + fake · Docker backend · engine-side clone · `Spawn`/`List`/`Attach`/`Stop`/`Remove` · CLI wiring (`spawn`/`list`/`attach`/`stop`/`rm`/`agents`) with `--json`.
**Deferred to later plans:** devcontainer CLI + Feature overlay, credential/config injection + setup handlers, egress firewall, submission (push/PR) + done-signal, logs/transcript mounts, VS Code extension, remote backend.

---

## File Structure

```
flotilla/
  go.mod
  main.go                          # cobra root; wires subcommands
  internal/
    agent/
      profile.go                   # Profile struct, TOML load, embedded built-ins
      profile_test.go
      builtin/                      # embedded built-in profiles
        claude.toml
        codex.toml
    naming/
      naming.go                    # curated word list, unique-letter pick
      naming_test.go
    backend/
      backend.go                   # Backend interface + Container/CreateOpts/Mount/AttachInfo types, label keys
      docker.go                    # local Docker impl (shells out to `docker`)
      docker_test.go               # integration (skips without docker)
      fake.go                      # in-memory fake backend for unit tests
    gitops/
      clone.go                     # engine-side fresh clone (shells out to `git`)
      clone_test.go                # integration against a local bare repo (hermetic)
    fleet/
      fleet.go                     # Spawn/List/Attach/Stop/Remove
      fleet_test.go
    cli/
      cli.go                       # buildRoot(*fleet.Fleet) *cobra.Command + subcommands
      cli_test.go
```

**Type contracts (defined once, used everywhere — keep names exact):**

- `agent.Profile{ Name, Install, Launch, Setup, ConfigMounts []string, Env []string, TranscriptPath, EgressAllow []string, DoneSignal string }`
- `backend.Mount{ Source, Target string }`
- `backend.CreateOpts{ Name, Image string, Cmd []string, Workdir string, Mounts []Mount, Env map[string]string, Labels map[string]string }`
- `backend.Container{ ID, Name, Repo, Status string, Created time.Time, Labels map[string]string }`
- `backend.AttachInfo{ ContainerID, DockerExec, VSCode string }`
- `backend.Backend` interface: `Create(ctx, CreateOpts) (string, error)`, `Start(ctx, id) error`, `Stop(ctx, id) error`, `Remove(ctx, id) error`, `Exec(ctx, id, cmd []string) error`, `List(ctx, labelFilter map[string]string) ([]Container, error)`, `AttachInfo(ctx, id) (AttachInfo, error)`
- Label keys (const): `LabelAgent = "flotilla.agent"`, `LabelRepo = "flotilla.repo"`, `LabelCreated = "flotilla.created"`, `LabelHost = "flotilla.host"`
- `fleet.Agent{ Name, Repo, Status string, Created time.Time, ID string }`
- `fleet.Fleet{ Backend backend.Backend, BaseImage string }`

---

## Task 1: Project scaffolding

**Files:**
- Create: `flotilla/go.mod`
- Create: `flotilla/main.go`

- [ ] **Step 1: Initialize the module and add deps**

Run:
```bash
cd /home/mick/Stack/Programmeren/flotilla
go mod init github.com/mickzijdel/flotilla
go get github.com/spf13/cobra@latest github.com/BurntSushi/toml@latest
```
Expected: `go.mod` created listing both deps.

- [ ] **Step 2: Write a minimal root command**

`main.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/cli"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func main() {
	f := &fleet.Fleet{
		Backend:   backend.NewDocker(),
		BaseImage: "ubuntu:24.04",
	}
	root := cli.BuildRoot(f)
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Add a temporary stub so it compiles**

Create `internal/cli/cli.go` (replaced in Task 11):
```go
package cli

import (
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

// BuildRoot is fully implemented in Task 11.
func BuildRoot(_ *fleet.Fleet) *cobra.Command {
	return &cobra.Command{Use: "flotilla", Short: "Manage a fleet of autonomous coding agents"}
}
```
Create `internal/backend/backend.go` with just the constructor stub so `main.go` compiles (replaced in Task 4/5):
```go
package backend

// NewDocker is implemented in Task 5.
func NewDocker() Backend { return nil }

// Backend is fully defined in Task 4.
type Backend interface{}
```
Create `internal/fleet/fleet.go` stub (replaced in Task 7):
```go
package fleet

import "github.com/mickzijdel/flotilla/internal/backend"

type Fleet struct {
	Backend   backend.Backend
	BaseImage string
}
```

- [ ] **Step 4: Verify it builds and runs**

Run: `go build ./... && go run . --help`
Expected: build succeeds; help shows `flotilla` usage.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum main.go internal/
git commit -m "chore: scaffold flotilla Go module and cobra root"
```

---

## Task 2: Agent profile model + TOML loader

**Files:**
- Create: `flotilla/internal/agent/profile.go`
- Create: `flotilla/internal/agent/builtin/claude.toml`
- Create: `flotilla/internal/agent/builtin/codex.toml`
- Test: `flotilla/internal/agent/profile_test.go`

- [ ] **Step 1: Write the built-in profiles**

`internal/agent/builtin/claude.toml`:
```toml
name = "claude"
install = ""
launch = 'claude --dangerously-skip-permissions -p "{prompt}"'
setup = "builtin:claude"
config_mounts = []
env = ["ANTHROPIC_API_KEY"]
transcript_path = "~/.claude/projects"
egress_allow = ["api.anthropic.com"]
done_signal = "process-exit"
```

`internal/agent/builtin/codex.toml`:
```toml
name = "codex"
install = "npm i -g @openai/codex"
launch = 'codex exec --dangerously-bypass-approvals-and-sandbox "{prompt}"'
setup = "builtin:codex"
config_mounts = ["~/.codex:/root/.codex"]
env = ["OPENAI_API_KEY"]
transcript_path = "~/.codex/sessions"
egress_allow = ["api.openai.com"]
done_signal = "process-exit"
```

- [ ] **Step 2: Write the failing test**

`internal/agent/profile_test.go`:
```go
package agent

import "testing"

func TestBuiltinsIncludeClaudeAndCodex(t *testing.T) {
	got, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins() error: %v", err)
	}
	if _, ok := got["claude"]; !ok {
		t.Errorf("missing claude builtin; have %v", keys(got))
	}
	if _, ok := got["codex"]; !ok {
		t.Errorf("missing codex builtin; have %v", keys(got))
	}
}

func TestParseProfileFields(t *testing.T) {
	p, err := Parse([]byte(`
name = "codex"
launch = 'codex exec "{prompt}"'
env = ["OPENAI_API_KEY"]
egress_allow = ["api.openai.com"]
`))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if p.Name != "codex" {
		t.Errorf("Name = %q, want codex", p.Name)
	}
	if p.Launch != `codex exec "{prompt}"` {
		t.Errorf("Launch = %q", p.Launch)
	}
	if len(p.Env) != 1 || p.Env[0] != "OPENAI_API_KEY" {
		t.Errorf("Env = %v", p.Env)
	}
}

func TestRenderLaunchSubstitutesPrompt(t *testing.T) {
	p := Profile{Launch: `claude -p "{prompt}"`}
	if got := p.RenderLaunch("fix bug"); got != `claude -p "fix bug"` {
		t.Errorf("RenderLaunch = %q", got)
	}
}

func keys(m map[string]Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestBuiltins -v`
Expected: FAIL — `Builtins`, `Parse`, `Profile` undefined.

- [ ] **Step 4: Write minimal implementation**

`internal/agent/profile.go`:
```go
package agent

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed builtin/*.toml
var builtinFS embed.FS

// Profile describes everything that varies between coding agents.
type Profile struct {
	Name           string   `toml:"name"`
	Install        string   `toml:"install"`
	Launch         string   `toml:"launch"`
	Setup          string   `toml:"setup"`
	ConfigMounts   []string `toml:"config_mounts"`
	Env            []string `toml:"env"`
	TranscriptPath string   `toml:"transcript_path"`
	EgressAllow    []string `toml:"egress_allow"`
	DoneSignal     string   `toml:"done_signal"`
}

// Parse decodes a profile from TOML bytes.
func Parse(b []byte) (Profile, error) {
	var p Profile
	if err := toml.Unmarshal(b, &p); err != nil {
		return Profile{}, fmt.Errorf("parse profile: %w", err)
	}
	return p, nil
}

// Builtins returns the embedded built-in profiles keyed by name.
func Builtins() (map[string]Profile, error) {
	out := map[string]Profile{}
	err := fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".toml") {
			return err
		}
		b, err := builtinFS.ReadFile(path)
		if err != nil {
			return err
		}
		p, err := Parse(b)
		if err != nil {
			return err
		}
		out[p.Name] = p
		return nil
	})
	return out, err
}

// RenderLaunch substitutes the {prompt} placeholder in the launch template.
func (p Profile) RenderLaunch(prompt string) string {
	return strings.ReplaceAll(p.Launch, "{prompt}", prompt)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/agent/ -v`
Expected: PASS (all three tests).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/
git commit -m "feat(agent): profile model, TOML loader, embedded claude+codex builtins"
```

---

## Task 3: Agent naming

**Files:**
- Create: `flotilla/internal/naming/naming.go`
- Test: `flotilla/internal/naming/naming_test.go`

- [ ] **Step 1: Write the failing test**

`internal/naming/naming_test.go`:
```go
package naming

import (
	"strings"
	"testing"
)

func TestPickAvoidsTakenNames(t *testing.T) {
	taken := map[string]bool{}
	for _, w := range Words {
		taken[w] = true
	}
	delete(taken, "atlas") // leave exactly one free
	got := Pick(taken)
	if got != "atlas" {
		t.Errorf("Pick = %q, want the only free word 'atlas'", got)
	}
}

func TestPickPrefersUniqueFirstLetter(t *testing.T) {
	taken := map[string]bool{}
	// Take everything not starting with 'b'.
	for _, w := range Words {
		if !strings.HasPrefix(w, "b") {
			taken[w] = true
		}
	}
	got := Pick(taken)
	if !strings.HasPrefix(got, "b") {
		t.Errorf("Pick = %q, want a free 'b' word", got)
	}
}

func TestPickFallsBackToSuffixWhenAllTaken(t *testing.T) {
	taken := map[string]bool{}
	for _, w := range Words {
		taken[w] = true
	}
	got := Pick(taken)
	if !strings.Contains(got, "-") {
		t.Errorf("Pick = %q, want a suffixed fallback when all words taken", got)
	}
	if taken[got] {
		t.Errorf("Pick returned a taken name %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/naming/ -v`
Expected: FAIL — `Words`, `Pick` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/naming/naming.go`:
```go
package naming

import "fmt"

// Words is a curated word list for instance names (nautical/exploration themed).
var Words = []string{
	"atlas", "beacon", "compass", "delta", "echo", "fathom", "galley", "harbor",
	"isle", "jetty", "keel", "lagoon", "mast", "nadir", "ozone", "prow",
	"quay", "reef", "sextant", "tide", "umbra", "vector", "wake", "yardarm", "zephyr",
}

// Pick returns a free name, preferring a word whose first letter is not yet used.
// taken maps already-used names to true. When every word is taken it appends a
// numeric suffix until a free name is found.
func Pick(taken map[string]bool) string {
	usedInitials := map[byte]bool{}
	for name := range taken {
		if len(name) > 0 {
			usedInitials[name[0]] = true
		}
	}
	// First pass: free word with an unused initial.
	for _, w := range Words {
		if !taken[w] && !usedInitials[w[0]] {
			return w
		}
	}
	// Second pass: any free word.
	for _, w := range Words {
		if !taken[w] {
			return w
		}
	}
	// Fallback: suffix the first word until free.
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", Words[0], i)
		if !taken[cand] {
			return cand
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/naming/ -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add internal/naming/
git commit -m "feat(naming): curated word-list agent naming with unique-initial preference"
```

---

## Task 4: Backend interface + types + fake backend

**Files:**
- Modify: `flotilla/internal/backend/backend.go` (replace the Task 1 stub)
- Create: `flotilla/internal/backend/fake.go`
- Test: `flotilla/internal/backend/fake_test.go`

- [ ] **Step 1: Write the full interface and types**

Replace `internal/backend/backend.go` entirely:
```go
package backend

import (
	"context"
	"time"
)

// Label keys applied to every flotilla-managed container.
const (
	LabelAgent   = "flotilla.agent"
	LabelRepo    = "flotilla.repo"
	LabelCreated = "flotilla.created"
	LabelHost    = "flotilla.host"
)

// Mount is a host->container bind mount.
type Mount struct {
	Source string
	Target string
}

// CreateOpts describes a container to create.
type CreateOpts struct {
	Name    string
	Image   string
	Cmd     []string
	Workdir string
	Mounts  []Mount
	Env     map[string]string
	Labels  map[string]string
}

// Container is a flotilla-managed container as seen by a backend.
type Container struct {
	ID      string
	Name    string
	Repo    string
	Status  string // "running" | "exited"
	Created time.Time
	Labels  map[string]string
}

// AttachInfo tells a client how to attach to a container.
type AttachInfo struct {
	ContainerID string
	DockerExec  string
	VSCode      string
}

// Backend abstracts the compute substrate (local Docker for v1).
type Backend interface {
	Create(ctx context.Context, opts CreateOpts) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Exec(ctx context.Context, id string, cmd []string) error
	List(ctx context.Context, labelFilter map[string]string) ([]Container, error)
	AttachInfo(ctx context.Context, id string) (AttachInfo, error)
}

// NewDocker is implemented in docker.go (Task 5).
```

- [ ] **Step 2: Write the failing test for the fake**

`internal/backend/fake_test.go`:
```go
package backend

import (
	"context"
	"testing"
)

func TestFakeLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	id, err := f.Create(ctx, CreateOpts{
		Name:   "atlas",
		Image:  "ubuntu:24.04",
		Cmd:    []string{"sleep", "infinity"},
		Labels: map[string]string{LabelAgent: "atlas", LabelRepo: "git@x:r.git"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := f.List(ctx, map[string]string{LabelAgent: "atlas"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("List = %+v, want one running container", got)
	}
	if err := f.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got, _ = f.List(ctx, nil)
	if got[0].Status != "exited" {
		t.Errorf("after Stop status = %q, want exited", got[0].Status)
	}
	if err := f.Remove(ctx, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = f.List(ctx, nil)
	if len(got) != 0 {
		t.Errorf("after Remove List = %+v, want empty", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/backend/ -run TestFake -v`
Expected: FAIL — `NewFake` undefined.

- [ ] **Step 4: Write the fake implementation**

`internal/backend/fake.go`:
```go
package backend

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Fake is an in-memory Backend for unit tests.
type Fake struct {
	mu    sync.Mutex
	seq   int
	items map[string]*Container
	// ExecCalls records (id, cmd) for assertions.
	ExecCalls [][]string
}

func NewFake() *Fake { return &Fake{items: map[string]*Container{}} }

func (f *Fake) Create(_ context.Context, o CreateOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("fake-%d", f.seq)
	f.items[id] = &Container{
		ID:      id,
		Name:    o.Labels[LabelAgent],
		Repo:    o.Labels[LabelRepo],
		Status:  "created",
		Created: time.Unix(int64(f.seq), 0).UTC(),
		Labels:  o.Labels,
	}
	return id, nil
}

func (f *Fake) Start(_ context.Context, id string) error { return f.setStatus(id, "running") }
func (f *Fake) Stop(_ context.Context, id string) error  { return f.setStatus(id, "exited") }

func (f *Fake) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return fmt.Errorf("no such container %q", id)
	}
	delete(f.items, id)
	return nil
}

func (f *Fake) Exec(_ context.Context, id string, cmd []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ExecCalls = append(f.ExecCalls, append([]string{id}, cmd...))
	return nil
}

func (f *Fake) List(_ context.Context, filter map[string]string) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Container
	for _, c := range f.items {
		if matches(c.Labels, filter) {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *Fake) AttachInfo(_ context.Context, id string) (AttachInfo, error) {
	return AttachInfo{
		ContainerID: id,
		DockerExec:  "docker exec -it " + id + " bash",
		VSCode:      "Dev Containers: Attach to Running Container -> " + id,
	}, nil
}

func (f *Fake) setStatus(id, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.items[id]
	if !ok {
		return fmt.Errorf("no such container %q", id)
	}
	c.Status = status
	return nil
}

func matches(labels, filter map[string]string) bool {
	for k, v := range filter {
		if labels[k] != v {
			return false
		}
	}
	return true
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/backend/ -run TestFake -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/backend.go internal/backend/fake.go internal/backend/fake_test.go
git commit -m "feat(backend): Backend interface, shared types, in-memory fake"
```

---

## Task 5: Docker backend (shells out to `docker`)

**Files:**
- Create: `flotilla/internal/backend/docker.go`
- Test: `flotilla/internal/backend/docker_test.go` (integration; skips without docker)

- [ ] **Step 1: Write the integration test (skips when docker absent)**

`internal/backend/docker_test.go`:
```go
package backend

import (
	"context"
	"os/exec"
	"testing"
)

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

func TestDockerLifecycleIntegration(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available; skipping integration test")
	}
	ctx := context.Background()
	d := NewDocker()
	id, err := d.Create(ctx, CreateOpts{
		Name:   "flotilla-test-atlas",
		Image:  "alpine:3.20",
		Cmd:    []string{"sleep", "120"},
		Labels: map[string]string{LabelAgent: "atlas-test", LabelRepo: "r"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer d.Remove(ctx, id) //nolint:errcheck
	if err := d.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := d.List(ctx, map[string]string{LabelAgent: "atlas-test"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("List = %+v, want one running", got)
	}
	if err := d.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/ -run TestDocker -v`
Expected: FAIL to compile — `NewDocker` returns `nil`/undefined methods. (If docker is absent the test would skip, but it won't compile yet — that's the failing state.)

- [ ] **Step 3: Write the Docker implementation**

`internal/backend/docker.go`:
```go
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type dockerBackend struct{}

// NewDocker returns a Backend backed by the local `docker` CLI.
func NewDocker() Backend { return &dockerBackend{} }

func run(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func (d *dockerBackend) Create(ctx context.Context, o CreateOpts) (string, error) {
	args := []string{"create", "--name", o.Name}
	for k, v := range o.Labels {
		args = append(args, "--label", k+"="+v)
	}
	for k, v := range o.Env {
		args = append(args, "--env", k+"="+v)
	}
	for _, m := range o.Mounts {
		args = append(args, "--volume", m.Source+":"+m.Target)
	}
	if o.Workdir != "" {
		args = append(args, "--workdir", o.Workdir)
	}
	args = append(args, o.Image)
	args = append(args, o.Cmd...)
	return run(ctx, args...)
}

func (d *dockerBackend) Start(ctx context.Context, id string) error {
	_, err := run(ctx, "start", id)
	return err
}

func (d *dockerBackend) Stop(ctx context.Context, id string) error {
	_, err := run(ctx, "stop", id)
	return err
}

func (d *dockerBackend) Remove(ctx context.Context, id string) error {
	_, err := run(ctx, "rm", "-f", id)
	return err
}

func (d *dockerBackend) Exec(ctx context.Context, id string, cmd []string) error {
	_, err := run(ctx, append([]string{"exec", id}, cmd...)...)
	return err
}

// dockerPS is the subset of `docker inspect`/`ps` fields we read.
type psLine struct {
	ID      string            `json:"ID"`
	Names   string            `json:"Names"`
	State   string            `json:"State"`
	Created string            `json:"CreatedAt"`
	Labels  string            `json:"Labels"`
}

func (d *dockerBackend) List(ctx context.Context, filter map[string]string) ([]Container, error) {
	args := []string{"ps", "-a", "--no-trunc", "--format", "{{json .}}"}
	for k, v := range filter {
		args = append(args, "--filter", "label="+k+"="+v)
	}
	// Always scope to flotilla-managed containers.
	if _, ok := filter[LabelAgent]; !ok {
		args = append(args, "--filter", "label="+LabelAgent)
	}
	out, err := run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var result []Container
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p psLine
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return nil, fmt.Errorf("parse ps line: %w", err)
		}
		labels := parseLabels(p.Labels)
		status := "exited"
		if p.State == "running" {
			status = "running"
		}
		result = append(result, Container{
			ID:      p.ID,
			Name:    labels[LabelAgent],
			Repo:    labels[LabelRepo],
			Status:  status,
			Created: parseDockerTime(p.Created),
			Labels:  labels,
		})
	}
	return result, nil
}

func (d *dockerBackend) AttachInfo(_ context.Context, id string) (AttachInfo, error) {
	return AttachInfo{
		ContainerID: id,
		DockerExec:  "docker exec -it " + id + " bash",
		VSCode:      "Run 'Dev Containers: Attach to Running Container' and pick " + id,
	}, nil
}

func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

func parseDockerTime(s string) time.Time {
	// docker ps CreatedAt: "2026-06-14 09:30:00 +0000 UTC"
	if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
```

- [ ] **Step 4: Run test to verify it passes (or skips cleanly)**

Run: `go test ./internal/backend/ -run TestDocker -v`
Expected: PASS if docker is running; SKIP with "docker not available" otherwise. Either is acceptable; it must compile and not FAIL.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/docker.go internal/backend/docker_test.go
git commit -m "feat(backend): local Docker backend via docker CLI"
```

---

## Task 6: Engine-side git clone

**Files:**
- Create: `flotilla/internal/gitops/clone.go`
- Test: `flotilla/internal/gitops/clone_test.go` (hermetic — uses a local bare repo as the "remote")

- [ ] **Step 1: Write the hermetic test**

`internal/gitops/clone_test.go`:
```go
package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeBareRepo creates a local bare repo with one commit and returns its path.
func makeBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	bare := filepath.Join(dir, "remote.git")
	mustRun(t, "", "git", "init", "-q", work)
	mustRun(t, work, "git", "config", "user.email", "t@example.com")
	mustRun(t, work, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "commit", "-q", "-m", "init")
	mustRun(t, "", "git", "clone", "-q", "--bare", work, bare)
	return bare
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
}

func TestCloneCheckoutsRepo(t *testing.T) {
	bare := makeBareRepo(t)
	dest := filepath.Join(t.TempDir(), "agentwork")
	if err := Clone(context.Background(), bare, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("expected README.md in clone: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gitops/ -v`
Expected: FAIL — `Clone` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/gitops/clone.go`:
```go
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Clone does a fresh engine-side clone of repoURL into dest. The engine holds
// the git credentials; the container never does.
func Clone(ctx context.Context, repoURL, dest string) error {
	var errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", repoURL, dest)
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w: %s", repoURL, err, errb.String())
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gitops/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitops/
git commit -m "feat(gitops): engine-side fresh clone"
```

---

## Task 7: Fleet.Spawn

**Files:**
- Modify: `flotilla/internal/fleet/fleet.go` (replace the Task 1 stub)
- Test: `flotilla/internal/fleet/fleet_test.go`

- [ ] **Step 1: Write the failing test**

`internal/fleet/fleet_test.go`:
```go
package fleet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

func bareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	bare := filepath.Join(dir, "remote.git")
	run := func(d, n string, a ...string) {
		c := exec.Command(n, a...)
		c.Dir = d
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", n, a, err, out)
		}
	}
	run("", "git", "init", "-q", work)
	run(work, "git", "config", "user.email", "t@e.com")
	run(work, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644)
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-q", "-m", "init")
	run("", "git", "clone", "-q", "--bare", work, bare)
	return bare
}

func TestSpawnClonesAndCreatesContainer(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	a, err := f.Spawn(context.Background(), bareRepo(t), prof, "do the thing")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if a.Name == "" || a.ID == "" {
		t.Fatalf("Spawn returned empty agent: %+v", a)
	}
	// The clone must exist on disk.
	if _, err := os.Stat(filepath.Join(f.WorkRoot, a.Name, "f.txt")); err != nil {
		t.Errorf("expected cloned file for agent: %v", err)
	}
	// The container must be labeled and running.
	got, _ := fake.List(context.Background(), map[string]string{backend.LabelAgent: a.Name})
	if len(got) != 1 {
		t.Fatalf("List = %+v, want 1", got)
	}
	if got[0].Status != "running" {
		t.Errorf("status = %q, want running", got[0].Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestSpawn -v`
Expected: FAIL — `Fleet.Spawn`, `Fleet.WorkRoot`, `Agent` undefined.

- [ ] **Step 3: Write the implementation**

Replace `internal/fleet/fleet.go`:
```go
package fleet

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/gitops"
	"github.com/mickzijdel/flotilla/internal/naming"
)

// Agent is a flotilla-managed agent as the engine sees it.
type Agent struct {
	Name    string
	Repo    string
	Status  string
	Created time.Time
	ID      string
}

// Fleet orchestrates agents over a Backend.
type Fleet struct {
	Backend   backend.Backend
	BaseImage string
	WorkRoot  string // host dir holding per-agent clones; defaults under ~/.flotilla
}

func (f *Fleet) workRoot() string {
	if f.WorkRoot != "" {
		return f.WorkRoot
	}
	return filepath.Join(homeDir(), ".flotilla", "work")
}

// Spawn clones repoURL engine-side, then creates+starts a container that runs
// the profile's launch command on the mounted clone.
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

	const containerWork = "/workspace"
	id, err := f.Backend.Create(ctx, backend.CreateOpts{
		Name:    "flotilla-" + name,
		Image:   f.BaseImage,
		Cmd:     []string{"sh", "-c", prof.RenderLaunch(prompt)},
		Workdir: containerWork,
		Mounts:  []backend.Mount{{Source: dest, Target: containerWork}},
		Labels: map[string]string{
			backend.LabelAgent:   name,
			backend.LabelRepo:    repoURL,
			backend.LabelCreated: time.Now().UTC().Format(time.RFC3339),
			backend.LabelHost:    "local",
		},
	})
	if err != nil {
		return Agent{}, fmt.Errorf("create container: %w", err)
	}
	if err := f.Backend.Start(ctx, id); err != nil {
		return Agent{}, fmt.Errorf("start container: %w", err)
	}
	return Agent{Name: name, Repo: repoURL, Status: "running", Created: time.Now().UTC(), ID: id}, nil
}
```

Add `internal/fleet/home.go`:
```go
package fleet

import "os"

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
```

- [ ] **Step 4: Add a temporary List stub so the package compiles**

Append to `internal/fleet/fleet.go` (replaced fully in Task 8):
```go
// List is implemented in Task 8.
func (f *Fleet) List(ctx context.Context) ([]Agent, error) {
	cs, err := f.Backend.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(cs))
	for _, c := range cs {
		out = append(out, Agent{Name: c.Name, Repo: c.Repo, Status: c.Status, Created: c.Created, ID: c.ID})
	}
	return out, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run TestSpawn -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/
git commit -m "feat(fleet): Spawn — engine clone + profile-driven container"
```

---

## Task 8: Fleet.List (finalize) + JSON marshalling

**Files:**
- Modify: `flotilla/internal/fleet/fleet.go` (the List from Task 7 is now the real one; add a test)
- Test: `flotilla/internal/fleet/list_test.go`

- [ ] **Step 1: Write the failing test**

`internal/fleet/list_test.go`:
```go
package fleet

import (
	"context"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestListMapsContainersToAgents(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{
		Labels: map[string]string{backend.LabelAgent: "atlas", backend.LabelRepo: "r1"},
	})
	_ = fake.Start(ctx, id)

	f := &Fleet{Backend: fake}
	got, err := f.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Name != "atlas" || got[0].Repo != "r1" || got[0].Status != "running" {
		t.Errorf("agent = %+v", got[0])
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run TestList -v`
Expected: PASS (List was implemented in Task 7; this locks its behavior).

- [ ] **Step 3: Confirm Agent fields are JSON-friendly**

The CLI (Task 11) marshals `[]Agent` with `encoding/json`. Add explicit tags to keep the JSON stable. In `internal/fleet/fleet.go`, change the `Agent` struct to:
```go
type Agent struct {
	Name    string    `json:"name"`
	Repo    string    `json:"repo"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
	ID      string    `json:"id"`
}
```

- [ ] **Step 4: Run the whole fleet package**

Run: `go test ./internal/fleet/ -v`
Expected: PASS (Spawn + List tests).

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/
git commit -m "feat(fleet): finalize List + stable JSON tags on Agent"
```

---

## Task 9: Fleet.Attach

**Files:**
- Modify: `flotilla/internal/fleet/fleet.go` (add `Attach`)
- Test: `flotilla/internal/fleet/attach_test.go`

- [ ] **Step 1: Write the failing test**

`internal/fleet/attach_test.go`:
```go
package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestAttachReturnsInfoForNamedAgent(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})
	_ = fake.Start(ctx, id)

	f := &Fleet{Backend: fake}
	info, err := f.Attach(ctx, "atlas")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !strings.Contains(info.DockerExec, id) {
		t.Errorf("DockerExec = %q, want it to mention %q", info.DockerExec, id)
	}
}

func TestAttachUnknownAgentErrors(t *testing.T) {
	f := &Fleet{Backend: backend.NewFake()}
	if _, err := f.Attach(context.Background(), "nope"); err == nil {
		t.Error("expected error for unknown agent")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestAttach -v`
Expected: FAIL — `Fleet.Attach` undefined.

- [ ] **Step 3: Write the implementation**

Append to `internal/fleet/fleet.go`:
```go
// resolve finds the backend container ID for an agent name.
func (f *Fleet) resolve(ctx context.Context, name string) (backend.Container, error) {
	cs, err := f.Backend.List(ctx, map[string]string{backend.LabelAgent: name})
	if err != nil {
		return backend.Container{}, err
	}
	if len(cs) == 0 {
		return backend.Container{}, fmt.Errorf("no agent named %q", name)
	}
	return cs[0], nil
}

// Attach returns attach info for a named agent.
func (f *Fleet) Attach(ctx context.Context, name string) (backend.AttachInfo, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return backend.AttachInfo{}, err
	}
	return f.Backend.AttachInfo(ctx, c.ID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run TestAttach -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/
git commit -m "feat(fleet): Attach resolves agent name to backend attach info"
```

---

## Task 10: Fleet.Stop and Fleet.Remove

**Files:**
- Modify: `flotilla/internal/fleet/fleet.go` (add `Stop`, `Remove`)
- Test: `flotilla/internal/fleet/stop_test.go`

- [ ] **Step 1: Write the failing test**

`internal/fleet/stop_test.go`:
```go
package fleet

import (
	"context"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestStopThenRemove(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})
	_ = fake.Start(ctx, id)

	f := &Fleet{Backend: fake}
	if err := f.Stop(ctx, "atlas"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	cs, _ := fake.List(ctx, nil)
	if cs[0].Status != "exited" {
		t.Errorf("status = %q, want exited", cs[0].Status)
	}
	if err := f.Remove(ctx, "atlas"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	cs, _ = fake.List(ctx, nil)
	if len(cs) != 0 {
		t.Errorf("after Remove len = %d, want 0", len(cs))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestStop -v`
Expected: FAIL — `Fleet.Stop`, `Fleet.Remove` undefined.

- [ ] **Step 3: Write the implementation**

Append to `internal/fleet/fleet.go`:
```go
// Stop stops a named agent's container.
func (f *Fleet) Stop(ctx context.Context, name string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	return f.Backend.Stop(ctx, c.ID)
}

// Remove force-removes a named agent's container.
func (f *Fleet) Remove(ctx context.Context, name string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	return f.Backend.Remove(ctx, c.ID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run TestStop -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/
git commit -m "feat(fleet): Stop and Remove by agent name"
```

---

## Task 11: CLI wiring (spawn/list/attach/stop/rm/agents)

**Files:**
- Modify: `flotilla/internal/cli/cli.go` (replace the Task 1 stub)
- Test: `flotilla/internal/cli/cli_test.go`

- [ ] **Step 1: Write the failing test**

`internal/cli/cli_test.go`:
```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func TestListJSONOutput(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas", backend.LabelRepo: "r1"}})
	_ = fake.Start(ctx, id)

	root := BuildRoot(&fleet.Fleet{Backend: fake})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"list", "--json"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got []fleet.Agent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Name != "atlas" {
		t.Errorf("got %+v", got)
	}
}

func TestAgentsListsBuiltins(t *testing.T) {
	root := BuildRoot(&fleet.Fleet{Backend: backend.NewFake()})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"agents"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("claude")) || !bytes.Contains(buf.Bytes(), []byte("codex")) {
		t.Errorf("agents output missing builtins: %s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -v`
Expected: FAIL — `BuildRoot` is the stub; `list`/`agents` subcommands missing.

- [ ] **Step 3: Write the implementation**

Replace `internal/cli/cli.go`:
```go
package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

// BuildRoot wires the CLI against a Fleet.
func BuildRoot(f *fleet.Fleet) *cobra.Command {
	root := &cobra.Command{Use: "flotilla", Short: "Manage a fleet of autonomous coding agents"}
	root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), agentsCmd())
	return root
}

func spawnCmd(f *fleet.Fleet) *cobra.Command {
	var agentName, prompt string
	c := &cobra.Command{
		Use:   "spawn <repo>",
		Short: "Clone a repo and start an agent on it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			builtins, err := agent.Builtins()
			if err != nil {
				return err
			}
			prof, ok := builtins[agentName]
			if !ok {
				return fmt.Errorf("unknown agent %q (try: flotilla agents)", agentName)
			}
			a, err := f.Spawn(cmd.Context(), args[0], prof, prompt)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", a.Name, a.Status, a.ID)
			return nil
		},
	}
	c.Flags().StringVar(&agentName, "agent", "claude", "agent profile to run")
	c.Flags().StringVar(&prompt, "prompt", "", "task prompt for the agent")
	return c
}

func listCmd(f *fleet.Fleet) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the fleet",
		RunE: func(cmd *cobra.Command, _ []string) error {
			agents, err := f.List(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				return enc.Encode(agents)
			}
			for _, a := range agents {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", a.Name, a.Status, a.Repo)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

func attachCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <agent>",
		Short: "Print attach info for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := f.Attach(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), info.DockerExec)
			fmt.Fprintln(cmd.OutOrStdout(), info.VSCode)
			return nil
		},
	}
}

func stopCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <agent>",
		Short: "Stop an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.Stop(cmd.Context(), args[0])
		},
	}
}

func rmCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <agent>",
		Short: "Remove an agent's container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.Remove(cmd.Context(), args[0])
		},
	}
}

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List available agent profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			builtins, err := agent.Builtins()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(builtins))
			for n := range builtins {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Fprintln(cmd.OutOrStdout(), n)
			}
			return nil
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -v`
Expected: PASS (both tests).

- [ ] **Step 5: Run the full suite + build**

Run: `go build ./... && go test ./...`
Expected: all packages PASS (docker integration test SKIPs if no docker).

- [ ] **Step 6: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): wire spawn/list/attach/stop/rm/agents with --json"
```

---

## Task 12: End-to-end smoke (manual, requires Docker)

**Files:** none (manual verification)

- [ ] **Step 1: Build the binary**

Run: `go build -o bin/flotilla . && ./bin/flotilla agents`
Expected: prints `claude` and `codex`.

- [ ] **Step 2: Spawn a stub agent against a public repo (Docker required)**

> This uses the default `claude` profile, whose launch command needs Claude installed; for a
> credential-free smoke we override with a trivial repo and observe lifecycle, then stop. If you
> want a no-Claude smoke, temporarily `flotilla spawn <repo> --agent codex --prompt "echo hi"` is
> still gated on the agent CLI; instead verify lifecycle with `list`/`attach`/`stop` which do not
> depend on the agent actually running.

Run:
```bash
./bin/flotilla spawn https://github.com/octocat/Hello-World.git --prompt "noop"
./bin/flotilla list
```
Expected: `spawn` prints a name/status/id; `list` shows one agent. (The container's command may exit immediately if the agent CLI is absent — that is fine for this skeleton; status will read `exited`.)

- [ ] **Step 3: Attach info + cleanup**

Run:
```bash
NAME=$(./bin/flotilla list --json | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["name"])')
./bin/flotilla attach "$NAME"
./bin/flotilla stop "$NAME"
./bin/flotilla rm "$NAME"
./bin/flotilla list
```
Expected: `attach` prints a `docker exec` line + VS Code hint; after `rm`, `list` is empty.

- [ ] **Step 4: Commit a short README note**

Add a `## Status` line to a new `flotilla/README.md` noting the walking skeleton is functional and pointing at the spec + this plan. Commit:
```bash
git add README.md
git commit -m "docs: note walking-skeleton status"
```

---

## Self-Review (completed during authoring)

- **Spec coverage (this slice):** substrate=Docker (Task 5), engine-side clone/no-creds (Task 6–7),
  agent-agnostic profiles incl. claude+codex (Task 2, used in Task 7/11), stateless list via labels
  (Task 5/8), naming (Task 3), CLI + JSON + `agents` (Task 11), compute-backend seam (Task 4).
  Deferred items are listed under Scope and are out of this plan by design.
- **Placeholder scan:** none — every step has complete code or an exact command + expected output.
- **Type consistency:** `Profile`, `CreateOpts`, `Container`, `AttachInfo`, `Backend`, `Agent`,
  label consts, and `Fleet{Backend,BaseImage,WorkRoot}` are defined once (Tasks 2/4/7) and reused
  verbatim. `Fleet.List` is introduced as a real method in Task 7 (the package must compile after
  Spawn) and locked by tests in Task 8 — not redefined.
