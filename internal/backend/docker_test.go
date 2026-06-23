package backend

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestDockerEventsDecodes(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := &dockerBackend{}
	ch, err := d.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	// We can't easily trigger a flotilla-labelled container here; assert the
	// stream is established and closes cleanly on ctx timeout.
	for range ch { //nolint:revive // drain until close
	}
}

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

func TestDockerLifecycleIntegration(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available; skipping integration test")
	}
	ctx := context.Background()
	d := NewDocker()
	id, err := d.Create(ctx, CreateOpts{
		Name:   "flotilla-test-atlas",
		Image:  "alpine:3.20",
		Cmd:    []string{"sleep", "120"},
		Labels: map[string]string{LabelAgent: "atlas-test", LabelRepo: "r"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer d.Remove(ctx, id) //nolint:errcheck
	if err := d.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := d.List(ctx, map[string]string{LabelAgent: "atlas-test"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("List = %+v, want one running", got)
	}
	if err := d.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
