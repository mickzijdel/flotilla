package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// Unit tests use the in-memory fake, where the proxy is "running" the instant
// it starts; collapse the liveness settle to a single immediate check so the
// suite stays fast.
func init() { proxyLivenessChecks = 0 }

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

// deadProxyBackend behaves like the fake but reports the proxy as exited, as a
// crashed squid (e.g. bad allowlist) would — to exercise the liveness check.
type deadProxyBackend struct{ *backend.Fake }

func (d deadProxyBackend) List(ctx context.Context, filter map[string]string) ([]backend.Container, error) {
	cs, err := d.Fake.List(ctx, filter)
	for i := range cs {
		if cs[i].Labels["flotilla.proxy"] != "" {
			cs[i].Status = "exited"
		}
	}
	return cs, err
}

func TestSetupFirewallFailsWhenProxyDies(t *testing.T) {
	ctx := context.Background()
	be := deadProxyBackend{backend.NewFake()}
	agentID, _ := be.Create(ctx, backend.CreateOpts{Name: "c", Network: "bridge", Labels: map[string]string{backend.LabelAgent: "atlas"}})

	err := setupFirewall(ctx, be, agentID, "atlas", []string{"x.com"})
	if err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("want a dead-proxy error, got %v", err)
	}
	// The agent must NOT have been confined to a dead proxy: bridge intact.
	nets, _ := be.ContainerNetworks(ctx, agentID)
	if len(nets) != 1 || nets[0] != "bridge" {
		t.Errorf("agent should stay on bridge when proxy is dead, got %v", nets)
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
