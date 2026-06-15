package fleet

import (
	"context"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestStopThenRemove(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas"}})
	_ = fake.Start(ctx, id)

	f := &Fleet{Backend: fake}
	if err := f.Stop(ctx, "atlas"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	cs, _ := fake.List(ctx, nil)
	if cs[0].Status != "exited" {
		t.Errorf("status = %q, want exited", cs[0].Status)
	}
	if err := f.Remove(ctx, "atlas"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	cs, _ = fake.List(ctx, nil)
	if len(cs) != 0 {
		t.Errorf("after Remove len = %d, want 0", len(cs))
	}
}
