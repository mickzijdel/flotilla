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
	// A local branch must also exist so PR tooling (gh pr create --fill) can
	// resolve the head ref by name — pushing HEAD detached-style would not.
	if _, err := git(context.Background(), dest, "rev-parse", "--verify", "refs/heads/flotilla/atlas"); err != nil {
		t.Errorf("expected local branch refs/heads/flotilla/atlas to exist: %v", err)
	}
}

func TestPushUpdatesLocalBranchOnResubmit(t *testing.T) {
	dest := cloneWithCommits(t, 1)
	ctx := context.Background()
	if err := Push(ctx, dest, "flotilla/atlas"); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	mustRun(t, dest, "git", "commit", "-q", "--allow-empty", "-m", "more")
	if err := Push(ctx, dest, "flotilla/atlas"); err != nil {
		t.Fatalf("second Push: %v", err)
	}
	// The local branch must point at the new HEAD after a re-submit.
	head, err := git(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	br, err := git(ctx, dest, "rev-parse", "refs/heads/flotilla/atlas")
	if err != nil {
		t.Fatalf("rev-parse branch: %v", err)
	}
	if head != br {
		t.Errorf("local branch = %s, want HEAD %s", br, head)
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
