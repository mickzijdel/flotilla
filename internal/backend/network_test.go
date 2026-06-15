package backend

import (
	"context"
	"testing"
)

func TestDockerNetworkLifecycleIntegration(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available; skipping integration test")
	}
	ctx := context.Background()
	d := NewDocker()
	const net = "flotilla-net-test-egress"
	if err := d.NetworkCreate(ctx, net, true); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	defer d.NetworkRemove(ctx, net) //nolint:errcheck
	id, err := d.Create(ctx, CreateOpts{
		Name: "flotilla-net-test-c", Image: "alpine:3.20", Cmd: []string{"sleep", "60"},
		Labels: map[string]string{LabelAgent: "nettest"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer d.Remove(ctx, id) //nolint:errcheck
	if err := d.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.NetworkConnect(ctx, net, id); err != nil {
		t.Fatalf("NetworkConnect: %v", err)
	}
	nets, err := d.ContainerNetworks(ctx, id)
	if err != nil {
		t.Fatalf("ContainerNetworks: %v", err)
	}
	found := false
	for _, n := range nets {
		if n == net {
			found = true
		}
	}
	if !found {
		t.Errorf("ContainerNetworks = %v, want it to include %q", nets, net)
	}
}
