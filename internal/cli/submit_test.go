// internal/cli/submit_test.go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	var stdout, stderr bytes.Buffer
	root2.SetOut(&stdout)
	root2.SetErr(&stderr)
	root2.SetArgs([]string{"submit", "atlas", "--json"})
	if err := root2.ExecuteContext(ctx); err != nil {
		t.Fatalf("submit: %v: %s", err, stderr.String())
	}
	var sub fleet.Submission
	if err := json.Unmarshal(stdout.Bytes(), &sub); err != nil {
		t.Fatalf("decode JSON %q: %v", stdout.String(), err)
	}
	if sub.PRURL != "https://h/pr/9" || !strings.Contains(sub.Branch, "flotilla/atlas") {
		t.Errorf("got %+v", sub)
	}
}

// seedClone creates a bare remote + cloned working dir under root named agentName,
// registers an exited container in fake, and returns the working dir path.
func seedClone(t *testing.T, root, work string, agentName string, fake *backend.Fake) string {
	t.Helper()
	seed := filepath.Join(root, agentName+"-seed")
	bare := filepath.Join(root, agentName+"-remote.git")
	runGit(t, "", "init", "-q", "-b", "main", seed)
	runGit(t, seed, "config", "user.email", "t@e.com")
	runGit(t, seed, "config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi"), 0o644)
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-q", "-m", "init")
	runGit(t, "", "clone", "-q", "--bare", seed, bare)
	dest := filepath.Join(work, agentName)
	runGit(t, "", "clone", "-q", bare, dest)
	runGit(t, dest, "config", "user.email", "a@e.com")
	runGit(t, dest, "config", "user.name", "a")
	runGit(t, dest, "commit", "-q", "--allow-empty", "-m", "work")
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: agentName}})
	_ = fake.Stop(ctx, id)
	return dest
}

func TestSubmitCmdHumanOutput(t *testing.T) {
	cases := []struct {
		name      string
		agent     string
		fk        *forge.Fake
		wantOut   []string // substrings expected in stdout
		wantNoOut []string // substrings NOT expected in stdout
	}{
		{
			name:    "created PR",
			agent:   "beta",
			fk:      &forge.Fake{AvailableFlag: true, Result: forge.PRResult{URL: "https://h/pr/1", Created: true}},
			wantOut: []string{"opened PR https://h/pr/1"},
		},
		{
			name:    "updated existing PR",
			agent:   "gamma",
			fk:      &forge.Fake{AvailableFlag: true, Result: forge.PRResult{URL: "https://h/pr/2", Created: false}},
			wantOut: []string{"updated existing PR https://h/pr/2"},
		},
		{
			name:    "push-only (gh unavailable)",
			agent:   "delta",
			fk:      &forge.Fake{AvailableFlag: false, Result: forge.PRResult{URL: "https://h/compare/x", PushOnly: true}},
			wantOut: []string{"open a PR: https://h/compare/x"},
		},
		{
			name:  "push-only with note (gh create failed)",
			agent: "epsilon",
			fk:    &forge.Fake{AvailableFlag: true, Err: errors.New("gh pr create: boom")},
			// The test seed uses a local remote, so the degrade path's compare URL is
			// empty; the note line carrying the gh failure is the point of this case.
			wantOut: []string{"open a pull request on your host", "(note:", "boom"},
		},
		{
			name:      "push-only, no compare URL (non-GitHub remote)",
			agent:     "zeta",
			fk:        &forge.Fake{AvailableFlag: false, Result: forge.PRResult{URL: "", PushOnly: true}},
			wantOut:   []string{"open a pull request on your host"},
			wantNoOut: []string{"open a PR:"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := backend.NewFake()
			work := t.TempDir()
			root := t.TempDir()
			seedClone(t, root, work, tc.agent, fake)

			f := &fleet.Fleet{Backend: fake, WorkRoot: work, Forge: tc.fk}
			root2 := BuildRoot(f)
			var stdout, stderr bytes.Buffer
			root2.SetOut(&stdout)
			root2.SetErr(&stderr)
			root2.SetArgs([]string{"submit", tc.agent})
			if err := root2.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("submit: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
			got := stdout.String()
			for _, want := range tc.wantOut {
				if !strings.Contains(got, want) {
					t.Errorf("output %q does not contain %q", got, want)
				}
			}
			for _, noWant := range tc.wantNoOut {
				if strings.Contains(got, noWant) {
					t.Errorf("output %q should NOT contain %q", got, noWant)
				}
			}
		})
	}
}
