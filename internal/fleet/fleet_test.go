package fleet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

func bareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	bare := filepath.Join(dir, "remote.git")
	run := func(d, n string, a ...string) {
		c := exec.Command(n, a...)
		c.Dir = d
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", n, a, err, out)
		}
	}
	run("", "git", "init", "-q", work)
	run(work, "git", "config", "user.email", "t@e.com")
	run(work, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644)
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-q", "-m", "init")
	run("", "git", "clone", "-q", "--bare", work, bare)
	return bare
}

func TestSpawnClonesAndCreatesContainer(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	a, err := f.Spawn(context.Background(), bareRepo(t), prof, "do the thing")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if a.Name == "" || a.ID == "" {
		t.Fatalf("Spawn returned empty agent: %+v", a)
	}
	// The clone must exist on disk.
	if _, err := os.Stat(filepath.Join(f.WorkRoot, a.Name, "f.txt")); err != nil {
		t.Errorf("expected cloned file for agent: %v", err)
	}
	// The container must be labeled and running.
	got, _ := fake.List(context.Background(), map[string]string{backend.LabelAgent: a.Name})
	if len(got) != 1 {
		t.Fatalf("List = %+v, want 1", got)
	}
	if got[0].Status != "running" {
		t.Errorf("status = %q, want running", got[0].Status)
	}
}
