# Flotilla Egress Firewall (proxy model) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Confine each spawned agent to a default-deny egress allowlist by putting it on a Docker `--internal` network whose only route out is a per-agent `ubuntu/squid` proxy sidecar that allows HTTP(S) only to allowlisted hostnames.

**Architecture:** Enforcement lives *outside* the agent container (no `NET_ADMIN`, no in-container iptables). The engine renders an allowlist (baked default ∪ `profile.EgressAllow` ∪ `Fleet.EgressAllow`) into a per-agent `squid.conf`, starts a pinned `ubuntu/squid` sidecar on a fresh `--internal` network, network-swaps the agent onto that internal network (so it loses its direct route), and sets `HTTP(S)_PROXY` in the agent env. Fail-closed: any setup error aborts the spawn; `Fleet.EgressFirewall` (default true) opts out.

**Tech Stack:** Go 1.23+ (run as `mise exec -- go ...`), `docker` CLI (network create/connect/disconnect, run), `ubuntu/squid`, stdlib `testing`.

**Spec:** [docs/specs/2026-06-15-flotilla-egress-firewall-design.md](../specs/2026-06-15-flotilla-egress-firewall-design.md)

---

## File Structure

```
flotilla/
  internal/
    egress/
      egress.go            # NEW: BakedAllowlist, Compose(), SquidConf() — pure
      egress_test.go       # NEW
    backend/
      backend.go           # MODIFY: + CreateOpts.Network; + network methods on Backend
      network.go           # NEW: dockerBackend network primitives (shell `docker network …`)
      network_test.go      # NEW: integration (skips without docker)
      fake.go              # MODIFY: record network calls + Create honors Network; ContainerNetworks
      fake_test.go         # MODIFY: cover the new fake methods
    fleet/
      firewall.go          # NEW: proxy/net naming, setupFirewall(), teardownFirewall()
      firewall_test.go     # NEW
      fleet.go             # MODIFY: Fleet.EgressFirewall + EgressAllow; Spawn wiring; Stop/Remove teardown
      fleet_test.go        # MODIFY: firewall ordering, fail-closed, skip-when-off
    cli/
      cli.go               # MODIFY: spawn --no-egress-firewall flag
  main.go                  # MODIFY: default Fleet.EgressFirewall = true
  docs/
    notes/2026-06-15-egress-spike.md   # NEW (Task 1)
  docs/backlog.md          # MODIFY
```

**Type contracts introduced here (names exact):**

- `egress.BakedAllowlist() []string`; `egress.Compose(baked, profile, fleet []string) []string`; `egress.SquidConf(allowlist []string, port int) string`
- `backend.CreateOpts.Network string` (network to attach at create; "" = default bridge)
- `backend.Backend` gains: `NetworkCreate(ctx, name string, internal bool) error`, `NetworkRemove(ctx, name string) error`, `NetworkConnect(ctx, network, id string) error`, `NetworkDisconnect(ctx, network, id string) error`, `ContainerNetworks(ctx, id string) ([]string, error)`
- fleet helpers: `proxyName(agent string) string` → `"flotilla-proxy-"+agent`; `netName(agent string) string` → `"flotilla-net-"+agent`; const `proxyPort = 3128`; `proxyEnv(agent string) map[string]string`
- `fleet.Fleet.EgressFirewall bool`, `fleet.Fleet.EgressAllow []string`
- `fleet.setupFirewall(ctx, be backend.Backend, agentID, agentName, allowlist []string) error`; `fleet.teardownFirewall(ctx, be backend.Backend, agentName string)`
- const `ProxyImage` = pinned `ubuntu/squid@sha256:<digest from Task 1>`

---

## Task 1: Spike — verify network-swap + squid allowlist (manual; de-risks)

Exploratory (not TDD). Record outcomes; later tasks ship defaults regardless.

**Files:** Create `docs/notes/2026-06-15-egress-spike.md`

- [ ] **Step 1: Pull + pin the squid image; capture the digest**

```bash
docker pull ubuntu/squid:latest
docker inspect --format '{{index .RepoDigests 0}}' ubuntu/squid:latest
```
Record the `ubuntu/squid@sha256:…` digest — this becomes `ProxyImage` in Task 5.

- [ ] **Step 2: Build a squid allowlist config + run the proxy on an internal+bridge setup**

```bash
tmp=$(mktemp -d)
cat > "$tmp/squid.conf" <<'CONF'
http_port 3128
acl SSL_ports port 443
acl CONNECT method CONNECT
http_access deny CONNECT !SSL_ports
acl allowed dstdomain .example.org
http_access allow allowed
http_access deny all
cache deny all
CONF
docker network create --internal flotilla-net-spike
# proxy on default bridge (internet) + the internal net:
docker run -d --name flotilla-proxy-spike \
  -v "$tmp/squid.conf:/etc/squid/squid.conf:ro" ubuntu/squid:latest
docker network connect flotilla-net-spike flotilla-proxy-spike
docker logs flotilla-proxy-spike 2>&1 | tail -5   # confirm squid started, parsed config
```

- [ ] **Step 3: Network-swap a test container + verify allow/deny + no direct route**

```bash
docker run -d --name flotilla-agent-spike alpine:3.20 sleep 600
docker network connect flotilla-net-spike flotilla-agent-spike
docker network disconnect bridge flotilla-agent-spike
docker exec flotilla-agent-spike sh -c 'apk add --no-cache curl >/dev/null 2>&1; \
  echo "via-proxy allowed:"; curl -s -o /dev/null -w "%{http_code}\n" -x http://flotilla-proxy-spike:3128 https://example.org; \
  echo "via-proxy denied:"; curl -s -o /dev/null -w "%{http_code}\n" -x http://flotilla-proxy-spike:3128 https://example.com; \
  echo "direct (should fail/timeout):"; curl -s -m 5 -o /dev/null -w "%{http_code}\n" https://example.com || echo "no direct route (good)"'
# also confirm engine control still works after the swap:
docker exec flotilla-agent-spike echo "exec still works after swap"
```
Record: allowed host returns a real code (200/3xx), denied host is refused (403 from squid), direct connection fails (no route), and `docker exec` still works. Note the network name the agent started on (expected `bridge`).

- [ ] **Step 4: Cleanup + write findings**

```bash
docker rm -f flotilla-agent-spike flotilla-proxy-spike 2>/dev/null
docker network rm flotilla-net-spike 2>/dev/null
rm -rf "$tmp"
```
Write `docs/notes/2026-06-15-egress-spike.md` with: the pinned digest (Step 1), allow/deny/no-direct-route results (Step 3), the agent's starting network name, and any squid config tweak needed. Commit:
```bash
git add docs/notes/2026-06-15-egress-spike.md
git commit -m "docs(spike): verify egress network-swap + squid allowlist mechanics"
```

> **Orchestrator note:** carry the Step-1 digest into Task 5's `ProxyImage` const.

---

## Task 2: Allowlist composition + baked default (pure, TDD)

**Files:** Create `internal/egress/egress.go`, `internal/egress/egress_test.go`

- [ ] **Step 1: Write the failing test**

`internal/egress/egress_test.go`:
```go
package egress

import (
	"reflect"
	"testing"
)

func TestComposeUnionsDedupesSorts(t *testing.T) {
	got := Compose([]string{"github.com", "npmjs.org"}, []string{"api.anthropic.com"}, []string{"github.com", "extra.example"})
	want := []string{"api.anthropic.com", "extra.example", "github.com", "npmjs.org"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose = %v, want %v", got, want)
	}
}

func TestComposeDropsEmpty(t *testing.T) {
	got := Compose([]string{"github.com", ""}, nil, []string{"  "})
	want := []string{"github.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose = %v, want %v", got, want)
	}
}

func TestBakedAllowlistHasEssentials(t *testing.T) {
	baked := BakedAllowlist()
	for _, must := range []string{"github.com", "registry.npmjs.org", "pypi.org", "ghcr.io"} {
		found := false
		for _, d := range baked {
			if d == must {
				found = true
			}
		}
		if !found {
			t.Errorf("baked allowlist missing %q", must)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/egress/ -v`
Expected: FAIL — `Compose`, `BakedAllowlist` undefined.

- [ ] **Step 3: Write the implementation**

`internal/egress/egress.go`:
```go
// Package egress builds the per-agent egress allowlist and the squid proxy
// config that enforces it (default-deny, allow only listed hostnames).
package egress

import (
	"sort"
	"strings"
)

// BakedAllowlist is the default set of hostnames a coding agent needs at run
// time. The agent API endpoint comes from the profile, not here.
func BakedAllowlist() []string {
	return []string{
		// GitHub (read-only; no creds in the box means it still cannot push)
		"github.com", "api.github.com", "codeload.github.com",
		"objects.githubusercontent.com", "raw.githubusercontent.com",
		// Package registries / toolchains
		"registry.npmjs.org", "pypi.org", "files.pythonhosted.org",
		"ghcr.io", "deb.nodesource.com", "mise.jdx.dev",
		"crates.io", "static.crates.io", "proxy.golang.org", "sum.golang.org",
	}
}

// Compose unions the three allowlist sources, dropping blanks, deduping, and
// sorting for a deterministic result.
func Compose(baked, profile, fleet []string) []string {
	set := map[string]bool{}
	for _, src := range [][]string{baked, profile, fleet} {
		for _, d := range src {
			d = strings.TrimSpace(d)
			if d != "" {
				set[d] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `mise exec -- go test ./internal/egress/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/egress/egress.go internal/egress/egress_test.go
git commit -m "feat(egress): baked default allowlist + Compose (union/dedup/sort)"
```

---

## Task 3: squid.conf rendering (pure, TDD)

**Files:** Modify `internal/egress/egress.go`, `internal/egress/egress_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/egress/egress_test.go`:
```go
func TestSquidConfDefaultDenyAndAllowlist(t *testing.T) {
	conf := SquidConf([]string{"api.anthropic.com", "github.com"}, 3128)
	for _, must := range []string{
		"http_port 3128",
		"acl CONNECT method CONNECT",
		"http_access deny CONNECT !SSL_ports",
		"acl allowed dstdomain .api.anthropic.com .github.com",
		"http_access allow allowed",
		"http_access deny all",
		"cache deny all",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("squid.conf missing %q\n---\n%s", must, conf)
		}
	}
}

func TestSquidConfEmptyAllowlistStillDenies(t *testing.T) {
	conf := SquidConf(nil, 3128)
	if !strings.Contains(conf, "http_access deny all") {
		t.Errorf("empty allowlist must still default-deny:\n%s", conf)
	}
}
```
Add `"strings"` to the test imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/egress/ -run TestSquidConf -v`
Expected: FAIL — `SquidConf` undefined.

- [ ] **Step 3: Write the implementation**

Append to `internal/egress/egress.go`:
```go
import "fmt"  // add to the existing import block (with sort, strings)

// SquidConf renders a squid config that default-denies egress and allows HTTP(S)
// only to the allowlisted hostnames (as dstdomain suffixes, so api.x.com matches
// .x.com). CONNECT is restricted to 443. Caching is off.
func SquidConf(allowlist []string, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "http_port %d\n", port)
	b.WriteString("acl SSL_ports port 443\n")
	b.WriteString("acl CONNECT method CONNECT\n")
	b.WriteString("http_access deny CONNECT !SSL_ports\n")
	if len(allowlist) > 0 {
		b.WriteString("acl allowed dstdomain")
		for _, d := range allowlist {
			fmt.Fprintf(&b, " .%s", strings.TrimPrefix(d, "."))
		}
		b.WriteString("\nhttp_access allow allowed\n")
	}
	b.WriteString("http_access deny all\n")
	b.WriteString("cache deny all\n")
	return b.String()
}
```
(Note: the `import "fmt"` must be merged into the file's single import block alongside `sort` and `strings` — do not add a second `import` statement.)

- [ ] **Step 4: Run test to verify it passes**

Run: `mise exec -- go test ./internal/egress/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/egress/egress.go internal/egress/egress_test.go
git commit -m "feat(egress): render default-deny squid.conf from an allowlist"
```

---

## Task 4: Backend network primitives + CreateOpts.Network (interface + fake + docker)

One commit so the build stays green (both `Fake` and `dockerBackend` satisfy the widened interface).

**Files:** Modify `internal/backend/backend.go`, `internal/backend/fake.go`, `internal/backend/fake_test.go`; Create `internal/backend/network.go`, `internal/backend/network_test.go`

- [ ] **Step 1: Write the failing fake test**

Append to `internal/backend/fake_test.go`:
```go
func TestFakeNetworkAndContainerNetworks(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if err := f.NetworkCreate(ctx, "flotilla-net-atlas", true); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	id, _ := f.Create(ctx, CreateOpts{Name: "p", Image: "ubuntu/squid", Network: "bridge", Labels: map[string]string{LabelAgent: "atlas"}})
	if err := f.NetworkConnect(ctx, "flotilla-net-atlas", id); err != nil {
		t.Fatalf("NetworkConnect: %v", err)
	}
	if err := f.NetworkDisconnect(ctx, "bridge", id); err != nil {
		t.Fatalf("NetworkDisconnect: %v", err)
	}
	nets, err := f.ContainerNetworks(ctx, id)
	if err != nil {
		t.Fatalf("ContainerNetworks: %v", err)
	}
	// started on "bridge" (from Create.Network), connected internal, disconnected bridge.
	if len(nets) != 1 || nets[0] != "flotilla-net-atlas" {
		t.Errorf("ContainerNetworks = %v, want [flotilla-net-atlas]", nets)
	}
	if err := f.NetworkRemove(ctx, "flotilla-net-atlas"); err != nil {
		t.Fatalf("NetworkRemove: %v", err)
	}
	if len(f.NetworkCreates) != 1 || len(f.NetworkRemoves) != 1 {
		t.Errorf("network calls not recorded: creates=%v removes=%v", f.NetworkCreates, f.NetworkRemoves)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/backend/ -run TestFakeNetwork -v`
Expected: FAIL — `NetworkCreate`, `CreateOpts.Network`, etc. undefined.

- [ ] **Step 3: Widen the interface + CreateOpts**

In `internal/backend/backend.go`, add `Network` to `CreateOpts`:
```go
type CreateOpts struct {
	Name    string
	Image   string
	Cmd     []string
	Workdir string
	Mounts  []Mount
	Env     map[string]string
	Labels  map[string]string
	Network string // network to attach at create ("" = default bridge)
}
```
Add to the `Backend` interface:
```go
	NetworkCreate(ctx context.Context, name string, internal bool) error
	NetworkRemove(ctx context.Context, name string) error
	NetworkConnect(ctx context.Context, network, id string) error
	NetworkDisconnect(ctx context.Context, network, id string) error
	ContainerNetworks(ctx context.Context, id string) ([]string, error)
```

- [ ] **Step 4: Implement on the fake**

In `internal/backend/fake.go`: add recording fields to the `Fake` struct:
```go
	NetworkCreates  []string
	NetworkRemoves  []string
	nets            map[string][]string // id -> attached network names
```
Initialize `nets` in `NewFake`: change it to
```go
func NewFake() *Fake { return &Fake{items: map[string]*Container{}, nets: map[string][]string{}} }
```
In `Fake.Create`, after computing `id` and storing the container, record its initial network:
```go
	if o.Network != "" {
		f.nets[id] = []string{o.Network}
	}
```
Also in `Fake.Up`, after creating the container, record it on `bridge` (mimicking how `devcontainer up` places the container on the default bridge — `setupFirewall`'s topology guard checks for exactly `["bridge"]`):
```go
	f.nets[id] = []string{"bridge"}
```
Add the methods:
```go
func (f *Fake) NetworkCreate(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.NetworkCreates = append(f.NetworkCreates, name)
	return nil
}

func (f *Fake) NetworkRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.NetworkRemoves = append(f.NetworkRemoves, name)
	return nil
}

func (f *Fake) NetworkConnect(_ context.Context, network, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nets[id] = append(f.nets[id], network)
	return nil
}

func (f *Fake) NetworkDisconnect(_ context.Context, network, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.nets[id]
	out := cur[:0]
	for _, n := range cur {
		if n != network {
			out = append(out, n)
		}
	}
	f.nets[id] = out
	return nil
}

func (f *Fake) ContainerNetworks(_ context.Context, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nets[id]...), nil
}
```

- [ ] **Step 5: Implement on the docker backend**

`internal/backend/network.go`:
```go
package backend

import (
	"context"
	"strings"
)

func (d *dockerBackend) NetworkCreate(ctx context.Context, name string, internal bool) error {
	args := []string{"network", "create"}
	if internal {
		args = append(args, "--internal")
	}
	args = append(args, name)
	_, err := run(ctx, args...)
	return err
}

func (d *dockerBackend) NetworkRemove(ctx context.Context, name string) error {
	_, err := run(ctx, "network", "rm", name)
	return err
}

func (d *dockerBackend) NetworkConnect(ctx context.Context, network, id string) error {
	_, err := run(ctx, "network", "connect", network, id)
	return err
}

func (d *dockerBackend) NetworkDisconnect(ctx context.Context, network, id string) error {
	_, err := run(ctx, "network", "disconnect", network, id)
	return err
}

// ContainerNetworks lists the networks a container is attached to.
func (d *dockerBackend) ContainerNetworks(ctx context.Context, id string) ([]string, error) {
	out, err := run(ctx, "inspect", "-f",
		`{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}`, id)
	if err != nil {
		return nil, err
	}
	var nets []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			nets = append(nets, line)
		}
	}
	return nets, nil
}
```
Then make the docker `Create` honor `o.Network` — in `internal/backend/docker.go` `Create`, after the labels/env/mounts loop and before `if o.Workdir != ""`, add:
```go
	if o.Network != "" {
		args = append(args, "--network", o.Network)
	}
```

- [ ] **Step 6: Write the docker integration test (skips without docker)**

`internal/backend/network_test.go`:
```go
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
```

- [ ] **Step 7: Run tests + build**

Run: `mise exec -- go build ./... && mise exec -- go test ./internal/backend/ -v`
Expected: PASS (integration test SKIPs without docker; fake test passes).

- [ ] **Step 8: Commit**

```bash
git add internal/backend/
git commit -m "feat(backend): network primitives + CreateOpts.Network for egress proxy"
```

---

## Task 5: Firewall orchestration (setup/teardown, TDD against fake)

**Files:** Create `internal/fleet/firewall.go`, `internal/fleet/firewall_test.go`

- [ ] **Step 1: Write the failing test**

`internal/fleet/firewall_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/fleet/ -run 'TestSetupFirewall|TestProxyEnv' -v`
Expected: FAIL — `setupFirewall`, `netName`, `proxyName`, `proxyEnv` undefined.

- [ ] **Step 3: Write the implementation**

`internal/fleet/firewall.go`:
```go
package fleet

import (
	"context"
	"fmt"
	"os"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/egress"
)

// ProxyImage is the pinned squid image (digest captured in the Task 1 spike).
const ProxyImage = "ubuntu/squid@sha256:REPLACE_WITH_DIGEST_FROM_TASK_1"

const proxyPort = 3128

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
		Labels:  map[string]string{"flotilla.proxy": agentName, backend.LabelAgent: agentName},
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

// teardownFirewall removes the per-agent proxy + network (best-effort, idempotent).
func teardownFirewall(ctx context.Context, be backend.Backend, agentName string) {
	if c, err := be.List(ctx, map[string]string{"flotilla.proxy": agentName}); err == nil {
		for _, p := range c {
			_ = be.Remove(ctx, p.ID)
		}
	}
	_ = be.NetworkRemove(ctx, netName(agentName))
}
```

> **Orchestrator note:** replace `REPLACE_WITH_DIGEST_FROM_TASK_1` with the `ubuntu/squid@sha256:…` digest recorded in Task 1, Step 1. The unit tests use the fake (the const value is irrelevant to them); the digest matters only at the Task 9 integration/smoke.

Also: the fake's `List` filters by labels, and the proxy carries `flotilla.proxy=<agent>`. The fake stores `Container.Labels` from `CreateOpts.Labels`, so `List(map[string]string{"flotilla.proxy": agentName})` matches. (No fake change needed — `Create` already records `o.Labels`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `mise exec -- go test ./internal/fleet/ -run 'TestSetupFirewall|TestProxyEnv' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/firewall.go internal/fleet/firewall_test.go
git commit -m "feat(fleet): egress firewall setup/teardown — squid sidecar + network swap"
```

---

## Task 6: Wire the firewall into Spawn (fail-closed, toggle) + teardown on Stop/Remove

**Files:** Modify `internal/fleet/fleet.go`, `internal/fleet/fleet_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/fleet/fleet_test.go`:
```go
func TestSpawnSetsUpFirewallAndProxyEnv(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), EgressFirewall: true}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`, EgressAllow: []string{"api.anthropic.com"}}
	a, err := f.Spawn(context.Background(), bareRepo(t), prof, "do")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// A proxy + network exist for the agent.
	if len(fake.NetworkCreates) != 1 || fake.NetworkCreates[0] != netName(a.Name) {
		t.Errorf("NetworkCreates = %v", fake.NetworkCreates)
	}
	proxies, _ := fake.List(context.Background(), map[string]string{"flotilla.proxy": a.Name})
	if len(proxies) != 1 {
		t.Errorf("want a proxy sidecar, got %v", proxies)
	}
	// The launch env-file content carries the proxy env.
	var sawProxy bool
	for _, cp := range fake.CopyCalls {
		if strings.Contains(cp.HostPath, "flotilla-inject-") && strings.Contains(string(cp.Content), "HTTP_PROXY="+("http://"+proxyName(a.Name))) {
			sawProxy = true
		}
	}
	if !sawProxy {
		t.Errorf("expected HTTP_PROXY in the injected env-file")
	}
}

func TestSpawnSkipsFirewallWhenDisabled(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), EgressFirewall: false}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(fake.NetworkCreates) != 0 {
		t.Errorf("firewall should be skipped, got NetworkCreates=%v", fake.NetworkCreates)
	}
}

// failNetBackend errors on NetworkCreate to exercise fail-closed.
type failNetBackend struct{ *backend.Fake }

func (failNetBackend) NetworkCreate(context.Context, string, bool) error {
	return errors.New("boom")
}

func TestSpawnFailClosedRemovesEverythingOnFirewallError(t *testing.T) {
	fake := backend.NewFake()
	be := failNetBackend{fake}
	f := &Fleet{Backend: be, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir(), EgressFirewall: true}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do"); err == nil {
		t.Fatal("expected fail-closed error when firewall setup fails")
	}
	// Agent container removed (no orphan), clone removed.
	cs, _ := fake.List(context.Background(), nil)
	for _, c := range cs {
		if c.Labels[backend.LabelAgent] != "" && c.Labels["flotilla.proxy"] == "" {
			t.Errorf("agent container left behind: %+v", c)
		}
	}
	entries, _ := os.ReadDir(f.WorkRoot)
	if len(entries) != 0 {
		t.Errorf("clone not cleaned: %v", entries)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/fleet/ -run 'TestSpawnSetsUpFirewall|TestSpawnSkipsFirewall|TestSpawnFailClosed' -v`
Expected: FAIL — `Fleet.EgressFirewall` field missing; firewall not wired.

- [ ] **Step 3: Add the fields + wire Spawn**

In `internal/fleet/fleet.go`, add fields to `Fleet`:
```go
type Fleet struct {
	Backend       backend.Backend
	BaseImage     string
	WorkRoot      string // host dir holding per-agent clones; defaults under ~/.flotilla
	EgressFirewall bool     // default-deny egress via a per-agent proxy (default true via main)
	EgressAllow   []string  // engine-wide extra allowlist entries
}
```
Add `"github.com/mickzijdel/flotilla/internal/egress"` to the imports. In `Spawn`, two changes:

(a) compute the proxy env up-front and merge it into the secret env (so the launch env-file carries `HTTP_PROXY`). Replace the secrets block:
```go
	// 1) Secrets: resolved allowlist + (when firewalled) the proxy env → 0600
	//    env-file under the run user's home.
	env := resolveEnv(prof.Env, os.LookupEnv)
	if f.EgressFirewall {
		for k, v := range proxyEnv(name) {
			env[k] = v
		}
	}
	if err := inj.WriteFile(ctx, envFileContent(env), agentEnvFile(home)); err != nil {
		return fail(fmt.Errorf("inject secrets: %w", err))
	}
```

(b) insert the firewall step BETWEEN install (step 3) and launch (step 4), and make `fail` also tear down the proxy/net. Update the `fail` closure to:
```go
	fail := func(e error) (Agent, error) {
		if f.EgressFirewall {
			teardownFirewall(ctx, f.Backend, name)
		}
		_ = f.Backend.Remove(ctx, id)
		_ = os.RemoveAll(dest)
		return Agent{}, e
	}
```
and add, right after the install block and before the launch block:
```go
	// 3.5) Egress firewall: confine the agent to the allowlist (fail-closed).
	if f.EgressFirewall {
		allow := egress.Compose(egress.BakedAllowlist(), prof.EgressAllow, f.EgressAllow)
		if err := setupFirewall(ctx, f.Backend, id, name, allow); err != nil {
			return fail(fmt.Errorf("egress firewall: %w", err))
		}
	}
```

- [ ] **Step 4: Teardown on Stop/Remove**

Replace `Fleet.Stop` and `Fleet.Remove` in `internal/fleet/fleet.go`:
```go
// Stop stops a named agent's container and its egress proxy.
func (f *Fleet) Stop(ctx context.Context, name string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	if proxies, err := f.Backend.List(ctx, map[string]string{"flotilla.proxy": name}); err == nil {
		for _, p := range proxies {
			_ = f.Backend.Stop(ctx, p.ID)
		}
	}
	return f.Backend.Stop(ctx, c.ID)
}

// Remove force-removes a named agent's container, its egress proxy, and network.
func (f *Fleet) Remove(ctx context.Context, name string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	teardownFirewall(ctx, f.Backend, name)
	return f.Backend.Remove(ctx, c.ID)
}
```

- [ ] **Step 5: Run tests + build**

Run: `mise exec -- go build ./... && mise exec -- go test ./internal/fleet/ -v`
Expected: PASS — the three new tests, plus the existing Spawn/cred-isolation/stop tests still green. (The cred-isolation test runs with `EgressFirewall` unset → false → firewall skipped, unaffected.)

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/fleet.go internal/fleet/fleet_test.go
git commit -m "feat(fleet): wire egress firewall into Spawn (fail-closed) + teardown on stop/rm"
```

---

## Task 7: CLI flag + default-on

**Files:** Modify `internal/cli/cli.go`, `main.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/cli_test.go`:
```go
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
```
(Imports `context`, `backend`, `fleet` already present in the cli test package.)

- [ ] **Step 2: Run test to verify it fails**

Run: `mise exec -- go test ./internal/cli/ -run TestSpawnNoEgress -v`
Expected: FAIL — flag unknown / field unchanged.

- [ ] **Step 3: Add the flag**

In `internal/cli/cli.go` `spawnCmd`, add a `noFirewall` var + flag, and flip the field at the start of `RunE` (before the preflight check):
```go
	var agentName, prompt string
	var noFirewall bool
	c := &cobra.Command{
		Use:   "spawn <repo>",
		Short: "Clone a repo and start an agent on it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if noFirewall {
				f.EgressFirewall = false
			}
			if rep := preflight.Check(cmd.Context(), preflight.Real()); !rep.OK() {
```
and register the flag with the others:
```go
	c.Flags().BoolVar(&noFirewall, "no-egress-firewall", false, "disable the default-deny egress firewall (trusted/dev runs)")
```

- [ ] **Step 4: Default-on in main**

In `main.go`, set the default when constructing the Fleet:
```go
	f := &fleet.Fleet{
		Backend:        backend.NewDocker(),
		BaseImage:      "ubuntu:24.04",
		EgressFirewall: true,
	}
```

- [ ] **Step 5: Run tests + build**

Run: `mise exec -- go build ./... && mise exec -- go test ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go main.go
git commit -m "feat(cli): default-on egress firewall + --no-egress-firewall opt-out"
```

---

## Task 8: Docs — backlog + spec status

**Files:** Modify `docs/backlog.md`

- [ ] **Step 1: Update the backlog**

In `docs/backlog.md`: under "## Next plans", mark the "Egress firewall" item **Done**, pointing at this plan + the egress spec, and renumber the rest (Submission flow becomes #1).

- [ ] **Step 2: Verify the suite**

Run: `mise exec -- go build ./... && mise exec -- go test ./...`
Expected: PASS (integration tests SKIP without docker+devcontainer).

- [ ] **Step 3: Commit**

```bash
git add docs/backlog.md
git commit -m "docs: mark egress firewall done in the backlog"
```

---

## Task 9: End-to-end smoke (manual; requires docker + devcontainer + token)

**Files:** none (manual verification — the "Always Works" gate for the security guarantee).

- [ ] **Step 1: Build + spawn with the firewall on**

```bash
mise exec -- go build -o bin/flotilla .
rm -rf ~/.flotilla/work/* 2>/dev/null
fnox exec -- ./bin/flotilla spawn https://github.com/octocat/Hello-World.git \
  --agent claude --prompt "run: curl -s -o /dev/null -w '%{http_code}' https://example.com ; then stop"
NAME=$(./bin/flotilla list --json | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["name"])')
CID=$(docker ps -q --filter "label=flotilla.agent=$NAME")
```

- [ ] **Step 2: Verify the egress guarantee from inside the agent container**

```bash
echo "=== agent is on the internal net only (no bridge) ==="
docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$CID"   # expect: flotilla-net-<name>
echo "=== allowed host reaches (via proxy env) ==="
docker exec -u ubuntu "$CID" sh -c 'curl -s -o /dev/null -w "anthropic:%{http_code}\n" https://api.anthropic.com/v1/messages -X POST || true'
echo "=== denied host blocked ==="
docker exec -u ubuntu "$CID" sh -c 'curl -s -o /dev/null -w "example.com:%{http_code}\n" https://example.com'   # expect 403 (squid) 
echo "=== no direct route (bypass proxy) ==="
docker exec -u ubuntu "$CID" sh -c 'curl -s -m 5 --noproxy "*" -o /dev/null -w "direct:%{http_code}\n" https://example.com || echo "direct: blocked (good)"'
echo "=== proxy sidecar present ==="
docker ps --filter "label=flotilla.proxy=$NAME" --format '{{.Names}} {{.Image}}'
```
Expected: agent on `flotilla-net-<name>` only; `api.anthropic.com` returns a real HTTP code (reached the API, even if 4xx from no body); `example.com` is `403` (squid deny); the `--noproxy` direct attempt is blocked (no route); the squid sidecar is listed.

- [ ] **Step 3: Verify teardown on rm**

```bash
./bin/flotilla stop "$NAME"; ./bin/flotilla rm "$NAME"
echo "proxy gone? $(docker ps -a --filter "label=flotilla.proxy=$NAME" --format '{{.Names}}' | head -1 || echo yes)"
echo "network gone? $(docker network ls --filter "name=flotilla-net-$NAME" --format '{{.Name}}' | head -1 || echo yes)"
rm -rf ~/.flotilla/work/* bin 2>/dev/null
```
Expected: after `rm`, both the proxy container and the network are gone.

- [ ] **Step 4: (No commit)** — record results in the spike note or PR description.

---

## Self-Review (completed during authoring)

- **Spec coverage:** §3.1 topology → Task 4 (network primitives) + Task 5 (swap). §3.2 allowlist → Task 2. §3.3 squid.conf/ubuntu-squid pinned → Task 3 + Task 5 (`ProxyImage`). §3.4 flow/ordering → Task 6 (firewall between install and launch; proxy env in env-file). §3.5 backend additions → Task 4. §3.6 lifecycle/cleanup → Task 5 (teardown) + Task 6 (Stop/Remove). §4 fail-closed + `EgressFirewall` toggle → Task 6 + Task 7. §5 security (no NET_ADMIN; agent on internal net; refuse unsupported topology) → Task 5. §6 testing (unit + integration + spike) → Tasks 2/3/5/6 (unit), Task 4/9 (integration/smoke), Task 1 (spike). §7 out-of-scope (compose) → Task 5 refuses non-bridge topology (fail-closed, no silent bypass).
- **Placeholder scan:** the only non-literal is `ProxyImage`'s digest, sourced from Task 1's spike (an explicit value-from-prior-task, not a vague TODO). Every other step has complete code/commands.
- **Type consistency:** `CreateOpts.Network`, the five `Backend` network methods, `egress.{BakedAllowlist,Compose,SquidConf}`, `proxyName`/`netName`/`proxyEnv`/`proxyPort`, `setupFirewall`/`teardownFirewall`, and `Fleet.{EgressFirewall,EgressAllow}` are defined once and used with identical signatures across Tasks 2–7.
- **Build-green ordering:** the interface widens and both implementers gain the methods in one commit (Task 4); the firewall package (Task 5) and wiring (Task 6) come after, so `go build ./...` stays green after every task.
