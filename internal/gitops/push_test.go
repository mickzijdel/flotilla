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
