package backend

import (
	"context"
	"testing"
)

func TestFakeLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	id, err := f.Create(ctx, CreateOpts{
		Name:   "atlas",
		Image:  "ubuntu:24.04",
		Cmd:    []string{"sleep", "infinity"},
		Labels: map[string]string{LabelAgent: "atlas", LabelRepo: "git@x:r.git"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := f.List(ctx, map[string]string{LabelAgent: "atlas"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("List = %+v, want one running container", got)
	}
	if err := f.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got, _ = f.List(ctx, nil)
	if got[0].Status != "exited" {
		t.Errorf("after Stop status = %q, want exited", got[0].Status)
	}
	if err := f.Remove(ctx, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = f.List(ctx, nil)
	if len(got) != 0 {
		t.Errorf("after Remove List = %+v, want empty", got)
	}
}
