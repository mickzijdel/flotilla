package fleet

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/egress"
)

// ProxyImage is the pinned squid image (digest captured in the Task 1 spike).
const ProxyImage = "ubuntu/squid@sha256:6a097f68bae708cedbabd6188d68c7e2e7a38cedd05a176e1cc0ba29e3bbe029"

const proxyPort = 3128

// Liveness settle for the proxy after start: squid validates its config on
// startup and exits within ~1s on a bad allowlist, so we watch the container
// for a short window before trusting it. Overridden to a single immediate check
// in tests.
var (
	proxyLivenessChecks   = 12
	proxyLivenessInterval = 250 * time.Millisecond
)

func proxyName(agent string) string { return "flotilla-proxy-" + agent }
func netName(agent string) string   { return "flotilla-net-" + agent }

// proxyEnv is the proxy environment injected into the agent so its HTTP(S)
// traffic routes through the sidecar (the only route out of the internal net).
func proxyEnv(agent string) map[string]string {
	url := fmt.Sprintf("http://%s:%d", proxyName(agent), proxyPort)
	return map[string]string{
		"HTTP_PROXY":  url,
		"HTTPS_PROXY": url,
		"http_proxy":  url,
		"https_proxy": url,
		"NO_PROXY":    "localhost,127.0.0.1",
		"no_proxy":    "localhost,127.0.0.1",
	}
}

// setupFirewall confines an already-provisioned agent to the allowlist: it
// starts a per-agent squid sidecar on a fresh --internal network and swaps the
// agent onto that network (removing its direct route). Fail-closed: on any error
// it tears down whatever it created and returns the error, so the caller never
// launches an unconfined agent. Refuses unsupported (non-bridge) topologies
// rather than silently leaving a bypass.
func setupFirewall(ctx context.Context, be backend.Backend, agentID, agentName string, allowlist []string) error {
	// Guard: only a plain single-bridge agent is supported (compose/custom nets
	// would leave a bypass route). Refuse otherwise.
	nets, err := be.ContainerNetworks(ctx, agentID)
	if err != nil {
		return fmt.Errorf("inspect agent networks: %w", err)
	}
	if len(nets) != 1 || nets[0] != "bridge" {
		return fmt.Errorf("egress firewall: unsupported network topology %v (compose devcontainer?) — set EgressFirewall=false to skip", nets)
	}

	fail := func(e error) error {
		teardownFirewall(ctx, be, agentName)
		return e
	}

	// Internal network (no route to the internet).
	if err := be.NetworkCreate(ctx, netName(agentName), true); err != nil {
		return fail(fmt.Errorf("create internal network: %w", err))
	}

	// Render squid.conf to a host temp file and start the proxy on the default
	// bridge (internet), then attach it to the internal net so the agent can
	// reach it.
	conf, err := os.CreateTemp("", "flotilla-squid-*.conf")
	if err != nil {
		return fail(fmt.Errorf("squid conf temp: %w", err))
	}
	confPath := conf.Name()
	defer func() { _ = os.Remove(confPath) }()
	if _, err := conf.WriteString(egress.SquidConf(allowlist, proxyPort)); err != nil {
		_ = conf.Close()
		return fail(fmt.Errorf("write squid conf: %w", err))
	}
	if err := conf.Close(); err != nil {
		return fail(fmt.Errorf("close squid conf: %w", err))
	}

	proxyID, err := be.Create(ctx, backend.CreateOpts{
		Name:    proxyName(agentName),
		Image:   ProxyImage,
		Network: "bridge",
		Mounts:  []backend.Mount{{Source: confPath, Target: "/etc/squid/squid.conf"}},
		Labels:  map[string]string{backend.LabelProxy: agentName, backend.LabelAgent: agentName},
	})
	if err != nil {
		return fail(fmt.Errorf("create proxy: %w", err))
	}
	if err := be.Start(ctx, proxyID); err != nil {
		return fail(fmt.Errorf("start proxy: %w", err))
	}
	if err := be.NetworkConnect(ctx, netName(agentName), proxyID); err != nil {
		return fail(fmt.Errorf("attach proxy to internal net: %w", err))
	}

	// Confirm the proxy actually stayed up before confining the agent: a squid
	// that crashes on a bad config exits non-zero but `start` already returned,
	// so without this the agent would be swapped onto a dead proxy (no egress,
	// no error). Checking here keeps the agent on the bridge if the proxy died.
	if err := waitProxyRunning(ctx, be, agentName); err != nil {
		return fail(err)
	}

	// Swap the agent: join the internal net, leave the bridge → its only route
	// out is now the proxy.
	if err := be.NetworkConnect(ctx, netName(agentName), agentID); err != nil {
		return fail(fmt.Errorf("attach agent to internal net: %w", err))
	}
	if err := be.NetworkDisconnect(ctx, "bridge", agentID); err != nil {
		return fail(fmt.Errorf("disconnect agent from bridge: %w", err))
	}
	return nil
}

// waitProxyRunning watches the per-agent proxy over a short settle window and
// errors if it is missing or has exited (a crashed squid), so the caller can
// fail the spawn instead of confining the agent to a dead proxy.
func waitProxyRunning(ctx context.Context, be backend.Backend, agentName string) error {
	for i := 0; ; i++ {
		cs, err := be.List(ctx, map[string]string{backend.LabelProxy: agentName})
		if err != nil {
			return fmt.Errorf("inspect proxy: %w", err)
		}
		if len(cs) == 0 {
			return fmt.Errorf("egress proxy %q not found after start", proxyName(agentName))
		}
		if cs[0].Status != "running" {
			return fmt.Errorf("egress proxy %q is %s (squid likely rejected the allowlist; check `docker logs %s`)", proxyName(agentName), cs[0].Status, proxyName(agentName))
		}
		if i >= proxyLivenessChecks {
			return nil
		}
		time.Sleep(proxyLivenessInterval)
	}
}

// teardownFirewall removes the per-agent proxy + network (best-effort, idempotent).
func teardownFirewall(ctx context.Context, be backend.Backend, agentName string) {
	if c, err := be.List(ctx, map[string]string{backend.LabelProxy: agentName}); err == nil {
		for _, p := range c {
			_ = be.Remove(ctx, p.ID)
		}
	}
	_ = be.NetworkRemove(ctx, netName(agentName))
}
