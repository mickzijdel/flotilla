package gitops

import (
	"context"
	"os"
	"os/exec"
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

// revParse returns the resolved object id for ref in dir (captured stdout, unlike
// mustRun which discards it).
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return string(out)
}
