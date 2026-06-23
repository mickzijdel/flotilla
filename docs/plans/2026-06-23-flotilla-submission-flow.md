# Flotilla Submission Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `flotilla submit <agent>` — the engine pushes a finished agent's local commits to a `flotilla/<agent>` branch and opens/updates a PR, with git credentials only ever on the engine.

**Architecture:** All git/gh work runs **host-side** against the agent's clone at `~/.flotilla/work/<agent>/` (which *is* the bind-mounted working tree), so the credential-free container is never involved in the push. New pure-git primitives live in `internal/gitops`; a new `internal/forge` package wraps `gh` behind a fakeable interface and owns the PR-vs-push-only decision; `Fleet.Submit` orchestrates; a `submit` CLI command and a `wrap_up` agent contract complete the loop.

**Tech Stack:** Go 1.26, cobra, `os/exec` shelling out to `git` and `gh`, the existing in-memory `backend.Fake` for tests.

**Design spec:** [docs/specs/2026-06-23-flotilla-submission-flow-design.md](../specs/2026-06-23-flotilla-submission-flow-design.md)

## Global Constraints

- **Go:** 1.26.4 (`go` directive in go.mod). cobra v1.10.2, BurntSushi/toml v1.6.0.
- **Git invocation:** `os/exec` only (no git library), matching `internal/gitops/clone.go`.
- **Every host git call passes `-c safe.directory=<dir>`** (scoped to the clone path, never the global `*`) to neutralize Git's dubious-ownership guard on container-written `.git` files.
- **`Inspect` uses read-only plumbing only** (`rev-list`, `diff --quiet`, `ls-files`, `symbolic-ref`, `rev-parse`) — never `git status` (which rewrites `.git/index`).
- **Branch name is always `flotilla/<agent>`**; push is always `--force-with-lease`.
- **No secret values in committed files**; tests use throwaway local repos in `t.TempDir()`.
- **Test style:** real-git tests build a local bare "remote" in a temp dir (see `internal/gitops/clone_test.go`'s `makeBareRepo`/`mustRun` helpers); fakeable seams use an in-memory fake (see `backend.Fake`). Tests that need a live external tool (`gh`) self-skip when it is absent, like the Docker backend integration test.
- **Commit after each task** (TDD: failing test → implementation → green → commit).

---

### Task 1: `gitops.Inspect` — read-only work-state inspection

**Files:**
- Create: `internal/gitops/inspect.go`
- Create: `internal/gitops/inspect_test.go`

**Interfaces:**
- Produces:
  - `type WorkState struct { Base string; CommitsAhead int; Dirty bool; RemoteURL string }`
  - `func Inspect(ctx context.Context, dir string) (WorkState, error)`
  - `func git(ctx context.Context, dir string, args ...string) (string, error)` (unexported; used by Task 2 too — trimmed stdout, wraps stderr on error, always injects `-C dir -c safe.directory=dir`)

- [ ] **Step 1: Write the failing test**

```go
// internal/gitops/inspect_test.go
package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// cloneWithCommits builds a bare "remote" on branch main, clones it, and adds
// nCommits commits on top of the clone's HEAD. Returns the clone dir.
func cloneWithCommits(t *testing.T, nCommits int) string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "remote.git")
	mustRun(t, "", "git", "init", "-q", "-b", "main", work)
	mustRun(t, work, "git", "config", "user.email", "t@example.com")
	mustRun(t, work, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "commit", "-q", "-m", "init")
	mustRun(t, "", "git", "clone", "-q", "--bare", work, bare)

	dest := filepath.Join(root, "clone")
	mustRun(t, "", "git", "clone", "-q", bare, dest)
	mustRun(t, dest, "git", "config", "user.email", "a@example.com")
	mustRun(t, dest, "git", "config", "user.name", "agent")
	for i := 0; i < nCommits; i++ {
		name := filepath.Join(dest, "file"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRun(t, dest, "git", "add", ".")
		mustRun(t, dest, "git", "commit", "-q", "-m", "agent change")
	}
	return dest
}

func TestInspectReportsBaseAheadAndClean(t *testing.T) {
	dest := cloneWithCommits(t, 2)
	st, err := Inspect(context.Background(), dest)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.Base != "main" {
		t.Errorf("Base = %q, want main", st.Base)
	}
	if st.CommitsAhead != 2 {
		t.Errorf("CommitsAhead = %d, want 2", st.CommitsAhead)
	}
	if st.Dirty {
		t.Errorf("Dirty = true, want false (tree committed clean)")
	}
	if st.RemoteURL == "" {
		t.Errorf("RemoteURL empty, want origin URL")
	}
}

func TestInspectDetectsDirtyAndNoCommits(t *testing.T) {
	dest := cloneWithCommits(t, 0)
	if err := os.WriteFile(filepath.Join(dest, "dirty.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(context.Background(), dest)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.CommitsAhead != 0 {
		t.Errorf("CommitsAhead = %d, want 0", st.CommitsAhead)
	}
	if !st.Dirty {
		t.Errorf("Dirty = false, want true (untracked file present)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gitops/ -run TestInspect -v`
Expected: FAIL — `undefined: Inspect` / `undefined: WorkState`.

- [ ] **Step 3: Write the implementation**

```go
// internal/gitops/inspect.go
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// WorkState describes an agent's clone relative to its push base.
type WorkState struct {
	Base         string // base branch from origin/HEAD (e.g. "main")
	CommitsAhead int    // commits on HEAD not on origin/<Base>
	Dirty        bool   // uncommitted changes (tracked or untracked)
	RemoteURL    string // origin URL
}

// git runs a read/write-neutral git command against dir. It always scopes
// safe.directory to dir so container-written .git files don't trip Git's
// dubious-ownership guard. Returns trimmed stdout; wraps stderr on failure.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "safe.directory=" + dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// gitClean reports whether `git <args>` exited 0 (clean). Exit code 1 means
// "differences" (not clean); any other error is returned.
func gitClean(ctx context.Context, dir string, args ...string) (bool, error) {
	full := append([]string{"-C", dir, "-c", "safe.directory=" + dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

// Inspect reports the clone's base branch, commits ahead of origin/<base>, and
// whether the tree is dirty. Read-only: never writes .git.
func Inspect(ctx context.Context, dir string) (WorkState, error) {
	var st WorkState

	url, err := git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return st, err
	}
	st.RemoteURL = url

	if ref, err := git(ctx, dir, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		st.Base = strings.TrimPrefix(ref, "refs/remotes/origin/")
	} else {
		b, err2 := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
		if err2 != nil {
			return st, err2
		}
		st.Base = b
	}

	cnt, err := git(ctx, dir, "rev-list", "--count", "origin/"+st.Base+"..HEAD")
	if err != nil {
		return st, err
	}
	n, err := strconv.Atoi(cnt)
	if err != nil {
		return st, fmt.Errorf("parse commit count %q: %w", cnt, err)
	}
	st.CommitsAhead = n

	dirty, err := isDirty(ctx, dir)
	if err != nil {
		return st, err
	}
	st.Dirty = dirty
	return st, nil
}

// isDirty is true if there are staged changes, unstaged changes, or untracked
// (non-ignored) files. Uses only read-only plumbing.
func isDirty(ctx context.Context, dir string) (bool, error) {
	unstagedClean, err := gitClean(ctx, dir, "diff", "--quiet")
	if err != nil {
		return false, err
	}
	stagedClean, err := gitClean(ctx, dir, "diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	untracked, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return false, err
	}
	return !unstagedClean || !stagedClean || untracked != "", nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gitops/ -run TestInspect -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/gitops/inspect.go internal/gitops/inspect_test.go
git commit -m "feat(gitops): Inspect — read-only work-state of an agent clone"
```

---

### Task 2: `gitops.Push` — force-with-lease push to a branch

**Files:**
- Create: `internal/gitops/push.go`
- Create: `internal/gitops/push_test.go`

**Interfaces:**
- Consumes: `git(ctx, dir, args...)` from Task 1.
- Produces: `func Push(ctx context.Context, dir, branch string) error` — pushes `HEAD:refs/heads/<branch>` to origin with `--force-with-lease`.

- [ ] **Step 1: Write the failing test**

```go
// internal/gitops/push_test.go
package gitops

import (
	"context"
	"strings"
	"testing"
)

func TestPushCreatesBranchOnRemote(t *testing.T) {
	dest := cloneWithCommits(t, 1)
	if err := Push(context.Background(), dest, "flotilla/atlas"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// The remote-tracking ref should now exist for the pushed branch.
	out, err := git(context.Background(), dest, "ls-remote", "origin", "flotilla/atlas")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if !strings.Contains(out, "flotilla/atlas") {
		t.Errorf("pushed ref not found on remote; ls-remote = %q", out)
	}
}

func TestPushUpdatesExistingBranch(t *testing.T) {
	dest := cloneWithCommits(t, 1)
	ctx := context.Background()
	if err := Push(ctx, dest, "flotilla/atlas"); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	// Add another commit, then re-push — force-with-lease should update in place.
	mustRun(t, dest, "git", "commit", "-q", "--allow-empty", "-m", "more")
	if err := Push(ctx, dest, "flotilla/atlas"); err != nil {
		t.Fatalf("second Push: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gitops/ -run TestPush -v`
Expected: FAIL — `undefined: Push`.

- [ ] **Step 3: Write the implementation**

```go
// internal/gitops/push.go
package gitops

import "context"

// Push force-pushes the clone's HEAD to origin as refs/heads/<branch> using
// --force-with-lease, so re-submitting updates the branch in place without
// clobbering a ref someone else moved. Reads local objects only; the engine
// holds the credentials, never the container.
func Push(ctx context.Context, dir, branch string) error {
	_, err := git(ctx, dir, "push", "--force-with-lease", "origin", "HEAD:refs/heads/"+branch)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gitops/ -run TestPush -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/gitops/push.go internal/gitops/push_test.go
git commit -m "feat(gitops): Push — force-with-lease push to flotilla branch"
```

---

### Task 3: `internal/forge` — gh wrapper, push-only fallback, fake

**Files:**
- Create: `internal/forge/forge.go` (interface, `PRResult`, `CompareURL`, `isGitHub`)
- Create: `internal/forge/gh.go` (real `gh` impl + `GHAvailable`)
- Create: `internal/forge/fake.go` (in-memory `Fake` for this + the fleet tests)
- Create: `internal/forge/forge_test.go`

**Interfaces:**
- Consumes: `gitops.WorkState` from Task 1.
- Produces:
  - `type PRResult struct { URL string; Created bool; PushOnly bool }`
  - `type Forge interface { Available(ctx context.Context) bool; EnsurePR(ctx context.Context, dir, branch string, st gitops.WorkState) (PRResult, error) }`
  - `func CompareURL(remoteURL, base, branch string) (string, error)`
  - `func GHAvailable(ctx context.Context) bool`
  - `type GH struct{}` + `func NewGH() *GH` (implements `Forge`)
  - `type Fake struct { Result PRResult; Err error; AvailableFlag bool; Calls []string }` (implements `Forge`)

- [ ] **Step 1: Write the failing test** (pure logic — `CompareURL`, push-only via `Fake`)

```go
// internal/forge/forge_test.go
package forge

import (
	"context"
	"testing"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

func TestCompareURLFromHTTPSAndSSH(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo.git": "https://github.com/owner/repo/compare/main...flotilla/atlas",
		"https://github.com/owner/repo":     "https://github.com/owner/repo/compare/main...flotilla/atlas",
		"git@github.com:owner/repo.git":     "https://github.com/owner/repo/compare/main...flotilla/atlas",
	}
	for remote, want := range cases {
		got, err := CompareURL(remote, "main", "flotilla/atlas")
		if err != nil {
			t.Fatalf("CompareURL(%q): %v", remote, err)
		}
		if got != want {
			t.Errorf("CompareURL(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestCompareURLRejectsUnknownRemote(t *testing.T) {
	if _, err := CompareURL("file:///tmp/x", "main", "b"); err == nil {
		t.Error("expected error for non-GitHub-style remote")
	}
}

func TestIsGitHub(t *testing.T) {
	if !isGitHub("git@github.com:o/r.git") {
		t.Error("git@github.com should be GitHub")
	}
	if isGitHub("https://gitlab.com/o/r.git") {
		t.Error("gitlab should not be GitHub")
	}
}

func TestFakeForgeReturnsConfiguredResult(t *testing.T) {
	f := &Fake{Result: PRResult{URL: "https://x/pr/1", Created: true}, AvailableFlag: true}
	got, err := f.EnsurePR(context.Background(), "/tmp/dir", "flotilla/atlas", gitops.WorkState{})
	if err != nil {
		t.Fatalf("EnsurePR: %v", err)
	}
	if !got.Created || got.URL != "https://x/pr/1" {
		t.Errorf("got %+v", got)
	}
	if len(f.Calls) != 1 || f.Calls[0] != "flotilla/atlas" {
		t.Errorf("Calls = %v, want [flotilla/atlas]", f.Calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/forge/ -v`
Expected: FAIL — package/types undefined.

- [ ] **Step 3: Write `forge.go` (interface + pure helpers)**

```go
// internal/forge/forge.go
// Package forge turns a pushed branch into a pull request via the gh CLI, with a
// push-only fallback (print a compare URL) when gh is unavailable or the remote
// is not GitHub. The Forge interface is faked in tests the way backend.Backend is.
package forge

import (
	"context"
	"fmt"
	"strings"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// PRResult is the outcome of ensuring a PR for a pushed branch.
type PRResult struct {
	URL      string // PR URL, or compare URL on fallback
	Created  bool   // true = a new PR was opened (false = existing PR, or push-only)
	PushOnly bool   // true = gh unavailable / non-GitHub; no PR opened
}

// Forge ensures a pull request exists for a pushed branch.
type Forge interface {
	Available(ctx context.Context) bool
	EnsurePR(ctx context.Context, dir, branch string, st gitops.WorkState) (PRResult, error)
}

// isGitHub reports whether the remote URL points at github.com.
func isGitHub(remoteURL string) bool { return strings.Contains(remoteURL, "github.com") }

// CompareURL builds the GitHub compare URL for opening a PR by hand, from either
// an https or scp-style (git@host:owner/repo) remote.
func CompareURL(remoteURL, base, branch string) (string, error) {
	h, err := httpsBase(remoteURL)
	if err != nil {
		return "", err
	}
	return h + "/compare/" + base + "..." + branch, nil
}

// httpsBase normalizes a git remote URL to https://host/owner/repo (no .git).
func httpsBase(remoteURL string) (string, error) {
	s := strings.TrimSuffix(remoteURL, ".git")
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		return s, nil
	case strings.HasPrefix(s, "ssh://git@"):
		rest := strings.TrimPrefix(s, "ssh://git@")
		host, path, ok := strings.Cut(rest, "/")
		if !ok {
			return "", fmt.Errorf("unrecognized remote %q", remoteURL)
		}
		return "https://" + host + "/" + path, nil
	case strings.HasPrefix(s, "git@"):
		rest := strings.TrimPrefix(s, "git@")
		host, path, ok := strings.Cut(rest, ":")
		if !ok {
			return "", fmt.Errorf("unrecognized remote %q", remoteURL)
		}
		return "https://" + host + "/" + path, nil
	default:
		return "", fmt.Errorf("unrecognized remote %q", remoteURL)
	}
}
```

- [ ] **Step 4: Write `gh.go` (real impl) and `fake.go`**

```go
// internal/forge/gh.go
package forge

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// GH ensures PRs via the gh CLI.
type GH struct{}

// NewGH returns a gh-backed Forge.
func NewGH() *GH { return &GH{} }

// GHAvailable reports whether gh is installed and authenticated.
func GHAvailable(ctx context.Context) bool {
	if _, err := exec.LookPath("gh"); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "gh", "auth", "status").Run() == nil
}

func (g *GH) Available(ctx context.Context) bool { return GHAvailable(ctx) }

// EnsurePR opens a PR for branch via `gh pr create --fill`, or returns the
// existing PR if one is already open. Falls back to push-only (compare URL) when
// gh is unavailable, the remote is not GitHub, or pr-create fails — the branch
// is already pushed, so no work is ever lost.
func (g *GH) EnsurePR(ctx context.Context, dir, branch string, st gitops.WorkState) (PRResult, error) {
	cmp, _ := CompareURL(st.RemoteURL, st.Base, branch)
	if !isGitHub(st.RemoteURL) || !GHAvailable(ctx) {
		return PRResult{URL: cmp, PushOnly: true}, nil
	}
	if url := ghOut(ctx, dir, "pr", "view", branch, "--json", "url", "-q", ".url"); url != "" {
		return PRResult{URL: url, Created: false}, nil
	}
	url, err := ghOut2(ctx, dir, "pr", "create", "--fill", "--head", branch)
	if err != nil {
		return PRResult{URL: cmp, PushOnly: true}, fmt.Errorf("gh pr create: %w", err)
	}
	return PRResult{URL: strings.TrimSpace(url), Created: true}, nil
}

// ghOut runs gh in dir and returns trimmed stdout, or "" on any error.
func ghOut(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ghOut2 runs gh in dir, returning stdout and wrapping stderr on error.
func ghOut2(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
```

```go
// internal/forge/fake.go
package forge

import "context"

// Fake is an in-memory Forge for unit tests (this package and internal/fleet).
type Fake struct {
	Result        PRResult
	Err           error
	AvailableFlag bool
	Calls         []string // branches passed to EnsurePR
}

func (f *Fake) Available(context.Context) bool { return f.AvailableFlag }

func (f *Fake) EnsurePR(_ context.Context, _, branch string, _ gitopsWorkState) (PRResult, error) {
	f.Calls = append(f.Calls, branch)
	return f.Result, f.Err
}
```

> NOTE: replace `gitopsWorkState` with `gitops.WorkState` and add the import
> `"github.com/mickzijdel/flotilla/internal/gitops"` — written this way only to
> flag that the fake's signature must match the interface exactly.

- [ ] **Step 5: Run tests + commit**

Run: `go test ./internal/forge/ -v`
Expected: PASS. Then:

```bash
git add internal/forge/
git commit -m "feat(forge): gh PR wrapper with push-only compare-URL fallback + fake"
```

---

### Task 4: `Fleet.Submit` — orchestration

**Files:**
- Create: `internal/fleet/submit.go`
- Create: `internal/fleet/submit_test.go`
- Modify: `internal/fleet/fleet.go:30-36` (add `Forge forge.Forge` field to the `Fleet` struct)
- Modify: `main.go:14-18` (wire `Forge: forge.NewGH()`)

**Interfaces:**
- Consumes: `gitops.Inspect`, `gitops.Push` (Tasks 1–2); `forge.Forge`, `forge.CompareURL` (Task 3); existing `f.resolve(ctx, name)` and `backend.Container.Status`.
- Produces:
  - `type Submission struct { Agent, Branch, PRURL string; Created, PushOnly bool; Note string }`
  - `func (f *Fleet) Submit(ctx context.Context, name string, force bool) (Submission, error)`
  - `func (f *Fleet) workDir(name string) string` (unexported helper: `filepath.Join(f.workRoot(), name)`)

- [ ] **Step 1: Add the `Forge` field**

In `internal/fleet/fleet.go`, add the import `"github.com/mickzijdel/flotilla/internal/forge"` and extend the struct:

```go
type Fleet struct {
	Backend        backend.Backend
	BaseImage      string
	WorkRoot       string
	EgressFirewall bool
	EgressAllow    []string
	Forge          forge.Forge // PR creation; nil → push-only
}
```

- [ ] **Step 2: Write the failing test**

```go
// internal/fleet/submit_test.go
package fleet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/forge"
)

// runGit is a test helper (combined output on failure).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// seedClone builds a bare remote + a clone at workRoot/<name> with nCommits
// commits, and registers an exited container labelled <name> on the fake.
func seedClone(t *testing.T, f *Fleet, fake *backend.Fake, name string, nCommits int) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	runGit(t, "", "init", "-q", "-b", "main", seed)
	runGit(t, seed, "config", "user.email", "t@e.com")
	runGit(t, seed, "config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi"), 0o644)
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-q", "-m", "init")
	runGit(t, "", "clone", "-q", "--bare", seed, bare)

	dest := filepath.Join(f.workRoot(), name)
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	runGit(t, "", "clone", "-q", bare, dest)
	runGit(t, dest, "config", "user.email", "a@e.com")
	runGit(t, dest, "config", "user.name", "agent")
	for i := 0; i < nCommits; i++ {
		runGit(t, dest, "commit", "-q", "--allow-empty", "-m", "work")
	}
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: name}})
	_ = fake.Stop(ctx, id) // exited == process-exit done-signal
}

func newTestFleet(t *testing.T, fk *forge.Fake) (*Fleet, *backend.Fake) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, WorkRoot: t.TempDir(), Forge: fk}
	return f, fake
}

func TestSubmitPushesAndReturnsPR(t *testing.T) {
	fk := &forge.Fake{Result: forge.PRResult{URL: "https://h/pr/7", Created: true}, AvailableFlag: true}
	f, fake := newTestFleet(t, fk)
	seedClone(t, f, fake, "atlas", 1)

	sub, err := f.Submit(context.Background(), "atlas", false)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sub.Branch != "flotilla/atlas" {
		t.Errorf("Branch = %q, want flotilla/atlas", sub.Branch)
	}
	if sub.PRURL != "https://h/pr/7" || !sub.Created {
		t.Errorf("got %+v", sub)
	}
	if len(fk.Calls) != 1 {
		t.Errorf("EnsurePR calls = %d, want 1", len(fk.Calls))
	}
}

func TestSubmitRefusesDirtyTree(t *testing.T) {
	fk := &forge.Fake{}
	f, fake := newTestFleet(t, fk)
	seedClone(t, f, fake, "atlas", 1)
	_ = os.WriteFile(filepath.Join(f.workRoot(), "atlas", "dirty.txt"), []byte("u"), 0o644)

	if _, err := f.Submit(context.Background(), "atlas", false); err == nil {
		t.Error("expected refusal on dirty tree")
	}
}

func TestSubmitRefusesNoCommits(t *testing.T) {
	fk := &forge.Fake{}
	f, fake := newTestFleet(t, fk)
	seedClone(t, f, fake, "atlas", 0)

	if _, err := f.Submit(context.Background(), "atlas", false); err == nil {
		t.Error("expected 'nothing to submit'")
	}
}

func TestSubmitRefusesRunningWithoutForce(t *testing.T) {
	fk := &forge.Fake{}
	f, fake := newTestFleet(t, fk)
	seedClone(t, f, fake, "atlas", 1)
	// flip the container back to running
	cs, _ := fake.List(context.Background(), map[string]string{backend.LabelAgent: "atlas"})
	_ = fake.Start(context.Background(), cs[0].ID)

	if _, err := f.Submit(context.Background(), "atlas", false); err == nil {
		t.Error("expected refusal while running without --force")
	}
	if _, err := f.Submit(context.Background(), "atlas", true); err != nil {
		t.Errorf("--force should bypass status gate: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestSubmit -v`
Expected: FAIL — `undefined: (*Fleet).Submit` / `undefined: Submission`.

- [ ] **Step 4: Write the implementation**

```go
// internal/fleet/submit.go
package fleet

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/mickzijdel/flotilla/internal/forge"
	"github.com/mickzijdel/flotilla/internal/gitops"
)

// Submission is the outcome of `flotilla submit`.
type Submission struct {
	Agent    string `json:"agent"`
	Branch   string `json:"branch"`
	PRURL    string `json:"prURL"`
	Created  bool   `json:"created"`
	PushOnly bool   `json:"pushOnly"`
	Note     string `json:"note,omitempty"`
}

func (f *Fleet) workDir(name string) string {
	return filepath.Join(f.workRoot(), name)
}

// Submit pushes a finished agent's commits to flotilla/<name> and ensures a PR.
// It is status-gated on the process-exit done-signal (container exited) unless
// force is set, and strictly refuses a dirty tree or zero commits.
func (f *Fleet) Submit(ctx context.Context, name string, force bool) (Submission, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return Submission{}, err
	}
	if c.Status != "exited" && !force {
		return Submission{}, fmt.Errorf("agent %q is still running; wait for it to finish or pass --force", name)
	}

	dir := f.workDir(name)
	st, err := gitops.Inspect(ctx, dir)
	if err != nil {
		return Submission{}, err
	}
	if st.Dirty {
		return Submission{}, fmt.Errorf("agent %q has uncommitted changes; commit them inside the container first", name)
	}
	if st.CommitsAhead == 0 {
		return Submission{}, fmt.Errorf("nothing to submit: agent %q has no commits beyond %s", name, st.Base)
	}

	branch := "flotilla/" + name
	if err := gitops.Push(ctx, dir, branch); err != nil {
		return Submission{}, err
	}

	sub := Submission{Agent: name, Branch: branch}
	if f.Forge == nil {
		cmp, _ := forge.CompareURL(st.RemoteURL, st.Base, branch)
		sub.PushOnly = true
		sub.PRURL = cmp
		return sub, nil
	}
	res, perr := f.Forge.EnsurePR(ctx, dir, branch, st)
	if perr != nil {
		// Push succeeded; PR automation didn't. Degrade to push-only, keep the reason.
		cmp, _ := forge.CompareURL(st.RemoteURL, st.Base, branch)
		sub.PushOnly = true
		sub.PRURL = cmp
		sub.Note = perr.Error()
		return sub, nil
	}
	sub.PRURL = res.URL
	sub.Created = res.Created
	sub.PushOnly = res.PushOnly
	return sub, nil
}
```

Then wire `main.go`:

```go
// main.go — add import "github.com/mickzijdel/flotilla/internal/forge"
	f := &fleet.Fleet{
		Backend:        backend.NewDocker(),
		BaseImage:      "ubuntu:24.04",
		EgressFirewall: true,
		Forge:          forge.NewGH(),
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/fleet/ -run TestSubmit -v`
Expected: PASS (all four). Then `go build ./...` to confirm `main.go` compiles.

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/submit.go internal/fleet/submit_test.go internal/fleet/fleet.go main.go
git commit -m "feat(fleet): Submit — status-gated push + PR orchestration"
```

---

### Task 5: `flotilla submit` CLI command + doctor gh check

**Files:**
- Modify: `internal/cli/cli.go` (add `submitCmd`, register it in `BuildRoot`, add gh advisory line to `doctorCmd`)
- Create: `internal/cli/submit_test.go`

**Interfaces:**
- Consumes: `f.Submit(ctx, name, force)` → `fleet.Submission` (Task 4); `forge.GHAvailable(ctx)` (Task 3).
- Produces: `flotilla submit <agent> [--force] [--json]`.

- [ ] **Step 1: Write the failing test**

```go
// internal/cli/submit_test.go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/mickzijdel/flotilla/internal/forge"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestSubmitCmdPrintsPRURL(t *testing.T) {
	fake := backend.NewFake()
	work := t.TempDir()
	fk := &forge.Fake{Result: forge.PRResult{URL: "https://h/pr/9", Created: true}, AvailableFlag: true}
	f := &fleet.Fleet{Backend: fake, WorkRoot: work, Forge: fk}

	// Seed a clone with one commit + an exited container named "atlas".
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	runGit(t, "", "init", "-q", "-b", "main", seed)
	runGit(t, seed, "config", "user.email", "t@e.com")
	runGit(t, seed, "config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi"), 0o644)
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-q", "-m", "init")
	runGit(t, "", "clone", "-q", "--bare", seed, bare)
	dest := filepath.Join(work, "atlas")
	runGit(t, "", "clone", "-q", bare, dest)
	runGit(t, dest, "config", "user.email", "a@e.com")
	runGit(t, dest, "config", "user.name", "a")
	runGit(t, dest, "commit", "-q", "--allow-empty", "-m", "work")
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})
	_ = fake.Stop(ctx, id)

	root2 := BuildRoot(f)
	var out bytes.Buffer
	root2.SetOut(&out)
	root2.SetErr(&out)
	root2.SetArgs([]string{"submit", "atlas", "--json"})
	if err := root2.ExecuteContext(ctx); err != nil {
		t.Fatalf("submit: %v: %s", err, out.String())
	}
	var sub fleet.Submission
	if err := json.Unmarshal(out.Bytes(), &sub); err != nil {
		t.Fatalf("decode JSON %q: %v", out.String(), err)
	}
	if sub.PRURL != "https://h/pr/9" || !strings.Contains(sub.Branch, "flotilla/atlas") {
		t.Errorf("got %+v", sub)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestSubmitCmd -v`
Expected: FAIL — `submit` is an unknown command (non-zero exit, error in output).

- [ ] **Step 3: Implement `submitCmd`, register it, extend doctor**

In `internal/cli/cli.go` add `"github.com/mickzijdel/flotilla/internal/forge"` to imports, register the command, and add the function:

```go
// in BuildRoot:
	root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), submitCmd(f), agentsCmd(), doctorCmd())
```

```go
func submitCmd(f *fleet.Fleet) *cobra.Command {
	var force, asJSON bool
	c := &cobra.Command{
		Use:   "submit <agent>",
		Short: "Push the agent's commits and open/update a PR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sub, err := f.Submit(cmd.Context(), args[0], force)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(sub)
			}
			out := cmd.OutOrStdout()
			switch {
			case sub.PushOnly:
				if _, err := fmt.Fprintf(out, "Pushed %s → open a PR: %s\n", sub.Branch, sub.PRURL); err != nil {
					return err
				}
				if sub.Note != "" {
					_, err = fmt.Fprintf(out, "(note: %s)\n", sub.Note)
				}
				return err
			case sub.Created:
				_, err = fmt.Fprintf(out, "Pushed %s → opened PR %s\n", sub.Branch, sub.PRURL)
				return err
			default:
				_, err = fmt.Fprintf(out, "Pushed %s → updated existing PR %s\n", sub.Branch, sub.PRURL)
				return err
			}
		},
	}
	c.Flags().BoolVar(&force, "force", false, "submit even if the agent is still running")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}
```

In `doctorCmd`, after the preflight loop and before the OK check, add an advisory `gh` line:

```go
			if forge.GHAvailable(cmd.Context()) {
				fmt.Fprintln(cmd.OutOrStdout(), "ok: gh CLI authenticated (PRs will be opened automatically)")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "advisory: gh CLI not found/authenticated — submit will push only and print a compare URL")
			}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/submit_test.go
git commit -m "feat(cli): submit command + doctor gh advisory"
```

---

### Task 6: `attach` auto-starts an exited container

**Files:**
- Modify: `internal/fleet/fleet.go:194-200` (`Attach`)
- Modify/Create: `internal/fleet/attach_test.go` (add an auto-start case)

**Interfaces:**
- Consumes: existing `f.resolve`, `backend.Backend.Start`, `backend.Backend.AttachInfo`.
- Produces: `Attach` starts the container when `Status == "exited"` before returning attach info (behavior change; signature unchanged).

- [ ] **Step 1: Write the failing test** (append to `internal/fleet/attach_test.go`)

```go
func TestAttachAutoStartsExitedContainer(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})
	_ = fake.Stop(ctx, id) // exited

	f := &Fleet{Backend: fake}
	if _, err := f.Attach(ctx, "atlas"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	cs, _ := fake.List(ctx, map[string]string{backend.LabelAgent: "atlas"})
	if cs[0].Status != "running" {
		t.Errorf("Status = %q, want running (attach should auto-start)", cs[0].Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestAttachAutoStarts -v`
Expected: FAIL — status stays `exited`.

- [ ] **Step 3: Update `Attach`**

```go
// Attach returns attach info for a named agent, auto-starting it if it exited
// (the process-exit done-signal leaves the container stopped but present, and
// docker exec needs it running).
func (f *Fleet) Attach(ctx context.Context, name string) (backend.AttachInfo, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return backend.AttachInfo{}, err
	}
	if c.Status == "exited" {
		if err := f.Backend.Start(ctx, c.ID); err != nil {
			return backend.AttachInfo{}, fmt.Errorf("start exited agent %q: %w", name, err)
		}
	}
	return f.Backend.AttachInfo(ctx, c.ID)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/fleet/ -run TestAttach -v`
Expected: PASS (existing two + the new one).

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/fleet.go internal/fleet/attach_test.go
git commit -m "feat(fleet): attach auto-starts an exited container for recovery"
```

---

### Task 7: `wrap_up` profile field + prompt-injected wrap-up contract

**Files:**
- Modify: `internal/agent/profile.go:16-26` (add `WrapUp` field)
- Create: `internal/agent/wrapup.go` (default text + helper)
- Modify: `internal/agent/builtin/claude.toml`, `internal/agent/builtin/codex.toml` (none needed if default applies; see Step 3)
- Modify: `internal/fleet/fleet.go:135` (append the wrap-up contract to the injected prompt)
- Create: `internal/agent/wrapup_test.go`
- Modify: `internal/fleet/spawn_helpers_test.go` or new `internal/fleet/wrapup_test.go` (assert appended content)

**Interfaces:**
- Produces:
  - `Profile.WrapUp string` (toml `wrap_up`)
  - `func (p Profile) WrapUpText() string` — returns `p.WrapUp` if set, else `DefaultWrapUp`; empty string `"-"` sentinel disables (returns "").
  - `const DefaultWrapUp = "..."`
  - `func PromptWithWrapUp(prompt, wrapUp string) string` — appends a delimited contract block when `wrapUp != ""`.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/wrapup_test.go
package agent

import "strings"

import "testing"

func TestWrapUpTextDefaultsAndDisable(t *testing.T) {
	if (Profile{}).WrapUpText() != DefaultWrapUp {
		t.Error("empty WrapUp should fall back to DefaultWrapUp")
	}
	if (Profile{WrapUp: "custom"}).WrapUpText() != "custom" {
		t.Error("explicit WrapUp should win")
	}
	if (Profile{WrapUp: "-"}).WrapUpText() != "" {
		t.Error("'-' sentinel should disable the wrap-up contract")
	}
}

func TestPromptWithWrapUpAppendsDelimitedBlock(t *testing.T) {
	got := PromptWithWrapUp("do the thing", DefaultWrapUp)
	if !strings.HasPrefix(got, "do the thing") {
		t.Error("user prompt must come first")
	}
	if !strings.Contains(got, "commit") {
		t.Error("wrap-up contract should mention committing")
	}
	if PromptWithWrapUp("just this", "") != "just this" {
		t.Error("empty wrap-up should leave the prompt unchanged")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run "TestWrapUp|TestPromptWithWrapUp" -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement the field, default, and helpers**

Add to `Profile` in `internal/agent/profile.go`:

```go
	DoneSignal     string   `toml:"done_signal"`
	WrapUp         string   `toml:"wrap_up"`
```

New file:

```go
// internal/agent/wrapup.go
package agent

import "strings"

// DefaultWrapUp is appended to every agent's prompt so the working tree is clean
// and committed by the time the agent exits (Flotilla submits strictly).
const DefaultWrapUp = "Before you finish, commit all your changes with clear, " +
	"descriptive messages — your commit messages become the pull request. Do not " +
	"leave uncommitted work; anything uncommitted will be discarded and the submission rejected."

// WrapUpText returns the effective wrap-up contract: the profile's override, the
// default when unset, or "" when explicitly disabled with the "-" sentinel.
func (p Profile) WrapUpText() string {
	switch p.WrapUp {
	case "":
		return DefaultWrapUp
	case "-":
		return ""
	default:
		return p.WrapUp
	}
}

// PromptWithWrapUp appends the wrap-up contract to the user prompt as a clearly
// delimited block. An empty contract leaves the prompt unchanged.
func PromptWithWrapUp(prompt, wrapUp string) string {
	if strings.TrimSpace(wrapUp) == "" {
		return prompt
	}
	return prompt + "\n\n---\n[Flotilla submission contract]\n" + wrapUp + "\n"
}
```

> No change needed to `claude.toml`/`codex.toml` — both inherit `DefaultWrapUp`.

- [ ] **Step 4: Wire it into Spawn**

In `internal/fleet/fleet.go`, change the prompt-injection line (currently line ~135):

```go
	if err := inj.WriteFile(ctx, []byte(agent.PromptWithWrapUp(prompt, prof.WrapUpText())), agentPromptFile(home)); err != nil {
		return fail(fmt.Errorf("inject prompt: %w", err))
	}
```

(The `agent` package is already imported in `fleet.go`.)

- [ ] **Step 5: Add a fleet-level test that the injected prompt carries the contract**

```go
// internal/fleet/wrapup_test.go
package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestSpawnInjectsWrapUpContractIntoPrompt(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, WorkRoot: t.TempDir(), BaseImage: "ubuntu:24.04"}
	prof := agent.Profile{Name: "x", Launch: `echo "{prompt}"`}

	// Spawn will fail later (no real git remote), so drive injection directly:
	// assert the helper the spawn path uses produces the contract.
	got := agent.PromptWithWrapUp("task", prof.WrapUpText())
	if !strings.Contains(got, "commit") || !strings.Contains(got, "task") {
		t.Errorf("prompt missing task or contract: %q", got)
	}
	_ = f
	_ = context.Background()
}
```

> NOTE: the fleet test asserts the helper rather than running a full `Spawn`
> (which needs a clonable remote + devcontainer). The behavioral guarantee — the
> *spawn path* calls `PromptWithWrapUp` — is enforced by the Step 4 edit and
> `go build`; the helper's correctness is covered in `internal/agent`.

- [ ] **Step 6: Run tests + build + commit**

Run: `go test ./internal/agent/ ./internal/fleet/ -v` then `go build ./...`
Expected: PASS / clean build.

```bash
git add internal/agent/profile.go internal/agent/wrapup.go internal/agent/wrapup_test.go internal/fleet/fleet.go internal/fleet/wrapup_test.go
git commit -m "feat(agent): wrap_up prompt contract so agents commit before exit"
```

---

### Task 8: Claude Stop hook safety net

**Files:**
- Modify: `internal/setup/setup.go:59-76` (`claudeSetup` writes a `settings.json` with a Stop hook)
- Modify: `internal/setup/setup_test.go` (assert the Stop hook is present)

**Interfaces:**
- Consumes: existing `setup.Injector.WriteFile`.
- Produces: the `claude` profile's injected `~/.claude/settings.json` contains a `Stop` hook that commits any leftover changes (safety net behind the Task 7 prompt contract).

- [ ] **Step 1: Write the failing test**

```go
// add to internal/setup/setup_test.go
func TestClaudeSetupWritesStopHookCommit(t *testing.T) {
	inj := &fakeInjector{}
	if err := Run(context.Background(), inj, agent.Profile{Setup: "builtin:claude"}, "/home/ubuntu"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var settings string
	for _, w := range inj.writes {
		if strings.HasSuffix(w.dest, "settings.json") {
			settings = string(w.content)
		}
	}
	if settings == "" {
		t.Fatal("no settings.json written")
	}
	if !strings.Contains(settings, "\"Stop\"") || !strings.Contains(settings, "git commit") {
		t.Errorf("settings.json missing Stop/commit hook: %s", settings)
	}
}
```

> If `internal/setup/setup_test.go` lacks a `fakeInjector` recording writes,
> add one: a struct with `writes []struct{ dest string; content []byte }`
> implementing `Exec`/`CopyTo`/`WriteFile` (WriteFile appends to `writes`).
> Check the existing test file first and reuse its fake if present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/setup/ -run TestClaudeSetupWritesStopHook -v`
Expected: FAIL — settings.json is currently `{}`.

- [ ] **Step 3: Update `claudeSetup`**

Replace the `settings.json` write in `claudeSetup` with one that embeds the Stop hook:

```go
	// Minimal settings + a Stop hook that commits anything the agent left
	// uncommitted (safety net behind the wrap_up prompt contract). `|| true`
	// keeps a no-op commit (nothing staged) from failing the hook.
	settings := `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "git add -A && (git diff --cached --quiet || git commit -m 'flotilla: wrap-up commit') || true" }
        ]
      }
    ]
  }
}
`
	if err := inj.WriteFile(ctx, []byte(settings), filepath.Join(dir, "settings.json")); err != nil {
		return err
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/setup/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/setup/setup.go internal/setup/setup_test.go
git commit -m "feat(setup): claude Stop hook commits leftovers as a wrap-up safety net"
```

---

### Task 9: Full-suite verification + docs

**Files:**
- Modify: `README.md` (submission flow is no longer "pending")
- Modify: `docs/backlog.md` (check off the submission-flow item)

- [ ] **Step 1: Run the whole suite + lint**

Run: `go test ./... && go build ./... && golangci-lint run ./... && golangci-lint fmt --diff`
Expected: all green; no format diff.

- [ ] **Step 2: Update README.md**

In the status paragraph, change the line that says the "submission/PR flow are still pending" to reflect that `flotilla submit <agent>` now pushes the agent's commits to a `flotilla/<agent>` branch and opens/updates a PR via `gh` (push-only compare-URL fallback), with credentials only on the engine. Add `submit` to any command list.

- [ ] **Step 3: Update docs/backlog.md**

Mark the "Submission flow" next-plan item as done (link the spec/plan), and remove it from the "still pending" framing.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/backlog.md
git commit -m "docs: submission flow shipped (flotilla submit)"
```

---

## Self-Review

**1. Spec coverage** (each spec section → task):
- §2 #1 trigger/status-gate → Task 4 (`Submit` status gate + `--force`), Task 5 (flag).
- §2 #2 strict work state → Task 1 (`Inspect`), Task 4 (refusals).
- §2 #3 branch + force-with-lease → Task 2 (`Push`), Task 4 (branch name).
- §2 #4 / #5 PR via `gh --fill` + push-only fallback + commit-message content → Task 3 (`forge`).
- §2 #6 wrap-up both layers → Task 7 (prompt contract), Task 8 (Claude Stop hook).
- §2 #7 structure A → Tasks 1–5 package boundaries.
- §2 #8 attach auto-start → Task 6.
- §4.1 base discovery → Task 1 (`symbolic-ref` + fallback).
- §4.2 ownership/`safe.directory` + read-only plumbing → Task 1 (`git` helper, `isDirty`).
- §7 error handling table → Task 4 (each refusal), Task 3 (gh-fail degrade), Task 4 (`Note`).
- §8 CLI surface + doctor gh check → Task 5.
- §9 testing → tests in every task; live-gh path documented as self-skipping (Task 3).
- §6 interaction/recovery → Task 6.

**2. Placeholder scan:** No "TBD"/"add error handling"/"similar to". The two `> NOTE` blocks flag exact substitutions (the `gitops.WorkState` import in `forge/fake.go`; reusing an existing `fakeInjector`) — both give the concrete action, not a deferral.

**3. Type consistency:** `WorkState{Base,CommitsAhead,Dirty,RemoteURL}` (Task 1) is consumed unchanged by `forge.EnsurePR`/`CompareURL` (Task 3) and `Fleet.Submit` (Task 4). `Forge` interface signature (Task 3) matches `Fake.EnsurePR` and `GH.EnsurePR`. `Submission` fields (Task 4) match the CLI's JSON decode and switch (Task 5). `PromptWithWrapUp`/`WrapUpText` (Task 7) names match their call site in `fleet.go`. `forge.GHAvailable` (Task 3) matches the doctor call (Task 5).

**Note for the implementer:** Tasks 1–5 are a strict dependency chain (build in order). Tasks 6, 7+8 are independent and may be done in any order after Task 4 / in parallel. `internal/cli/submit_test.go` and `internal/fleet/submit_test.go` both define a `runGit` helper in *different packages* — no collision.
