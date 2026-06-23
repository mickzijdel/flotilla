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
