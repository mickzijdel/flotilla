package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestSetupFirewallWiresProxyAndSwapsNetwork(t *testing.T) {
	ctx := context.Background()
	fake := backend.NewFake()
	// The agent container starts on "bridge".
	agentID, _ := fake.Create(ctx, backend.CreateOpts{Name: "flotilla-atlas", Network: "bridge", Labels: map[string]string{backend.LabelAgent: "atlas"}})

	if err := setupFirewall(ctx, fake, agentID, "atlas", []string{"api.anthropic.com"}); err != nil {
		t.Fatalf("setupFirewall: %v", err)
	}
	// A per-agent network was created.
	if len(fake.NetworkCreates) != 1 || fake.NetworkCreates[0] != netName("atlas") {
		t.Fatalf("NetworkCreates = %v", fake.NetworkCreates)
	}
	// The agent ended up on the internal net only (bridge disconnected).
	nets, _ := fake.ContainerNetworks(ctx, agentID)
	if len(nets) != 1 || nets[0] != netName("atlas") {
		t.Errorf("agent networks = %v, want [%s]", nets, netName("atlas"))
	}
	// A proxy container exists, labeled flotilla.proxy=atlas.
	proxies, _ := fake.List(ctx, map[string]string{"flotilla.proxy": "atlas"})
	if len(proxies) != 1 {
		t.Errorf("want 1 proxy container, got %v", proxies)
	}
}

func TestSetupFirewallRefusesUnsupportedTopology(t *testing.T) {
	ctx := context.Background()
	fake := backend.NewFake()
	// Simulate a compose devcontainer: started on a non-bridge network.
	agentID, _ := fake.Create(ctx, backend.CreateOpts{Name: "c", Network: "some-compose-net", Labels: map[string]string{backend.LabelAgent: "atlas"}})
	err := setupFirewall(ctx, fake, agentID, "atlas", []string{"x.com"})
	if err == nil || !strings.Contains(err.Error(), "topology") {
		t.Errorf("want unsupported-topology error, got %v", err)
	}
}

func TestProxyEnvPointsAtSidecar(t *testing.T) {
	env := proxyEnv("atlas")
	want := "http://" + proxyName("atlas") + ":3128"
	if env["HTTP_PROXY"] != want || env["HTTPS_PROXY"] != want {
		t.Errorf("proxyEnv = %v, want HTTP(S)_PROXY=%s", env, want)
	}
	if !strings.Contains(env["NO_PROXY"], "localhost") {
		t.Errorf("NO_PROXY should include localhost: %v", env["NO_PROXY"])
	}
}
