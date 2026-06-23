package fleet

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// dockerAvailable reports whether a working Docker daemon is reachable.
func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command("docker", args...)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// TestFetchVisibleInContainerViaBindMount is the Docker-specific claim of the
// on-demand-fetch slice: an engine-side `gitops.Fetch` (the primitive both the
// `flotilla fetch` CLI and the daemon handler converge on) updates origin's
// remote-tracking ref in the engine-side clone, and because that clone is
// bind-mounted into the container, the new ref is live inside the container
// instantly — no restart, no copy. The container resolves the ref with its own
// git (origin/<base> via rev-parse, which reads packed and loose refs).
// Self-skips without Docker.
func TestFetchVisibleInContainerViaBindMount(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available; skipping integration test")
	}
	ctx := context.Background()

	// Engine-side bare remote + clone (the clone is what gets bind-mounted).
	bare := bareRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", "-q", bare, clone)
	baseBranch := gitOut(t, clone, "rev-parse", "--abbrev-ref", "HEAD") // bareRepo's default branch

	// Run a throwaway git-capable container with the clone bind-mounted at /work.
	cid := dockerRun(t, "run", "-d", "--rm", "--entrypoint", "sh",
		"-v", clone+":/work", "alpine/git", "-c", "sleep 120")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", cid).Run() })

	// inContainerOriginRef resolves origin/<base> using the container's own git.
	// safe.directory placates Git's dubious-ownership guard for the host-owned .git.
	inContainerOriginRef := func() string {
		return dockerRun(t, "exec", cid, "git", "-C", "/work",
			"-c", "safe.directory=/work", "rev-parse", "origin/"+baseBranch)
	}
	before := inContainerOriginRef()

	// Advance the remote engine-side via a second clone.
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "-q", bare, other)
	runGit(t, other, "config", "user.email", "o@e.com")
	runGit(t, other, "config", "user.name", "o")
	if err := os.WriteFile(filepath.Join(other, "up.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-q", "-m", "upstream")
	runGit(t, other, "push", "-q", "origin", baseBranch)
	newSHA := gitOut(t, other, "rev-parse", "HEAD")

	// The engine fetches into the bind-mounted clone (the shared primitive).
	if err := gitops.Fetch(ctx, clone); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// The advanced origin ref is now visible inside the container.
	after := inContainerOriginRef()
	if after == before {
		t.Fatalf("origin/%s did not change inside the container (still %s)", baseBranch, before)
	}
	if after != newSHA {
		t.Fatalf("in-container origin/%s = %q, want pushed SHA %q", baseBranch, after, newSHA)
	}
}
