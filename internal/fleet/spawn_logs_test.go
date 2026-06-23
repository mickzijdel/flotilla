package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

// mountTargets returns the set of mount targets recorded on the fake's only Up.
func mountTargets(t *testing.T, fake *backend.Fake) (string, []string) {
	t.Helper()
	if len(fake.UpCalls) != 1 {
		t.Fatalf("UpCalls = %d, want 1", len(fake.UpCalls))
	}
	up := fake.UpCalls[0]
	var targets []string
	for _, m := range up.Mounts {
		targets = append(targets, m.Target)
	}
	return up.Labels[backend.LabelLogDir], targets
}

func TestSpawnMountsSessionAndLiveTranscript(t *testing.T) {
	fake := backend.NewFake()
	fake.RemoteUser = "ubuntu"
	fake.ReadConfigResult = backend.ConfigInfo{RemoteUser: "ubuntu"}
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), LogRoot: t.TempDir()}
	prof := agent.Profile{Name: "claude", Launch: `echo "{prompt}"`, TranscriptPath: "~/.claude/projects"}

	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	logDir, targets := mountTargets(t, fake)
	if logDir == "" || filepath.Dir(filepath.Dir(logDir)) != f.LogRoot {
		t.Errorf("logdir label = %q, want under %q", logDir, f.LogRoot)
	}
	if !contains(targets, containerSessionDir) {
		t.Errorf("missing fixed session mount %q in %v", containerSessionDir, targets)
	}
	if !contains(targets, "/home/ubuntu/.claude/projects") {
		t.Errorf("missing live transcript mount in %v", targets)
	}
	// Host transcript dir was created.
	if _, err := os.Stat(filepath.Join(logDir, "transcript")); err != nil {
		t.Errorf("host transcript dir missing: %v", err)
	}
	// No copy-fallback sentinel when live-mounted.
	if _, err := os.Stat(filepath.Join(logDir, ".copy-fallback")); !os.IsNotExist(err) {
		t.Errorf(".copy-fallback should be absent on live mount, stat err = %v", err)
	}
}

func TestSpawnCopyFallbackWhenRemoteUserUnresolved(t *testing.T) {
	fake := backend.NewFake()
	fake.RemoteUser = "ubuntu"                                 // known post-up (from Up result)
	fake.ReadConfigResult = backend.ConfigInfo{RemoteUser: ""} // unresolved pre-up
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), LogRoot: t.TempDir()}
	prof := agent.Profile{Name: "claude", Launch: `echo "{prompt}"`, TranscriptPath: "~/.claude/projects"}

	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	logDir, targets := mountTargets(t, fake)
	if contains(targets, "/home/ubuntu/.claude/projects") {
		t.Errorf("transcript should NOT be live-mounted when unresolved: %v", targets)
	}
	if !contains(targets, containerSessionDir) {
		t.Errorf("fixed session mount still required: %v", targets)
	}
	b, err := os.ReadFile(filepath.Join(logDir, ".copy-fallback"))
	if err != nil {
		t.Fatalf("expected .copy-fallback sentinel: %v", err)
	}
	if got := string(b); got != "/home/ubuntu/.claude/projects\n" {
		t.Errorf(".copy-fallback = %q, want the resolved abs path", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
