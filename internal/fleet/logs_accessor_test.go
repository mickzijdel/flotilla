package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// seedLoggedAgent registers a fake container labelled name with a logdir whose
// status file says status, and returns the fake + log dir.
func seedLoggedAgent(t *testing.T, fake *backend.Fake, name, status string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "transcript"), 0o777); err != nil {
		t.Fatal(err)
	}
	if status != "" {
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = fake.Create(context.Background(), backend.CreateOpts{Labels: map[string]string{
		backend.LabelAgent:  name,
		backend.LabelRepo:   "r",
		backend.LabelLogDir: dir,
	}})
	return dir
}

func TestLogsResolvesDirAndStatus(t *testing.T) {
	fake := backend.NewFake()
	dir := seedLoggedAgent(t, fake, "atlas", "done")
	f := &Fleet{Backend: fake}
	info, err := f.Logs(context.Background(), "atlas")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if info.LogDir != dir || info.Status != "done" {
		t.Errorf("info = %+v, want dir %q status done", info, dir)
	}
	if info.TranscriptPath != filepath.Join(dir, "transcript") {
		t.Errorf("TranscriptPath = %q", info.TranscriptPath)
	}
}

func TestLogsErrorsWithoutLabel(t *testing.T) {
	fake := backend.NewFake()
	_, _ = fake.Create(context.Background(), backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas", backend.LabelRepo: "r"}})
	f := &Fleet{Backend: fake}
	if _, err := f.Logs(context.Background(), "atlas"); err == nil {
		t.Error("expected error when no logdir label is set")
	}
}

func TestLogsCopyFallbackOnExit(t *testing.T) {
	fake := backend.NewFake()
	dir := seedLoggedAgent(t, fake, "atlas", "done")
	// Flag copy-fallback and mark the container exited.
	if err := os.WriteFile(filepath.Join(dir, ".copy-fallback"), []byte("/home/ubuntu/.claude/projects\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs, _ := fake.List(context.Background(), map[string]string{backend.LabelAgent: "atlas"})
	_ = fake.SetStatus(cs[0].ID, "exited")

	f := &Fleet{Backend: fake}
	if _, err := f.Logs(context.Background(), "atlas"); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(fake.CopyFromCalls) != 1 {
		t.Fatalf("CopyFrom calls = %d, want 1", len(fake.CopyFromCalls))
	}
	if fake.CopyFromCalls[0].DestPath != "/home/ubuntu/.claude/projects/." {
		t.Errorf("CopyFrom src = %q, want trailing /.", fake.CopyFromCalls[0].DestPath)
	}
	// Idempotent: the fake's CopyFrom populated the transcript dir, so a second
	// call must not copy again.
	if _, err := f.Logs(context.Background(), "atlas"); err != nil {
		t.Fatalf("Logs (2nd): %v", err)
	}
	if len(fake.CopyFromCalls) != 1 {
		t.Errorf("CopyFrom should be idempotent, got %d calls", len(fake.CopyFromCalls))
	}
}
