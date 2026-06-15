package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/egress"
	"github.com/mickzijdel/flotilla/internal/feature"
	"github.com/mickzijdel/flotilla/internal/gitops"
	"github.com/mickzijdel/flotilla/internal/naming"
	"github.com/mickzijdel/flotilla/internal/setup"
)

// Agent is a flotilla-managed agent as the engine sees it.
type Agent struct {
	Name    string    `json:"name"`
	Repo    string    `json:"repo"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
	ID      string    `json:"id"`
}

// Fleet orchestrates agents over a Backend.
type Fleet struct {
	Backend        backend.Backend
	BaseImage      string
	WorkRoot       string   // host dir holding per-agent clones; defaults under ~/.flotilla
	EgressFirewall bool     // default-deny egress via a per-agent proxy (default true via main)
	EgressAllow    []string // engine-wide extra allowlist entries
}

func (f *Fleet) workRoot() string {
	if f.WorkRoot != "" {
		return f.WorkRoot
	}
	return filepath.Join(homeDir(), ".flotilla", "work")
}

// Spawn clones repoURL engine-side, provisions a devcontainer with the toolchain
// Feature, injects the agent's token + config, installs the agent CLI, and
// launches it. Git credentials never enter the container.
func (f *Fleet) Spawn(ctx context.Context, repoURL string, prof agent.Profile, prompt string) (Agent, error) {
	existing, err := f.List(ctx)
	if err != nil {
		return Agent{}, err
	}
	taken := map[string]bool{}
	for _, a := range existing {
		taken[a.Name] = true
	}
	name := naming.Pick(taken)

	dest := filepath.Join(f.workRoot(), name)
	// A leftover clone at dest (name is free per List, so any dir here is a
	// stale orphan from an interrupted spawn) would make git clone fail.
	_ = os.RemoveAll(dest)
	if err := gitops.Clone(ctx, repoURL, dest); err != nil {
		return Agent{}, err
	}

	// Overlay the vendored toolchain Feature via `devcontainer up
	// --additional-features`, on top of the repo's own devcontainer when present
	// or a bundled default otherwise. The devcontainer CLI only resolves a *local*
	// Feature when it lives in a sub-folder of the workspace's .devcontainer/ and
	// is referenced by a path relative to that folder — so extract it there and
	// reference it as "./flotilla-toolchain".
	devDir := filepath.Join(dest, ".devcontainer")
	if _, err := feature.Extract(devDir); err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("extract feature: %w", err)
	}
	if !hasDevcontainer(dest) {
		cfg := filepath.Join(devDir, "devcontainer.json")
		if err := os.WriteFile(cfg, defaultDevcontainerJSON(f.BaseImage), 0o644); err != nil {
			_ = os.RemoveAll(dest)
			return Agent{}, fmt.Errorf("write default devcontainer: %w", err)
		}
	}

	res, err := f.Backend.Up(ctx, backend.UpOpts{
		Name:               name,
		WorkspaceFolder:    dest,
		AdditionalFeatures: map[string]any{"./flotilla-toolchain": map[string]any{}},
		Labels: map[string]string{
			backend.LabelAgent:   name,
			backend.LabelRepo:    repoURL,
			backend.LabelCreated: time.Now().UTC().Format(time.RFC3339),
			backend.LabelHost:    "local",
		},
	})
	if err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("provision container: %w", err)
	}
	id := res.ID
	user := res.RemoteUser
	if user == "" {
		user = "root"
	}
	home := homeForUser(user)

	inj := &injector{be: f.Backend, id: id, user: user}

	// After provisioning, any failure must remove both the container and the clone
	// so a failed spawn leaves no orphan (the container is labelled and would
	// otherwise appear in List and hold the name).
	fail := func(e error) (Agent, error) {
		if f.EgressFirewall {
			teardownFirewall(ctx, f.Backend, name)
		}
		_ = f.Backend.Remove(ctx, id)
		_ = os.RemoveAll(dest)
		return Agent{}, e
	}

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
	// Prompt: written out-of-band (file via docker cp, never argv) and loaded
	// into $FLOTILLA_PROMPT by the launch wrapper, so metacharacters are inert.
	if err := inj.WriteFile(ctx, []byte(prompt), agentPromptFile(home)); err != nil {
		return fail(fmt.Errorf("inject prompt: %w", err))
	}
	// 2) Config: setup handler / declarative config_mounts, in the run user's home.
	if err := setup.Run(ctx, inj, prof, home); err != nil {
		return fail(fmt.Errorf("setup: %w", err))
	}
	// 3) Install the agent CLI as root (global npm needs root).
	if strings.TrimSpace(prof.Install) != "" {
		if err := f.Backend.Exec(ctx, id, []string{"sh", "-c", prof.Install}); err != nil {
			return fail(fmt.Errorf("install agent: %w", err))
		}
	}
	// 3.5) Egress firewall: confine the agent to the allowlist (fail-closed).
	if f.EgressFirewall {
		allow := egress.Compose(egress.BakedAllowlist(), prof.EgressAllow, f.EgressAllow)
		if err := setupFirewall(ctx, f.Backend, id, name, allow); err != nil {
			return fail(fmt.Errorf("egress firewall: %w", err))
		}
	}
	// 4) Launch the agent as the non-root run user, backgrounded (exec-into-idle).
	if err := f.Backend.ExecDetached(ctx, id, runAsUser(user, launchScript(prof.RenderLaunch(), home, res.RemoteWorkspaceFolder))); err != nil {
		return fail(fmt.Errorf("launch agent: %w", err))
	}

	return Agent{Name: name, Repo: repoURL, Status: "running", Created: time.Now().UTC(), ID: id}, nil
}

// List returns all flotilla-managed agents known to the backend.
func (f *Fleet) List(ctx context.Context) ([]Agent, error) {
	cs, err := f.Backend.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(cs))
	for _, c := range cs {
		if c.Labels[backend.LabelProxy] != "" {
			continue // egress proxy sidecar, not an agent
		}
		out = append(out, Agent{Name: c.Name, Repo: c.Repo, Status: c.Status, Created: c.Created, ID: c.ID})
	}
	return out, nil
}

// resolve finds the backend container ID for an agent name.
func (f *Fleet) resolve(ctx context.Context, name string) (backend.Container, error) {
	cs, err := f.Backend.List(ctx, map[string]string{backend.LabelAgent: name})
	if err != nil {
		return backend.Container{}, err
	}
	for _, c := range cs {
		if c.Labels[backend.LabelProxy] == "" {
			return c, nil // the agent container, not its proxy sidecar
		}
	}
	return backend.Container{}, fmt.Errorf("no agent named %q", name)
}

// Attach returns attach info for a named agent.
func (f *Fleet) Attach(ctx context.Context, name string) (backend.AttachInfo, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return backend.AttachInfo{}, err
	}
	return f.Backend.AttachInfo(ctx, c.ID)
}

// Stop stops a named agent's container and its egress proxy.
func (f *Fleet) Stop(ctx context.Context, name string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	if proxies, err := f.Backend.List(ctx, map[string]string{backend.LabelProxy: name}); err == nil {
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
