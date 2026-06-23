package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeBareRepo creates a local bare repo on branch main with one commit and
// returns its path. The fixed branch keeps base-branch assertions deterministic
// regardless of the host's git init.defaultBranch.
func makeBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	bare := filepath.Join(dir, "remote.git")
	mustRun(t, "", "git", "init", "-q", "-b", "main", work)
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
