package fleet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-q", "-m", "init")
	run("", "git", "clone", "-q", "--bare", work, bare)
	return bare
}

// failUpBackend wraps a Fake but always errors from Up, to exercise Spawn's
// clone-cleanup-on-failure path.
type failUpBackend struct{ *backend.Fake }

func (failUpBackend) Up(context.Context, backend.UpOpts) (backend.UpResult, error) {
	return backend.UpResult{}, errors.New("boom")
}

func TestSpawnCleansUpCloneOnBackendFailure(t *testing.T) {
	be := failUpBackend{backend.NewFake()}
	f := &Fleet{Backend: be, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do the thing"); err == nil {
		t.Fatal("Spawn: expected error when Create fails, got nil")
	}
	// The clone dir must have been removed, leaving the work root empty.
	entries, err := os.ReadDir(f.WorkRoot)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", f.WorkRoot, err)
	}
	if len(entries) != 0 {
		t.Errorf("WorkRoot not empty after failed Spawn: %v", entries)
	}
}

// failInjectBackend wraps a Fake but errors from CopyTo, to exercise Spawn's
// post-provision cleanup (the container AND the clone must be removed).
type failInjectBackend struct{ *backend.Fake }

func (failInjectBackend) CopyTo(context.Context, string, string, string) error {
	return errors.New("boom")
}

func TestSpawnCleansUpContainerAndCloneOnPostProvisionFailure(t *testing.T) {
	fake := backend.NewFake()
	be := failInjectBackend{fake}
	f := &Fleet{Backend: be, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err == nil {
		t.Fatal("expected error when CopyTo fails after provisioning")
	}
	// Clone removed:
	entries, err := os.ReadDir(f.WorkRoot)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", f.WorkRoot, err)
	}
	if len(entries) != 0 {
		t.Errorf("WorkRoot not empty after failed spawn: %v", entries)
	}
	// Container not left orphaned in the fleet:
	got, _ := fake.List(context.Background(), nil)
	if len(got) != 0 {
		t.Errorf("container left behind after failed spawn: %+v", got)
	}
}

func TestSpawnRemovesStaleWorkDir(t *testing.T) {
	fake := backend.NewFake()
	work := t.TempDir()
	// Pre-create the dir the first picked name ("atlas") will clone into, with junk.
	stale := filepath.Join(work, "atlas")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: work}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	a, err := f.Spawn(context.Background(), bareRepo(t), prof, "do")
	if err != nil {
		t.Fatalf("Spawn over a stale work dir: %v", err)
	}
	// The clone replaced the stale dir: the repo file exists, the leftover is gone.
	if _, err := os.Stat(filepath.Join(work, a.Name, "f.txt")); err != nil {
		t.Errorf("expected cloned repo file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, a.Name, "leftover.txt")); !os.IsNotExist(err) {
		t.Errorf("stale leftover.txt should be gone, stat err = %v", err)
	}
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

func TestSpawnSetsUpFirewallAndProxyEnv(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), EgressFirewall: true}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`, EgressAllow: []string{"api.anthropic.com"}}
	a, err := f.Spawn(context.Background(), bareRepo(t), prof, "do")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// A proxy + network exist for the agent.
	if len(fake.NetworkCreates) != 1 || fake.NetworkCreates[0] != netName(a.Name) {
		t.Errorf("NetworkCreates = %v", fake.NetworkCreates)
	}
	proxies, _ := fake.List(context.Background(), map[string]string{"flotilla.proxy": a.Name})
	if len(proxies) != 1 {
		t.Errorf("want a proxy sidecar, got %v", proxies)
	}
	// The launch env-file content carries the proxy env.
	var sawProxy bool
	for _, cp := range fake.CopyCalls {
		if strings.Contains(cp.HostPath, "flotilla-inject-") && strings.Contains(string(cp.Content), "HTTP_PROXY="+("http://"+proxyName(a.Name))) {
			sawProxy = true
		}
	}
	if !sawProxy {
		t.Errorf("expected HTTP_PROXY in the injected env-file")
	}
}

func TestSpawnSkipsFirewallWhenDisabled(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), EgressFirewall: false}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(fake.NetworkCreates) != 0 {
		t.Errorf("firewall should be skipped, got NetworkCreates=%v", fake.NetworkCreates)
	}
}

// failNetBackend errors on NetworkCreate to exercise fail-closed.
type failNetBackend struct{ *backend.Fake }

func (failNetBackend) NetworkCreate(context.Context, string, bool) error {
	return errors.New("boom")
}

func TestSpawnFailClosedRemovesEverythingOnFirewallError(t *testing.T) {
	fake := backend.NewFake()
	be := failNetBackend{fake}
	f := &Fleet{Backend: be, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), EgressFirewall: true}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err == nil {
		t.Fatal("expected fail-closed error when firewall setup fails")
	}
	// Agent container removed (no orphan), clone removed.
	cs, _ := fake.List(context.Background(), nil)
	for _, c := range cs {
		if c.Labels[backend.LabelAgent] != "" && c.Labels["flotilla.proxy"] == "" {
			t.Errorf("agent container left behind: %+v", c)
		}
	}
	entries, _ := os.ReadDir(f.WorkRoot)
	if len(entries) != 0 {
		t.Errorf("clone not cleaned: %v", entries)
	}
}
