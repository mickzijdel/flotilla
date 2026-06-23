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
