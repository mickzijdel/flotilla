# Flotilla Remote Backend (Federated SSH Client) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one laptop drive a fleet of flotilla agents spread across multiple remote machines, where each host runs the full unchanged engine and the laptop is a stateless SSH multiplexer that fans commands out and merges JSON.

**Architecture:** A new `internal/remote` package (host registry + a `Transport` abstraction + fan-out/merge) plus `--host`/`--all-hosts` routing in `internal/cli`. `LocalTransport` calls the in-process `Fleet`; `SSHTransport` shells `ssh <target> flotilla <cmd> --host local --json`. Aggregate commands merge per-host JSON into a `{rows, hosts}` envelope, tagging each row with its host. **No new `Backend`** and `Fleet`/`gitops`/`daemon` are untouched — this is a client layer *above* the CLI.

**Tech Stack:** Go 1.26.4, cobra v1.10.2, BurntSushi/toml v1.6.0, the system `ssh` binary.

## Global Constraints

- Module path: `github.com/mickzijdel/flotilla` (all imports).
- Go 1.26.4; cobra v1.10.2; BurntSushi/toml v1.6.0 (already in go.mod — no new deps).
- The remote flotilla engine is **never modified to be remote-aware**. The client always invokes a remote as `flotilla <cmd> --host local --json` so the remote runs its plain local path and never re-fans.
- `Fleet`, `Backend`, `gitops`, `daemon` packages must not change behavior. New code lives in `internal/remote`, `internal/version`, and additive wiring in `internal/cli` + `main.go`.
- Tests are Docker-free and SSH-free via injected fakes (a fake `Runner`/`Transport`); the one live path self-skips when ssh-to-localhost is unavailable (mirror the existing Docker integration self-skip).
- Test style: build the CLI with `BuildRoot(...)`, `root.SetOut(&buf)`, `root.SetArgs([]string{...})`, `root.Execute()` (see `internal/cli/inbox_test.go`). Registry/HOME-touching tests use `t.Setenv("HOME", t.TempDir())`.
- Run the full suite with `go test ./...`; never tail/head output.
- Default aggregate scope (no `--host`, no `--all-hosts`) = **all registered hosts** (`local` + remotes). A failing host is a warning row, never fatal; non-zero exit only when *every* targeted host fails.
- `flotilla version --json` reports `{"version": "<semver>", "contract": <int>}`. Same `contract` → proceed (warn if version string differs); different `contract` → block that host with an actionable error.

---

## File Structure

**New:**
- `internal/version/version.go` — `Version`, `Contract` consts + `Info` struct + `Get()`.
- `internal/cli/version.go` + `internal/cli/version_test.go` — `version` command.
- `internal/remote/registry.go` + `registry_test.go` — `~/.flotilla/hosts.toml` load/save/add/rm/get; implicit `local`.
- `internal/remote/ssh.go` + `ssh_test.go` — POSIX shell-escaping + `ssh` argv construction.
- `internal/remote/transport.go` + `transport_test.go` — `Transport` interface, `SSHTransport`, `LocalTransport`, version handshake + compatibility.
- `internal/remote/fan.go` + `fan_test.go` — `Fan` fan-out/merge, `Aggregate`/`HostStatus`, `{rows, hosts}` envelope.
- `internal/cli/host.go` + `host_test.go` — `flotilla host add|ls|rm|doctor` group.
- `internal/cli/remote.go` + `remote_test.go` — `--host`/`--all-hosts` resolution + aggregate/single routing helpers.
- `internal/cli/remote_live_test.go` — self-skipping ssh-to-localhost round trip.

**Modified:**
- `internal/cli/cli.go` — `BuildRoot` gains the registry, persistent flags, and registers `version`/`host`; `list`/`inbox`/`agents` route through the aggregate helper.
- `main.go` — load the registry, pass it into `BuildRoot`.
- `README.md`, `docs/backlog.md`, `docs/specs/2026-06-14-flotilla-design.md`, `docs/specs/2026-06-24-flotilla-remote-backend-design.md` — doc corrections.

---

## Task 1: `version` package + command

**Files:**
- Create: `internal/version/version.go`
- Create: `internal/cli/version.go`
- Test: `internal/cli/version_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `version.Info{Version string, Contract int}`, `version.Get() Info`, `version.Version` (string const), `version.Contract` (int const); `versionCmd() *cobra.Command`.

- [ ] **Step 1: Write the failing test**

`internal/cli/version_test.go`:
```go
package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/mickzijdel/flotilla/internal/version"
)

func TestVersionJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got version.Info
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	if got.Version != version.Version || got.Contract != version.Contract {
		t.Fatalf("got %+v, want version=%q contract=%d", got, version.Version, version.Contract)
	}
}

func TestVersionPlain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), version.Version) {
		t.Fatalf("plain output %q missing version %q", out.String(), version.Version)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestVersion -v`
Expected: FAIL — `internal/version` does not exist / `version` command not registered.

- [ ] **Step 3: Write minimal implementation**

`internal/version/version.go`:
```go
// Package version reports the flotilla build version and the client⇄engine
// JSON-contract major, used to gate remote (cross-host) compatibility.
package version

// Version is the human build version. Contract is bumped ONLY when the JSON a
// remote engine emits to the client changes incompatibly.
const (
	Version  = "0.4.0-dev"
	Contract = 1
)

// Info is the payload of `flotilla version --json`.
type Info struct {
	Version  string `json:"version"`
	Contract int    `json:"contract"`
}

// Get returns the current build's Info.
func Get() Info { return Info{Version: Version, Contract: Contract} }
```

`internal/cli/version.go`:
```go
package cli

import (
	"encoding/json"
	"fmt"

	"github.com/mickzijdel/flotilla/internal/version"
	"github.com/spf13/cobra"
)

func versionCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print the flotilla version (and JSON contract)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(version.Get())
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "flotilla %s (contract %d)\n", version.Version, version.Contract)
			return err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}
```

Then register it in `internal/cli/cli.go` `BuildRoot` by adding `versionCmd()` to the `root.AddCommand(...)` call.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestVersion -v`
Expected: PASS (both subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/version/version.go internal/cli/version.go internal/cli/version_test.go internal/cli/cli.go
git commit -m "feat(version): flotilla version --json reporting version + contract"
```

---

## Task 2: Host registry (`hosts.toml`)

**Files:**
- Create: `internal/remote/registry.go`
- Test: `internal/remote/registry_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Host struct { Name string; SSH string }` (SSH empty ⇒ the implicit local host).
  - `type Registry struct { ... }`
  - `func Load(path string) (*Registry, error)` — missing file ⇒ empty registry (no error).
  - `func DefaultPath() string` — `~/.flotilla/hosts.toml`.
  - `func (r *Registry) Add(name, ssh string, force bool) error`
  - `func (r *Registry) Remove(name string) error`
  - `func (r *Registry) Get(name string) (Host, error)`
  - `func (r *Registry) Hosts() []Host` — `local` first, then remotes in sorted name order.
  - `func (r *Registry) Save(path string) error`
  - `const LocalHost = "local"`

- [ ] **Step 1: Write the failing test**

`internal/remote/registry_test.go`:
```go
package remote

import (
	"path/filepath"
	"testing"
)

func TestRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	r, err := Load(path)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if err := r.Add("beefy", "user@beefy.example.com", false); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	r2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	h, err := r2.Get("beefy")
	if err != nil || h.SSH != "user@beefy.example.com" {
		t.Fatalf("get beefy = %+v, %v", h, err)
	}
}

func TestRegistryLocalAlwaysPresent(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "none.toml"))
	hosts := r.Hosts()
	if len(hosts) != 1 || hosts[0].Name != LocalHost || hosts[0].SSH != "" {
		t.Fatalf("want only implicit local, got %+v", hosts)
	}
	if _, err := r.Get(LocalHost); err != nil {
		t.Fatalf("get local: %v", err)
	}
}

func TestRegistryAddRefusesClobber(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "none.toml"))
	_ = r.Add("beefy", "a", false)
	if err := r.Add("beefy", "b", false); err == nil {
		t.Fatal("expected clobber error without force")
	}
	if err := r.Add("beefy", "b", true); err != nil {
		t.Fatalf("force re-add: %v", err)
	}
	h, _ := r.Get("beefy")
	if h.SSH != "b" {
		t.Fatalf("force did not overwrite: %+v", h)
	}
}

func TestRegistryAddLocalRejected(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err := r.Add(LocalHost, "x", true); err == nil {
		t.Fatal("expected error adding reserved name 'local'")
	}
}

func TestRegistryRemove(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "none.toml"))
	_ = r.Add("beefy", "a", false)
	if err := r.Remove("beefy"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := r.Get("beefy"); err == nil {
		t.Fatal("expected get to fail after remove")
	}
	if err := r.Remove("nope"); err == nil {
		t.Fatal("expected remove of unknown host to error")
	}
}

func TestRegistryHostsOrder(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "none.toml"))
	_ = r.Add("zeta", "z", false)
	_ = r.Add("alpha", "a", false)
	hosts := r.Hosts()
	want := []string{"local", "alpha", "zeta"}
	for i, h := range hosts {
		if h.Name != want[i] {
			t.Fatalf("order[%d]=%q, want %q (full %+v)", i, h.Name, want[i], hosts)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/remote/ -run TestRegistry -v`
Expected: FAIL — package `remote` does not exist.

- [ ] **Step 3: Write minimal implementation**

`internal/remote/registry.go`:
```go
// Package remote is the client-side multiplexer that drives one or many
// per-host flotilla engines over SSH. It owns the host registry, the Transport
// abstraction, and the fan-out/merge of per-host JSON. It does NOT touch Fleet,
// Backend, or the engine — a remote engine is the unchanged binary invoked over
// ssh as `flotilla <cmd> --host local --json`.
package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// LocalHost is the reserved name for the in-process engine on this machine.
const LocalHost = "local"

// Host is a target the client can dispatch to. SSH == "" means the implicit
// local host (LocalTransport); otherwise it is an ssh destination or
// ~/.ssh/config alias.
type Host struct {
	Name string
	SSH  string
}

// Registry is the set of registered remote hosts (the implicit local host is
// never stored). It mirrors ~/.flotilla/hosts.toml.
type Registry struct {
	remotes map[string]string // name -> ssh target
}

// tomlFile is the on-disk shape: [hosts.<name>] ssh = "...".
type tomlFile struct {
	Hosts map[string]struct {
		SSH string `toml:"ssh"`
	} `toml:"hosts"`
}

// DefaultPath returns ~/.flotilla/hosts.toml.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".flotilla", "hosts.toml")
}

// Load reads the registry; a missing file yields an empty registry (no error).
func Load(path string) (*Registry, error) {
	r := &Registry{remotes: map[string]string{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f tomlFile
	if err := toml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, h := range f.Hosts {
		r.remotes[name] = h.SSH
	}
	return r, nil
}

// Add registers a remote. It refuses to overwrite an existing name unless
// force is set, and refuses the reserved name "local".
func (r *Registry) Add(name, ssh string, force bool) error {
	if name == LocalHost {
		return fmt.Errorf("%q is reserved for the local engine", LocalHost)
	}
	if name == "" || ssh == "" {
		return fmt.Errorf("host name and ssh target are required")
	}
	if _, ok := r.remotes[name]; ok && !force {
		return fmt.Errorf("host %q already exists (use --force to overwrite)", name)
	}
	r.remotes[name] = ssh
	return nil
}

// Remove deregisters a remote.
func (r *Registry) Remove(name string) error {
	if _, ok := r.remotes[name]; !ok {
		return fmt.Errorf("no such host %q", name)
	}
	delete(r.remotes, name)
	return nil
}

// Get resolves a host by name; "local" always resolves.
func (r *Registry) Get(name string) (Host, error) {
	if name == LocalHost {
		return Host{Name: LocalHost}, nil
	}
	ssh, ok := r.remotes[name]
	if !ok {
		return Host{}, fmt.Errorf("no such host %q (see 'flotilla host ls')", name)
	}
	return Host{Name: name, SSH: ssh}, nil
}

// Hosts returns the implicit local host first, then remotes sorted by name.
func (r *Registry) Hosts() []Host {
	names := make([]string, 0, len(r.remotes))
	for n := range r.remotes {
		names = append(names, n)
	}
	sort.Strings(names)
	hosts := []Host{{Name: LocalHost}}
	for _, n := range names {
		hosts = append(hosts, Host{Name: n, SSH: r.remotes[n]})
	}
	return hosts
}

// Save writes the registry to path (creating the parent dir).
func (r *Registry) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var f tomlFile
	f.Hosts = map[string]struct {
		SSH string `toml:"ssh"`
	}{}
	for name, ssh := range r.remotes {
		f.Hosts[name] = struct {
			SSH string `toml:"ssh"`
		}{SSH: ssh}
	}
	b, err := toml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/remote/ -run TestRegistry -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/remote/registry.go internal/remote/registry_test.go
git commit -m "feat(remote): host registry (~/.flotilla/hosts.toml) with implicit local"
```

---

## Task 3: Shell-escaping + SSH argv construction

**Files:**
- Create: `internal/remote/ssh.go`
- Test: `internal/remote/ssh_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func ShellEscape(arg string) string` — POSIX single-quote escape of one arg.
  - `func RemoteCommand(fargs []string) string` — the remote shell command string: `flotilla` + escaped fargs (caller passes the already-final fargs, e.g. `["list","--host","local","--json"]`).
  - `func SSHArgv(target string, fargs []string) []string` — full argv: `["ssh","-o","BatchMode=yes","-o","ControlMaster=auto","-o","ControlPersist=60s", target, "--", RemoteCommand(fargs)]`.

- [ ] **Step 1: Write the failing test**

`internal/remote/ssh_test.go`:
```go
package remote

import (
	"strings"
	"testing"
)

func TestShellEscape(t *testing.T) {
	cases := map[string]string{
		"plain":            "'plain'",
		"with space":       "'with space'",
		`has"quote`:        `'has"quote'`,
		"has'apostrophe":   `'has'\''apostrophe'`,
		"dollar$and`tick`": "'dollar$and`tick`'",
		"semi;colon":       "'semi;colon'",
	}
	for in, want := range cases {
		if got := ShellEscape(in); got != want {
			t.Errorf("ShellEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoteCommand(t *testing.T) {
	got := RemoteCommand([]string{"answer", "brave-otter", "drop it; rm -rf /"})
	want := `flotilla 'answer' 'brave-otter' 'drop it; rm -rf /'`
	if got != want {
		t.Fatalf("RemoteCommand = %q, want %q", got, want)
	}
}

func TestSSHArgv(t *testing.T) {
	argv := SSHArgv("user@beefy", []string{"list", "--host", "local", "--json"})
	if argv[0] != "ssh" {
		t.Fatalf("argv[0] = %q, want ssh", argv[0])
	}
	// target appears, followed by `--` and the single remote command string.
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "user@beefy") {
		t.Fatalf("argv missing target: %v", argv)
	}
	last := argv[len(argv)-1]
	if last != `flotilla 'list' '--host' 'local' '--json'` {
		t.Fatalf("remote command = %q", last)
	}
	if argv[len(argv)-2] != "--" {
		t.Fatalf("expected -- before remote command, got %v", argv)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/remote/ -run 'TestShellEscape|TestRemoteCommand|TestSSHArgv' -v`
Expected: FAIL — `ShellEscape`/`RemoteCommand`/`SSHArgv` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/remote/ssh.go`:
```go
package remote

import "strings"

// ShellEscape wraps an argument in single quotes for a POSIX remote shell,
// escaping embedded single quotes via the '\'' idiom. Everything else
// (spaces, $, backticks, ;, ") is literal inside single quotes.
func ShellEscape(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// RemoteCommand builds the command string run by the remote login shell:
// the literal `flotilla` followed by each escaped argument.
func RemoteCommand(fargs []string) string {
	parts := make([]string, 0, len(fargs)+1)
	parts = append(parts, "flotilla")
	for _, a := range fargs {
		parts = append(parts, ShellEscape(a))
	}
	return strings.Join(parts, " ")
}

// SSHArgv builds the full `ssh` argument vector for a remote flotilla call.
// BatchMode avoids interactive password prompts hanging a fan-out; the
// ControlMaster options amortize the handshake across a multi-host fan-out
// (the user's own ~/.ssh/config can override these).
func SSHArgv(target string, fargs []string) []string {
	return []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=60s",
		target,
		"--",
		RemoteCommand(fargs),
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/remote/ -run 'TestShellEscape|TestRemoteCommand|TestSSHArgv' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remote/ssh.go internal/remote/ssh_test.go
git commit -m "feat(remote): shell-escaping + ssh argv construction"
```

---

## Task 4: Transport interface, runners, version handshake

**Files:**
- Create: `internal/remote/transport.go`
- Test: `internal/remote/transport_test.go`

**Interfaces:**
- Consumes: `SSHArgv` (Task 3), `version.Info` (Task 1), `Host` (Task 2).
- Produces:
  - `type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)` — defaults to exec; injectable in tests. `ExecRunner` is the production value.
  - `type Transport interface { Run(ctx context.Context, fargs []string) ([]byte, error); Name() string }`
  - `type SSHTransport struct { Host Host; Runner Runner }`
  - `type LocalTransport struct { HostName string; Local func(ctx context.Context, fargs []string) ([]byte, error) }`
  - `func Handshake(ctx context.Context, t Transport) (version.Info, error)` — runs `version --json` through the transport.
  - `func Compatible(client, remote version.Info) (ok bool, warn bool)` — ok=false when contracts differ; warn=true when contracts match but version strings differ.

- [ ] **Step 1: Write the failing test**

`internal/remote/transport_test.go`:
```go
package remote

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/version"
)

func TestSSHTransportRunUsesRunner(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("[]\n"), nil
	}
	tr := &SSHTransport{Host: Host{Name: "beefy", SSH: "user@beefy"}, Runner: runner}
	out, err := tr.Run(context.Background(), []string{"list", "--host", "local", "--json"})
	if err != nil || string(out) != "[]\n" {
		t.Fatalf("run = %q, %v", out, err)
	}
	if gotName != "ssh" {
		t.Fatalf("runner invoked %q, want ssh", gotName)
	}
	if last := gotArgs[len(gotArgs)-1]; !strings.Contains(last, "flotilla 'list'") {
		t.Fatalf("remote command = %q", last)
	}
}

func TestLocalTransportRunCallsLocal(t *testing.T) {
	tr := &LocalTransport{HostName: "local", Local: func(_ context.Context, fargs []string) ([]byte, error) {
		if fargs[0] != "list" {
			return nil, errors.New("unexpected")
		}
		return []byte(`[{"name":"x"}]`), nil
	}}
	out, err := tr.Run(context.Background(), []string{"list"})
	if err != nil || string(out) != `[{"name":"x"}]` {
		t.Fatalf("run = %q, %v", out, err)
	}
}

func TestHandshakeParsesVersion(t *testing.T) {
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"version":"0.9.0","contract":1}`), nil
	}
	tr := &SSHTransport{Host: Host{Name: "beefy", SSH: "x"}, Runner: runner}
	info, err := Handshake(context.Background(), tr)
	if err != nil || info.Version != "0.9.0" || info.Contract != 1 {
		t.Fatalf("handshake = %+v, %v", info, err)
	}
}

func TestCompatible(t *testing.T) {
	client := version.Info{Version: "0.4.0", Contract: 1}
	if ok, warn := Compatible(client, version.Info{Version: "0.4.0", Contract: 1}); !ok || warn {
		t.Fatalf("same: ok=%v warn=%v", ok, warn)
	}
	if ok, warn := Compatible(client, version.Info{Version: "0.9.0", Contract: 1}); !ok || !warn {
		t.Fatalf("minor drift: ok=%v warn=%v", ok, warn)
	}
	if ok, _ := Compatible(client, version.Info{Version: "1.0.0", Contract: 2}); ok {
		t.Fatal("contract break should be incompatible")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/remote/ -run 'Transport|Handshake|Compatible' -v`
Expected: FAIL — types/functions undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/remote/transport.go`:
```go
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/mickzijdel/flotilla/internal/version"
)

// Runner executes a command and returns its stdout. Injectable for tests.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs the command for real, returning stdout (stderr is folded into
// the error so a failing remote surfaces a useful message).
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("%s: %w: %s", name, err, string(ee.Stderr))
		}
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Transport runs a flotilla subcommand against one host and returns its stdout.
type Transport interface {
	Run(ctx context.Context, fargs []string) ([]byte, error)
	Name() string
}

// SSHTransport invokes the remote flotilla over ssh.
type SSHTransport struct {
	Host   Host
	Runner Runner
}

func (t *SSHTransport) Name() string { return t.Host.Name }

func (t *SSHTransport) Run(ctx context.Context, fargs []string) ([]byte, error) {
	argv := SSHArgv(t.Host.SSH, fargs)
	return t.Runner(ctx, argv[0], argv[1:]...)
}

// LocalTransport runs the command in-process against the local Fleet via an
// injected callback (wired by the cli layer).
type LocalTransport struct {
	HostName string
	Local    func(ctx context.Context, fargs []string) ([]byte, error)
}

func (t *LocalTransport) Name() string { return t.HostName }

func (t *LocalTransport) Run(ctx context.Context, fargs []string) ([]byte, error) {
	return t.Local(ctx, fargs)
}

// Handshake runs `version --json` through a transport and parses the result.
func Handshake(ctx context.Context, t Transport) (version.Info, error) {
	out, err := t.Run(ctx, []string{"version", "--json"})
	if err != nil {
		return version.Info{}, err
	}
	var info version.Info
	if err := json.Unmarshal(out, &info); err != nil {
		return version.Info{}, fmt.Errorf("parse version from %q: %w", string(out), err)
	}
	return info, nil
}

// Compatible reports whether a remote engine is usable by this client.
// ok=false when the JSON contract differs; warn=true when contracts match but
// the human version strings differ.
func Compatible(client, remote version.Info) (ok bool, warn bool) {
	if client.Contract != remote.Contract {
		return false, false
	}
	return true, client.Version != remote.Version
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/remote/ -run 'Transport|Handshake|Compatible' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remote/transport.go internal/remote/transport_test.go
git commit -m "feat(remote): Transport interface, ssh/local runners, version handshake"
```

---

## Task 5: Fan-out aggregation + merge + `{rows, hosts}` envelope

**Files:**
- Create: `internal/remote/fan.go`
- Test: `internal/remote/fan_test.go`

**Interfaces:**
- Consumes: `Transport` (Task 4).
- Produces:
  - `type HostStatus struct { Name string; OK bool; Error string omitempty; Version string omitempty; Contract int omitempty }`
  - `type Aggregate struct { Rows []map[string]any; Hosts []HostStatus }`
  - `func (a Aggregate) AllFailed() bool` — true iff ≥1 host and every host failed.
  - `func Fan(ctx context.Context, transports []Transport, fargs []string) Aggregate` — runs each transport concurrently, parses its stdout as a JSON array of objects, injects `"host"` into each row, preserves transport order, records per-host OK/Error. A host whose output is not a JSON array becomes `OK:false` with the parse error.

- [ ] **Step 1: Write the failing test**

`internal/remote/fan_test.go`:
```go
package remote

import (
	"context"
	"errors"
	"testing"
)

type fakeTransport struct {
	name string
	out  []byte
	err  error
}

func (f fakeTransport) Name() string { return f.name }
func (f fakeTransport) Run(_ context.Context, _ []string) ([]byte, error) {
	return f.out, f.err
}

func TestFanMergesAndTagsHost(t *testing.T) {
	ts := []Transport{
		fakeTransport{name: "local", out: []byte(`[{"name":"a"}]`)},
		fakeTransport{name: "beefy", out: []byte(`[{"name":"b"},{"name":"c"}]`)},
	}
	agg := Fan(context.Background(), ts, []string{"list", "--host", "local", "--json"})
	if len(agg.Rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(agg.Rows), agg.Rows)
	}
	// rows preserve transport order and are tagged with host.
	if agg.Rows[0]["host"] != "local" || agg.Rows[0]["name"] != "a" {
		t.Fatalf("row0 = %+v", agg.Rows[0])
	}
	if agg.Rows[1]["host"] != "beefy" || agg.Rows[2]["host"] != "beefy" {
		t.Fatalf("beefy rows mis-tagged: %+v", agg.Rows)
	}
	for _, h := range agg.Hosts {
		if !h.OK {
			t.Fatalf("host %q not OK: %+v", h.Name, h)
		}
	}
}

func TestFanHostErrorIsNonFatal(t *testing.T) {
	ts := []Transport{
		fakeTransport{name: "local", out: []byte(`[{"name":"a"}]`)},
		fakeTransport{name: "beefy", err: errors.New("ssh exit 255: connection refused")},
	}
	agg := Fan(context.Background(), ts, nil)
	if len(agg.Rows) != 1 || agg.Rows[0]["host"] != "local" {
		t.Fatalf("want only local row, got %+v", agg.Rows)
	}
	var beefy HostStatus
	for _, h := range agg.Hosts {
		if h.Name == "beefy" {
			beefy = h
		}
	}
	if beefy.OK || beefy.Error == "" {
		t.Fatalf("beefy should be failed with error, got %+v", beefy)
	}
	if agg.AllFailed() {
		t.Fatal("not all failed (local succeeded)")
	}
}

func TestFanAllFailed(t *testing.T) {
	ts := []Transport{fakeTransport{name: "beefy", err: errors.New("boom")}}
	if !Fan(context.Background(), ts, nil).AllFailed() {
		t.Fatal("expected AllFailed when the only host errors")
	}
}

func TestFanRejectsNonArray(t *testing.T) {
	ts := []Transport{fakeTransport{name: "beefy", out: []byte(`{"oops":true}`)}}
	agg := Fan(context.Background(), ts, nil)
	if agg.Hosts[0].OK {
		t.Fatalf("non-array output should fail the host: %+v", agg.Hosts[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/remote/ -run TestFan -v`
Expected: FAIL — `Fan`/`Aggregate`/`HostStatus` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/remote/fan.go`:
```go
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// HostStatus reports a single host's outcome in an aggregate. Version/Contract
// are populated only by the handshake path (host ls / host doctor); ordinary
// aggregate commands populate Name/OK/Error.
type HostStatus struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Version  string `json:"version,omitempty"`
	Contract int    `json:"contract,omitempty"`
}

// Aggregate is the merged result of fanning a command across hosts.
type Aggregate struct {
	Rows  []map[string]any `json:"rows"`
	Hosts []HostStatus     `json:"hosts"`
}

// AllFailed reports whether every targeted host failed (caller maps this to a
// non-zero exit). False for an empty target set.
func (a Aggregate) AllFailed() bool {
	if len(a.Hosts) == 0 {
		return false
	}
	for _, h := range a.Hosts {
		if h.OK {
			return false
		}
	}
	return true
}

// Fan runs fargs against every transport concurrently and merges the per-host
// JSON arrays into one row list (each row tagged with its host), preserving the
// transports' order. A transport error or non-array output marks that host
// failed without aborting the others.
func Fan(ctx context.Context, transports []Transport, fargs []string) Aggregate {
	type result struct {
		rows []map[string]any
		st   HostStatus
	}
	results := make([]result, len(transports))
	var wg sync.WaitGroup
	for i, tr := range transports {
		wg.Add(1)
		go func(i int, tr Transport) {
			defer wg.Done()
			out, err := tr.Run(ctx, fargs)
			st := HostStatus{Name: tr.Name(), OK: true}
			if err != nil {
				st.OK, st.Error = false, err.Error()
				results[i] = result{st: st}
				return
			}
			var rows []map[string]any
			if err := json.Unmarshal(out, &rows); err != nil {
				st.OK = false
				st.Error = fmt.Sprintf("unexpected output (run 'flotilla host doctor %s'): %v", tr.Name(), err)
				results[i] = result{st: st}
				return
			}
			for _, row := range rows {
				row["host"] = tr.Name()
			}
			results[i] = result{rows: rows, st: st}
		}(i, tr)
	}
	wg.Wait()

	agg := Aggregate{Rows: []map[string]any{}}
	for _, r := range results {
		agg.Rows = append(agg.Rows, r.rows...)
		agg.Hosts = append(agg.Hosts, r.st)
	}
	return agg
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/remote/ -run TestFan -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remote/fan.go internal/remote/fan_test.go
git commit -m "feat(remote): fan-out aggregation, host tagging, {rows,hosts} envelope"
```

---

## Task 6: `flotilla host` command group (add/ls/rm/doctor)

**Files:**
- Create: `internal/cli/host.go`
- Test: `internal/cli/host_test.go`
- Modify: `internal/cli/cli.go` (register `hostCmd`)

**Interfaces:**
- Consumes: `remote.Registry` (Task 2), `remote.Handshake`/`Compatible`/`SSHTransport`/`ExecRunner` (Task 4), `version.Get()` (Task 1).
- Produces: `func hostCmd(reg *remote.Registry, path string, runner remote.Runner) *cobra.Command`. (The `runner` and `path` are injected so tests can fake ssh and use a temp file; `main.go` passes `remote.ExecRunner` and `remote.DefaultPath()`.)

- [ ] **Step 1: Write the failing test**

`internal/cli/host_test.go`:
```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/remote"
)

func TestHostAddLsRm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	reg, _ := remote.Load(path)
	// fake runner: pretend every ssh `version --json` succeeds with contract 1.
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"version":"0.4.0-dev","contract":1}`), nil
	}
	run := func(args ...string) string {
		c := hostCmd(reg, path, runner)
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatalf("host %v: %v", args, err)
		}
		return out.String()
	}
	run("add", "beefy", "user@beefy")
	ls := run("ls")
	if !strings.Contains(ls, "beefy") || !strings.Contains(ls, "local") {
		t.Fatalf("ls missing hosts: %q", ls)
	}
	run("rm", "beefy")
	reg2, _ := remote.Load(path)
	if _, err := reg2.Get("beefy"); err == nil {
		t.Fatal("beefy should be gone after rm (and persisted)")
	}
}

func TestHostDoctorJSONContractBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	reg, _ := remote.Load(path)
	_ = reg.Add("oldbox", "user@oldbox", false)
	_ = reg.Save(path)
	// remote reports an incompatible contract.
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"version":"9.9.9","contract":99}`), nil
	}
	c := hostCmd(reg, path, runner)
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"doctor", "--json"})
	if err := c.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var statuses []remote.HostStatus
	if err := json.Unmarshal(out.Bytes(), &statuses); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	var old remote.HostStatus
	for _, s := range statuses {
		if s.Name == "oldbox" {
			old = s
		}
	}
	if old.OK {
		t.Fatalf("oldbox with contract 99 should be blocked: %+v", old)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestHost -v`
Expected: FAIL — `hostCmd` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/cli/host.go`:
```go
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mickzijdel/flotilla/internal/remote"
	"github.com/mickzijdel/flotilla/internal/version"
	"github.com/spf13/cobra"
)

func hostCmd(reg *remote.Registry, path string, runner remote.Runner) *cobra.Command {
	c := &cobra.Command{Use: "host", Short: "Manage remote flotilla hosts"}
	c.AddCommand(hostAddCmd(reg, path), hostRmCmd(reg, path), hostLsCmd(reg, runner, false), hostLsCmd(reg, runner, true))
	return c
}

func hostAddCmd(reg *remote.Registry, path string) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "add <name> <ssh-target>",
		Short: "Register a remote host",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := reg.Add(args[0], args[1], force); err != nil {
				return err
			}
			return reg.Save(path)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing host")
	return c
}

func hostRmCmd(reg *remote.Registry, path string) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Deregister a remote host (never touches the remote)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := reg.Remove(args[0]); err != nil {
				return err
			}
			return reg.Save(path)
		},
	}
}

// hostLsCmd backs both `host ls` and `host doctor`; they share the per-host
// handshake and differ only in default verbosity. `doctor` is registered as an
// alias via a second construction with use="doctor".
func hostLsCmd(reg *remote.Registry, runner remote.Runner, doctor bool) *cobra.Command {
	use, short := "ls", "List hosts with reachability/version"
	if doctor {
		use, short = "doctor [name]", "Preflight one or all hosts (ssh + version contract)"
	}
	var asJSON bool
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts := reg.Hosts()
			if len(args) == 1 {
				h, err := reg.Get(args[0])
				if err != nil {
					return err
				}
				hosts = []remote.Host{h}
			}
			statuses := probeHosts(cmd.Context(), hosts, runner)
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(statuses)
			}
			for _, s := range statuses {
				line := fmt.Sprintf("%s\tok\t%s (contract %d)", s.Name, s.Version, s.Contract)
				if !s.OK {
					line = fmt.Sprintf("%s\tFAIL\t%s", s.Name, s.Error)
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

// probeHosts handshakes every host concurrently, applying the compatibility
// rule (contract mismatch -> blocked).
func probeHosts(ctx context.Context, hosts []remote.Host, runner remote.Runner) []remote.HostStatus {
	statuses := make([]remote.HostStatus, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h remote.Host) {
			defer wg.Done()
			st := remote.HostStatus{Name: h.Name}
			var info version.Info
			var err error
			if h.SSH == "" { // local host: no ssh, report our own build.
				info = version.Get()
			} else {
				info, err = remote.Handshake(ctx, &remote.SSHTransport{Host: h, Runner: runner})
			}
			if err != nil {
				st.OK, st.Error = false, err.Error()
				statuses[i] = st
				return
			}
			st.Version, st.Contract = info.Version, info.Contract
			ok, warn := remote.Compatible(version.Get(), info)
			switch {
			case !ok:
				st.OK = false
				st.Error = fmt.Sprintf("contract %d, client expects %d — align flotilla versions", info.Contract, version.Get().Contract)
			case warn:
				st.OK = true
				st.Error = fmt.Sprintf("version skew: remote %s vs client %s (contract ok)", info.Version, version.Version)
			default:
				st.OK = true
			}
			statuses[i] = st
		}(i, h)
	}
	wg.Wait()
	return statuses
}
```

Register in `internal/cli/cli.go` (see Task 9 for the full `BuildRoot` change; for this task, add `hostCmd(reg, path, runner)` to `AddCommand` using the registry/path/runner threaded into `BuildRoot`).

> **Note:** cobra rejects two subcommands sharing a parent if names collide; `ls` and `doctor` have distinct `Use` names so both register cleanly. They share `hostLsCmd`'s body via the `doctor bool` switch.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestHost -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/host.go internal/cli/host_test.go internal/cli/cli.go
git commit -m "feat(cli): flotilla host add/ls/rm/doctor with version handshake"
```

---

## Task 7: `--host`/`--all-hosts` resolution + aggregate routing (list, inbox, agents)

**Files:**
- Create: `internal/cli/remote.go`
- Test: `internal/cli/remote_test.go`
- Modify: `internal/cli/cli.go` (`listCmd`, `inboxCmd`, `agentsCmd` route through the helper)

**Interfaces:**
- Consumes: `remote.Registry`/`Host` (Task 2), `remote.Transport`/`SSHTransport`/`LocalTransport`/`Runner` (Task 4), `remote.Fan`/`Aggregate` (Task 5).
- Produces:
  - `func resolveTargets(reg *remote.Registry, hostFlag string, allHosts bool, local remote.LocalRunner, runner remote.Runner) ([]remote.Transport, error)` where `type LocalRunner = func(ctx context.Context, fargs []string) ([]byte, error)`. Rules: `--host X` → just X (error if unknown); otherwise (default and `--all-hosts` are the same default-all) → every registry host. The `local` host gets a `LocalTransport` wrapping `local`; remotes get `SSHTransport`.
  - `func runAggregate(cmd *cobra.Command, targets []remote.Transport, cmdName string, asJSON bool, passthrough []string, renderRow func(map[string]any) string) error` — single-host {local} fast path is handled by the caller; this fans out, prints `{rows, hosts}` (JSON) or a HOST-tagged table, and returns an error only when `Aggregate.AllFailed()`.

- [ ] **Step 1: Write the failing test**

`internal/cli/remote_test.go`:
```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/remote"
)

// buildRemoteRoot wires BuildRoot with a fake ssh runner so `--host`/aggregate
// paths are exercised without a real ssh or docker.
func buildRemoteRoot(t *testing.T, remoteListJSON string) *cobra.Command {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "hosts.toml")
	reg, _ := remote.Load(path)
	_ = reg.Add("beefy", "user@beefy", false)
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(remoteListJSON), nil
	}
	f := &fleet.Fleet{Backend: backend.NewFake()}
	return BuildRootWith(f, reg, path, runner) // test seam added in Task 9
}

func TestListAggregatesAcrossHosts(t *testing.T) {
	root := buildRemoteRoot(t, `[{"name":"remote-agent","status":"running","repo":"r"}]`)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"list", "--all-hosts", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var agg remote.Aggregate
	if err := json.Unmarshal(out.Bytes(), &agg); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	// local (fake backend, empty) + beefy (one agent) => one tagged row from beefy.
	var sawBeefy bool
	for _, r := range agg.Rows {
		if r["host"] == "beefy" && r["name"] == "remote-agent" {
			sawBeefy = true
		}
	}
	if !sawBeefy {
		t.Fatalf("expected beefy's remote-agent row, got %+v", agg.Rows)
	}
	names := map[string]bool{}
	for _, h := range agg.Hosts {
		names[h.Name] = true
	}
	if !names["local"] || !names["beefy"] {
		t.Fatalf("hosts missing: %+v", agg.Hosts)
	}
}

func TestListLocalOnlyKeepsPlainShape(t *testing.T) {
	root := buildRemoteRoot(t, `[]`)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"list", "--host", "local", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// --host local must NOT wrap in {rows,hosts}; it is the plain engine path.
	s := strings.TrimSpace(out.String())
	if strings.Contains(s, `"rows"`) || strings.Contains(s, `"hosts"`) {
		t.Fatalf("--host local should emit the plain array, got %q", s)
	}
}
```

(Add imports `"github.com/mickzijdel/flotilla/internal/backend"`, `"github.com/mickzijdel/flotilla/internal/fleet"`, and `"github.com/spf13/cobra"` to the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run 'TestList(Aggregates|LocalOnly)' -v`
Expected: FAIL — `BuildRootWith`/`resolveTargets`/`runAggregate` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/cli/remote.go`:
```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/mickzijdel/flotilla/internal/remote"
	"github.com/spf13/cobra"
)

// LocalRunner produces the bare JSON a remote engine would, but in-process.
type LocalRunner = func(ctx context.Context, fargs []string) ([]byte, error)

// localScope reports whether the resolved selection is exactly the local host —
// the fast path that runs the plain engine logic and emits the unwrapped shape.
func localScope(hostFlag string) bool { return hostFlag == remote.LocalHost }

// resolveTargets turns the --host / --all-hosts selection into transports.
// --host X selects just X; otherwise every registry host is targeted.
func resolveTargets(reg *remote.Registry, hostFlag string, local LocalRunner, runner remote.Runner) ([]remote.Transport, error) {
	var hosts []remote.Host
	if hostFlag != "" {
		h, err := reg.Get(hostFlag)
		if err != nil {
			return nil, err
		}
		hosts = []remote.Host{h}
	} else {
		hosts = reg.Hosts()
	}
	ts := make([]remote.Transport, 0, len(hosts))
	for _, h := range hosts {
		if h.SSH == "" {
			ts = append(ts, &remote.LocalTransport{HostName: h.Name, Local: local})
		} else {
			ts = append(ts, &remote.SSHTransport{Host: h, Runner: runner})
		}
	}
	return ts, nil
}

// runAggregate fans the command across targets and renders the merged result.
func runAggregate(cmd *cobra.Command, targets []remote.Transport, passthrough []string, asJSON bool, renderRow func(map[string]any) string) error {
	// every remote leg runs as `<cmd> --host local --json`; the local leg too.
	fargs := append(append([]string{}, passthrough...), "--host", remote.LocalHost, "--json")
	agg := remote.Fan(cmd.Context(), targets, fargs)
	out := cmd.OutOrStdout()
	if asJSON {
		if err := json.NewEncoder(out).Encode(agg); err != nil {
			return err
		}
	} else {
		for _, h := range agg.Hosts {
			if !h.OK {
				fmt.Fprintf(out, "! %s: %s\n", h.Name, h.Error)
			}
		}
		for _, row := range agg.Rows {
			if _, err := fmt.Fprintln(out, renderRow(row)); err != nil {
				return err
			}
		}
	}
	if agg.AllFailed() {
		return fmt.Errorf("all targeted hosts failed")
	}
	return nil
}

// localJSON runs a subcommand of the plain root in-process and captures its
// stdout — the LocalRunner used for the local leg of an aggregate.
func localJSON(root *cobra.Command) LocalRunner {
	return func(ctx context.Context, fargs []string) ([]byte, error) {
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetArgs(fargs)
		err := root.ExecuteContext(ctx)
		return buf.Bytes(), err
	}
}
```

Now modify `listCmd` in `internal/cli/cli.go` to route. The command reads the persistent `--host`/`--all-hosts` flags (added in Task 9) and the injected registry/runner. Pattern (shown for `list`; `inbox` and `agents` mirror it with their own `renderRow` and `localFn`):

```go
func listCmd(f *fleet.Fleet, reg *remote.Registry, runner remote.Runner) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the fleet (all hosts by default)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			hostFlag, _ := cmd.Flags().GetString("host")
			// Fast path: explicit local => plain engine output (unwrapped).
			if localScope(hostFlag) || onlyLocal(reg) {
				agents, err := f.List(cmd.Context())
				if err != nil {
					return err
				}
				if asJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(agents)
				}
				for _, a := range agents {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", a.Name, a.Status, a.Repo)
				}
				return nil
			}
			// Aggregate path: local leg re-enters the plain `list` in-process.
			plain := BuildRoot(f) // plain tree (no registry) = single-host engine
			targets, err := resolveTargets(reg, hostFlagOrEmpty(hostFlag), localJSON(plain), runner)
			if err != nil {
				return err
			}
			render := func(r map[string]any) string {
				return fmt.Sprintf("%v\t%v\t%v\t%v", r["host"], r["name"], r["status"], r["repo"])
			}
			return runAggregate(cmd, targets, []string{"list"}, asJSON, render)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}
```

Add helpers in `remote.go`:
```go
// onlyLocal reports whether no remotes are registered (so bare commands stay
// on the fast in-process path).
func onlyLocal(reg *remote.Registry) bool { return len(reg.Hosts()) == 1 }

// hostFlagOrEmpty maps the "all hosts" sentinel (empty) through unchanged, and
// passes an explicit non-local host so resolveTargets selects just it.
func hostFlagOrEmpty(hostFlag string) string { return hostFlag }
```

> **Why re-enter `BuildRoot(f)` for the local leg:** the plain tree has no registry, so its `list --host local --json` takes the fast path above and emits the bare array `Fan` expects — no duplication of the list-formatting logic, no infinite recursion.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run 'TestList(Aggregates|LocalOnly)' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/remote.go internal/cli/remote_test.go internal/cli/cli.go
git commit -m "feat(cli): --host/--all-hosts aggregate routing for list/inbox/agents"
```

---

## Task 8: Single-agent routing + cross-host resolution + ssh passthrough

**Files:**
- Create: (extend) `internal/cli/remote.go`
- Test: (extend) `internal/cli/remote_test.go`
- Modify: `internal/cli/cli.go` (`attachCmd`, `stopCmd`, `rmCmd`, `submitCmd`, `logsCmd`)

**Interfaces:**
- Consumes: `remote.Registry`/`Transport`/`Fan` (Tasks 2/4/5).
- Produces:
  - `func resolveAgentHost(ctx context.Context, reg *remote.Registry, runner remote.Runner, local LocalRunner, hostFlag, agentRef string) (host string, agent string, err error)` — explicit `--host`/`host:agent` wins; otherwise fan-out `list` to locate. Unique → that host; ambiguous → error listing `host:agent` candidates; missing → error.
  - `func sshPassthrough(ctx context.Context, target string, fargs []string, in io.Reader, out, errw io.Writer, tty bool) error` — exec ssh with inherited stdio (`-t` when tty) for `attach`/`logs -f`. (Live-only; unit test asserts the argv via a seam, not a real ssh.)

- [ ] **Step 1: Write the failing test**

`internal/cli/remote_test.go` (append):
```go
func TestResolveAgentHostUnique(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "hosts.toml")
	reg, _ := remote.Load(path)
	_ = reg.Add("beefy", "user@beefy", false)
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`[{"name":"remote-agent"}]`), nil
	}
	local := func(_ context.Context, _ []string) ([]byte, error) { return []byte(`[]`), nil }
	host, agent, err := resolveAgentHost(context.Background(), reg, runner, local, "", "remote-agent")
	if err != nil || host != "beefy" || agent != "remote-agent" {
		t.Fatalf("resolve = %q/%q, %v", host, agent, err)
	}
}

func TestResolveAgentHostExplicitPrefix(t *testing.T) {
	reg, _ := remote.Load(filepath.Join(t.TempDir(), "none.toml"))
	_ = reg.Add("beefy", "x", false)
	host, agent, err := resolveAgentHost(context.Background(), reg, nil, nil, "", "beefy:thing")
	if err != nil || host != "beefy" || agent != "thing" {
		t.Fatalf("prefix resolve = %q/%q, %v", host, agent, err)
	}
}

func TestResolveAgentHostAmbiguous(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	reg, _ := remote.Load(filepath.Join(t.TempDir(), "none.toml"))
	_ = reg.Add("beefy", "x", false)
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`[{"name":"dup"}]`), nil // beefy has it...
	}
	local := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(`[{"name":"dup"}]`), nil // ...and so does local
	}
	if _, _, err := resolveAgentHost(context.Background(), reg, runner, local, "", "dup"); err == nil {
		t.Fatal("expected ambiguity error")
	}
}

func TestResolveAgentHostMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	reg, _ := remote.Load(filepath.Join(t.TempDir(), "none.toml"))
	local := func(_ context.Context, _ []string) ([]byte, error) { return []byte(`[]`), nil }
	if _, _, err := resolveAgentHost(context.Background(), reg, nil, local, "", "ghost"); err == nil {
		t.Fatal("expected not-found error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestResolveAgentHost -v`
Expected: FAIL — `resolveAgentHost` undefined.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/cli/remote.go`:
```go
import (
	"io"
	"os/exec"
	"strings"
	// (plus the imports already in remote.go)
)

// resolveAgentHost finds which host owns agentRef. An explicit --host or a
// "host:agent" prefix is authoritative; otherwise it fans `list` across hosts.
func resolveAgentHost(ctx context.Context, reg *remote.Registry, runner remote.Runner, local LocalRunner, hostFlag, agentRef string) (string, string, error) {
	if hostFlag != "" {
		if _, err := reg.Get(hostFlag); err != nil {
			return "", "", err
		}
		return hostFlag, agentRef, nil
	}
	if host, agent, ok := strings.Cut(agentRef, ":"); ok {
		if _, err := reg.Get(host); err != nil {
			return "", "", err
		}
		return host, agent, nil
	}
	// Fan `list` across all hosts and see who has it.
	var targets []remote.Transport
	for _, h := range reg.Hosts() {
		if h.SSH == "" {
			targets = append(targets, &remote.LocalTransport{HostName: h.Name, Local: local})
		} else {
			targets = append(targets, &remote.SSHTransport{Host: h, Runner: runner})
		}
	}
	agg := remote.Fan(ctx, targets, []string{"list", "--host", remote.LocalHost, "--json"})
	var matches []string
	for _, row := range agg.Rows {
		if name, _ := row["name"].(string); name == agentRef {
			matches = append(matches, fmt.Sprintf("%v:%s", row["host"], name))
		}
	}
	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("no agent %q on any host", agentRef)
	case 1:
		host, agent, _ := strings.Cut(matches[0], ":")
		return host, agent, nil
	default:
		return "", "", fmt.Errorf("agent %q is ambiguous across hosts: %s (use host:agent)", agentRef, strings.Join(matches, ", "))
	}
}

// sshPassthrough execs ssh with inherited stdio for interactive/streaming
// commands (attach, logs -f). tty adds -t.
func sshPassthrough(ctx context.Context, target string, fargs []string, in io.Reader, out, errw io.Writer, tty bool) error {
	argv := SSHArgvTTY(target, fargs, tty)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errw
	return cmd.Run()
}
```

Add `SSHArgvTTY` to `internal/remote/ssh.go` (and a small test in `ssh_test.go` asserting `-t` is present when tty):
```go
// SSHArgvTTY is SSHArgv with an optional -t (force pseudo-tty) for interactive
// passthrough (attach) and streaming (logs -f).
func SSHArgvTTY(target string, fargs []string, tty bool) []string {
	base := []string{"ssh", "-o", "BatchMode=yes", "-o", "ControlMaster=auto", "-o", "ControlPersist=60s"}
	if tty {
		base = append(base, "-t")
	}
	base = append(base, target, "--", RemoteCommand(fargs))
	return base
}
```
(Refactor `SSHArgv` to `return SSHArgvTTY(target, fargs, false)` to keep one implementation.)

Then route the single-agent commands in `cli.go`. Pattern for `stopCmd` (non-streaming; `attach`/`logs` use `sshPassthrough` with `tty`/`-f`):
```go
func stopCmd(f *fleet.Fleet, reg *remote.Registry, runner remote.Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <agent>",
		Short: "Stop an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostFlag, _ := cmd.Flags().GetString("host")
			if onlyLocal(reg) || hostFlag == remote.LocalHost {
				return f.Stop(cmd.Context(), args[0])
			}
			host, agent, err := resolveAgentHost(cmd.Context(), reg, runner, localJSON(BuildRoot(f)), hostFlag, args[0])
			if err != nil {
				return err
			}
			if host == remote.LocalHost {
				return f.Stop(cmd.Context(), agent)
			}
			h, _ := reg.Get(host)
			return sshPassthrough(cmd.Context(), h.SSH, []string{"stop", agent, "--host", remote.LocalHost},
				cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), false)
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run 'TestResolveAgentHost' ./internal/remote/ -run TestSSHArgvTTY -v`
Expected: PASS. Then `go test ./...` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/remote.go internal/cli/remote_test.go internal/cli/cli.go internal/remote/ssh.go internal/remote/ssh_test.go
git commit -m "feat(cli): single-agent host resolution + ssh passthrough for attach/logs/stop/rm/submit"
```

---

## Task 9: Wire registry into `BuildRoot` + `main.go` + a test seam

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `main.go`
- Test: covered by Tasks 6–8 (which call `BuildRootWith`).

**Interfaces:**
- Consumes: all prior tasks.
- Produces:
  - `func BuildRoot(f *fleet.Fleet) *cobra.Command` — **unchanged signature**, now defined as `BuildRootWith(f, emptyRegistry, "", remote.ExecRunner)` so existing tests/callers and the in-process local leg keep working. The "plain tree" referenced in Task 7 is exactly a `BuildRoot` over an empty registry (no remotes ⇒ `onlyLocal` true ⇒ fast path).
  - `func BuildRootWith(f *fleet.Fleet, reg *remote.Registry, regPath string, runner remote.Runner) *cobra.Command` — the real wiring: adds persistent `--host`/`--all-hosts` flags, registers `versionCmd()` and `hostCmd(reg, regPath, runner)`, and constructs the routing-aware `list`/`inbox`/`agents`/single-agent commands.

- [ ] **Step 1: Write the failing test**

No new test file; Tasks 6–8 reference `BuildRootWith`. Add a smoke test to `internal/cli/cli_test.go`:
```go
func TestBuildRootHasRemoteFlagsAndCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	if root.PersistentFlags().Lookup("host") == nil || root.PersistentFlags().Lookup("all-hosts") == nil {
		t.Fatal("missing persistent --host/--all-hosts")
	}
	for _, name := range []string{"version", "host"} {
		found := false
		for _, c := range root.Commands() {
			if c.Name() == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %q command", name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestBuildRootHasRemote -v`
Expected: FAIL — no persistent flags / `version`/`host` not registered.

- [ ] **Step 3: Write minimal implementation**

Rewrite the top of `internal/cli/cli.go`:
```go
// BuildRoot wires the CLI against a Fleet with an empty host registry (local
// only). Existing callers/tests and the in-process local leg of an aggregate
// use this; it always takes the fast single-host path.
func BuildRoot(f *fleet.Fleet) *cobra.Command {
	reg := &remote.Registry{} // zero-value: no remotes; Hosts() => [local]
	return BuildRootWith(f, reg, "", remote.ExecRunner)
}

// BuildRootWith wires the CLI with a real host registry, enabling --host /
// --all-hosts routing across remote engines.
func BuildRootWith(f *fleet.Fleet, reg *remote.Registry, regPath string, runner remote.Runner) *cobra.Command {
	root := &cobra.Command{Use: "flotilla", Short: "Manage a fleet of autonomous coding agents"}
	root.PersistentFlags().String("host", "", "target a single host by name ('local' for this machine)")
	root.PersistentFlags().Bool("all-hosts", false, "target every registered host (the default for aggregate commands)")
	root.AddCommand(
		spawnCmd(f), listCmd(f, reg, runner), attachCmd(f, reg, runner), stopCmd(f, reg, runner),
		rmCmd(f, reg, runner), submitCmd(f, reg, runner), fetchCmd(f), logsCmd(f, reg, runner),
		daemonCmd(f), inboxCmd(f, reg, runner), agentsCmd(), doctorCmd(),
		versionCmd(), hostCmd(reg, regPath, runner),
	)
	return root
}
```

> **Registry zero-value:** `Registry{}` has a nil `remotes` map; `Hosts()`/`Get(local)` must tolerate nil (range over nil map is fine; `Get` only reads). Confirm `Load` is the only writer that needs the map initialized — `Add` is never called on the `BuildRoot` empty registry. If `Add` must work on a zero-value, lazily init in `Add` (`if r.remotes == nil { r.remotes = map[string]string{} }`). Add this guard in Task 2's `Add`.

Update `main.go`:
```go
func main() {
	f := &fleet.Fleet{
		Backend:        backend.NewDocker(),
		BaseImage:      "ubuntu:24.04",
		EgressFirewall: true,
		Forge:          forge.NewGH(),
	}
	regPath := remote.DefaultPath()
	reg, err := remote.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	root := cli.BuildRootWith(f, reg, regPath, remote.ExecRunner)
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```
(Add the `remote` import to `main.go`.)

> **Back-fill the lazy-init guard** into `internal/remote/registry.go` `Add` now (so `BuildRoot`'s zero-value registry is safe if ever mutated): first line of `Add`: `if r.remotes == nil { r.remotes = map[string]string{} }`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -v 2>&1 | tail -40` — then read the FULL output (do not tail in practice; the `tail` here is illustrative — run `go test ./...` and ingest all of it).
Expected: PASS across all packages; the smoke test and Tasks 6–8 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/cli.go main.go internal/remote/registry.go
git commit -m "feat(cli): wire host registry into BuildRoot + persistent --host/--all-hosts"
```

---

## Task 10: Live ssh-to-localhost integration test (self-skipping)

**Files:**
- Create: `internal/cli/remote_live_test.go`

**Interfaces:**
- Consumes: `BuildRootWith` (Task 9), `remote.Registry` (Task 2).

- [ ] **Step 1: Write the test (it is the deliverable; it self-skips)**

`internal/cli/remote_live_test.go`:
```go
package cli

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/mickzijdel/flotilla/internal/remote"
)

// sshLocalhostOK reports whether `ssh -o BatchMode=yes localhost true` works
// AND a `flotilla` binary is on the remote PATH — otherwise the live test skips.
func sshLocalhostOK(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		return false
	}
	ctx := context.Background()
	if err := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "localhost", "true").Run(); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "localhost", "command -v flotilla").Run() == nil
}

func TestLiveSSHRoundTrip(t *testing.T) {
	if !sshLocalhostOK(t) {
		t.Skip("ssh-to-localhost with flotilla on PATH unavailable; skipping live remote test")
	}
	path := filepath.Join(t.TempDir(), "hosts.toml")
	reg, _ := remote.Load(path)
	_ = reg.Add("selfhost", "localhost", false)
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRootWith(f, reg, path, remote.ExecRunner)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "--host", "selfhost"})
	if err := root.Execute(); err != nil {
		t.Fatalf("live version over ssh: %v", err)
	}
	if !strings.Contains(out.String(), "flotilla") {
		t.Fatalf("unexpected live output: %q", out.String())
	}
}
```

> `version --host selfhost` exercises the full SSH transport against a real `flotilla` over a real `ssh`, without needing Docker. It self-skips on any CI/dev box lacking passwordless ssh-to-localhost or a PATH `flotilla`.

- [ ] **Step 2: Run it (verify skip vs pass locally)**

Run: `go test ./internal/cli/ -run TestLiveSSHRoundTrip -v`
Expected: PASS where ssh-localhost + `flotilla` exist; SKIP otherwise (the message prints).

- [ ] **Step 3: Commit**

```bash
git add internal/cli/remote_live_test.go
git commit -m "test(remote): self-skipping live ssh-to-localhost round trip"
```

---

## Task 11: Doc corrections (README, backlog, design specs)

**Files:**
- Modify: `README.md`
- Modify: `docs/backlog.md`
- Modify: `docs/specs/2026-06-14-flotilla-design.md`
- Modify: `docs/specs/2026-06-24-flotilla-remote-backend-design.md`

- [ ] **Step 1: README** — replace the roadmap "`DOCKER_HOST` over TLS/SSH for multi-machine" line with the federated-client model: each host runs the full engine; the laptop registers hosts (`flotilla host add`) and drives them over SSH (`--host` / default all-hosts). Note the remote-Docker-socket approach was evaluated and rejected. Add a short "Remote hosts" usage stanza:

```markdown
### Remote hosts

Flotilla runs the whole engine on each machine; your laptop is a thin client.

    flotilla host add beefy user@beefy.example.com   # register a host
    flotilla host doctor                              # check ssh + version on all hosts
    flotilla list                                     # merged fleet across all hosts
    flotilla --host beefy spawn <repo> --prompt "…"   # run on one host
    flotilla attach beefy:brave-otter                 # attach to a remote agent

Each host needs `flotilla`, `docker`, and the `devcontainer` CLI installed, plus
its own git/gh/Claude credentials (no secrets ever transit the client).
```

- [ ] **Step 2: backlog** — in the "Remote backend" entry, correct "the `Backend` interface seam is already in place" to note the remote story is a **client transport layer above the CLI**, not a new `Backend`; the Docker-Sandboxes/`sbx` note stays as a genuine future `Backend`. Mark the entry's spec/plan links done-style once merged.

- [ ] **Step 3: design spec §7** — add a sentence: the remote-host/multi-machine pillar is realised as a federated SSH client (run the engine per host), not a remote-`DOCKER_HOST` substrate swap.

- [ ] **Step 4: remote-backend design spec §8** — add one clarifying sentence: `hosts[].version`/`contract` are populated by `host ls`/`host doctor`; ordinary aggregate commands populate `name`/`ok`/`error` (to avoid a second round-trip per host on every command).

- [ ] **Step 5: Commit**

```bash
git add README.md docs/backlog.md docs/specs/2026-06-14-flotilla-design.md docs/specs/2026-06-24-flotilla-remote-backend-design.md
git commit -m "docs: correct remote-backend framing (federated SSH client, not a Backend)"
```

---

## Task 12: Full-suite + lint gate

**Files:** none (verification).

- [ ] **Step 1: Run the full test suite and ingest all output**

Run: `go test ./...`
Expected: all packages PASS (the live ssh test SKIPs unless ssh-localhost + flotilla are present).

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 3: Lint + format**

Run: `golangci-lint run ./...` then `golangci-lint fmt --diff`
Expected: no findings; no diff. (If `golangci-lint fmt` reports changes, run `golangci-lint fmt` and re-commit.)

- [ ] **Step 4: Commit any lint/format fixes**

```bash
git add -A
git commit -m "chore(remote): lint/format fixes"
```

---

## Self-Review

**Spec coverage:**
- §2/§4 federated client, no new Backend, LocalTransport/SSHTransport → Tasks 4, 7, 9.
- §3 decision 2 (run remote binary over ssh, `--host local`) → Tasks 3, 7.
- §3 decision 3 / §6 (talk-only + `host` group + doctor) → Task 6.
- §3 decision 4 / §8 (default all-hosts, parallel, errors inline, `{rows,hosts}`) → Tasks 5, 7.
- §3 decision 5 / §7 (version handshake, warn-minor/block-contract) → Tasks 1, 4, 6.
- §3 decision 6 / §8 (`--host`, `FLOTILLA_HOST`, `host:agent`, cross-host resolution) → Tasks 7, 8. **Gap noted:** `FLOTILLA_HOST` env fallback — fold into Task 7's flag read (`if hostFlag == "" { hostFlag = os.Getenv("FLOTILLA_HOST") }`); added to that command's RunE.
- §5 registry/`hosts.toml` → Task 2.
- §9 attach/streaming passthrough → Task 8.
- §10 ssh mechanics + arg escaping + ControlMaster → Tasks 3, 8.
- §11 trust boundary (no secrets on client) → satisfied by construction (client only ships argv); no code task needed.
- §12 doc corrections → Task 11.
- §14 testing → every task is TDD; live self-skip → Task 10.
- §15 sequencing: `questions` aggregation is intentionally **not** built here (depends on the Q/A slice); it plugs into Task 7's identical pattern once landed — noted as out-of-scope-for-now, not a gap.

**Placeholder scan:** no TBD/TODO; every code step shows complete code.

**Type consistency:** `Host`, `Registry`, `Transport`, `Runner`, `LocalRunner`, `Aggregate`, `HostStatus`, `version.Info`, `BuildRootWith` signatures are consistent across Tasks 2–9 (e.g. `Fan(ctx, []Transport, fargs)` used identically in Tasks 5, 7, 8; `resolveAgentHost` returns `(host, agent, err)` consistently). `SSHArgv` is refactored to delegate to `SSHArgvTTY` in Task 8 (single implementation).

**Added during review:** `FLOTILLA_HOST` env fallback (Task 7); `Registry.Add` nil-map guard (Tasks 2/9).

---

## Execution Handoff

Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks.
2. **Inline Execution** — execute tasks in this session with checkpoints.
