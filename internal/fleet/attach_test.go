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
