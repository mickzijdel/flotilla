package fleet

import (
	"context"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestListMapsContainersToAgents(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{
		Labels: map[string]string{backend.LabelAgent: "atlas", backend.LabelRepo: "r1"},
	})
	_ = fake.Start(ctx, id)

	f := &Fleet{Backend: fake}
	got, err := f.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Name != "atlas" || got[0].Repo != "r1" || got[0].Status != "running" {
		t.Errorf("agent = %+v", got[0])
	}
}
