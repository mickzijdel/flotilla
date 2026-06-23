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
