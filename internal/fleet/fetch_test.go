package fleet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/forge"
)

// gitOut runs git in dir and returns trimmed stdout (test helper).
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// TestFetchRefreshesAgentClone runs the real Fleet.Fetch against a real clone an
// agent container owns (modelled with the fake backend) and asserts origin/main
// advances after the upstream moves — without touching the local branch.
func TestFetchRefreshesAgentClone(t *testing.T) {
	f, fake := newTestFleet(t, &forge.Fake{})
	seedClone(t, f, fake, "otter", 1) // clone at workDir("otter"), origin=bare on main

	dest := f.workDir("otter")
	bare := gitOut(t, dest, "remote", "get-url", "origin")
	originBefore := gitOut(t, dest, "rev-parse", "refs/remotes/origin/main")

	// Advance the remote via a second clone of the same bare.
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "-q", bare, other)
	runGit(t, other, "config", "user.email", "o@e.com")
	runGit(t, other, "config", "user.name", "o")
	if err := os.WriteFile(filepath.Join(other, "up.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-q", "-m", "upstream")
	runGit(t, other, "push", "-q", "origin", "main")

	localBefore := gitOut(t, dest, "rev-parse", "HEAD")
	if err := f.Fetch(context.Background(), "otter"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got := gitOut(t, dest, "rev-parse", "refs/remotes/origin/main"); got == originBefore {
		t.Fatalf("origin/main did not advance after Fetch (still %s)", originBefore)
	}
	if got := gitOut(t, dest, "rev-parse", "HEAD"); got != localBefore {
		t.Fatalf("Fetch moved the local branch HEAD: %s != %s", got, localBefore)
	}
}

func TestFetchUnknownAgent(t *testing.T) {
	f, _ := newTestFleet(t, &forge.Fake{})
	err := f.Fetch(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), `no agent named "ghost"`) {
		t.Fatalf("want no-agent error, got %v", err)
	}
}

func TestFetchMissingClone(t *testing.T) {
	f, fake := newTestFleet(t, &forge.Fake{})
	// Agent container exists, but no clone was seeded under WorkRoot.
	_, _ = fake.Create(context.Background(), backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "otter"}})
	err := f.Fetch(context.Background(), "otter")
	if err == nil || !strings.Contains(err.Error(), "no workspace clone for agent") {
		t.Fatalf("want missing-clone error, got %v", err)
	}
}
