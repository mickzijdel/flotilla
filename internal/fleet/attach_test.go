package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestAttachReturnsInfoForNamedAgent(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})
	_ = fake.Start(ctx, id)

	f := &Fleet{Backend: fake}
	info, err := f.Attach(ctx, "atlas")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !strings.Contains(info.DockerExec, id) {
		t.Errorf("DockerExec = %q, want it to mention %q", info.DockerExec, id)
	}
}

func TestAttachUnknownAgentErrors(t *testing.T) {
	f := &Fleet{Backend: backend.NewFake()}
	if _, err := f.Attach(context.Background(), "nope"); err == nil {
		t.Error("expected error for unknown agent")
	}
}

func TestAttachAutoStartsExitedContainer(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})
	_ = fake.Stop(ctx, id) // exited

	f := &Fleet{Backend: fake}
	if _, err := f.Attach(ctx, "atlas"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	cs, _ := fake.List(ctx, map[string]string{backend.LabelAgent: "atlas"})
	if cs[0].Status != "running" {
		t.Errorf("Status = %q, want running (attach should auto-start)", cs[0].Status)
	}
}

func TestAttachAutoStartsNonRunningContainer(t *testing.T) {
	// A container that was created but never started (status "created") is also
	// not execable; attach must start it, not just when it has cleanly "exited".
	fake := backend.NewFake()
	ctx := context.Background()
	_, _ = fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})

	f := &Fleet{Backend: fake}
	if _, err := f.Attach(ctx, "atlas"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	cs, _ := fake.List(ctx, map[string]string{backend.LabelAgent: "atlas"})
	if cs[0].Status != "running" {
		t.Errorf("Status = %q, want running (attach should start a non-running container)", cs[0].Status)
	}
}
