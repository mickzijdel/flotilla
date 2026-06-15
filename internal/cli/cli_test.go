package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func TestListJSONOutput(t *testing.T) {
	fake := backend.NewFake()
	ctx := context.Background()
	id, _ := fake.Create(ctx, backend.CreateOpts{Labels: map[string]string{backend.LabelAgent: "atlas", backend.LabelRepo: "r1"}})
	_ = fake.Start(ctx, id)

	root := BuildRoot(&fleet.Fleet{Backend: fake})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"list", "--json"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got []fleet.Agent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Name != "atlas" {
		t.Errorf("got %+v", got)
	}
}

func TestSpawnNoEgressFirewallFlagDisables(t *testing.T) {
	f := &fleet.Fleet{Backend: backend.NewFake(), EgressFirewall: true}
	root := BuildRoot(f)
	root.SetArgs([]string{"spawn", "https://example.com/x.git", "--agent", "claude", "--no-egress-firewall"})
	// The command will fail later (no docker/devcontainer), but the flag must
	// flip the fleet field before Spawn runs.
	_ = root.ExecuteContext(context.Background())
	if f.EgressFirewall {
		t.Error("--no-egress-firewall should set Fleet.EgressFirewall = false")
	}
}

func TestAgentsListsBuiltins(t *testing.T) {
	root := BuildRoot(&fleet.Fleet{Backend: backend.NewFake()})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"agents"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("claude")) || !bytes.Contains(buf.Bytes(), []byte("codex")) {
		t.Errorf("agents output missing builtins: %s", out)
	}
}
