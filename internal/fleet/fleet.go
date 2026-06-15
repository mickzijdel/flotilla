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
	Backend   backend.Backend
	BaseImage string
	WorkRoot  string // host dir holding per-agent clones; defaults under ~/.flotilla
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
	if err := gitops.Clone(ctx, repoURL, dest); err != nil {
		return Agent{}, err
	}

	// Scratch holds the extracted Feature + (optional) default config; consumed
	// by `devcontainer up`'s build, then removed. Kept out of the agent's workspace.
	scratch, err := os.MkdirTemp("", "flotilla-"+name+"-")
	if err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	featPath, err := feature.Extract(scratch)
	if err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("extract feature: %w", err)
	}

	configPath := ""
	if !hasDevcontainer(dest) {
		configPath = filepath.Join(scratch, "devcontainer.json")
		if err := os.WriteFile(configPath, defaultDevcontainerJSON(f.BaseImage), 0o644); err != nil {
			_ = os.RemoveAll(dest)
			return Agent{}, fmt.Errorf("write default devcontainer: %w", err)
		}
	}

	id, err := f.Backend.Up(ctx, backend.UpOpts{
		Name:               name,
		WorkspaceFolder:    dest,
		ConfigPath:         configPath,
		AdditionalFeatures: map[string]any{featPath: map[string]any{}},
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

	inj := &injector{be: f.Backend, id: id}

	// 1) Secrets: resolved allowlist → 0600 env-file → container (no git creds).
	env := resolveEnv(prof.Env, os.LookupEnv)
	if err := inj.WriteFile(ctx, envFileContent(env), agentEnvFile); err != nil {
		return Agent{}, fmt.Errorf("inject secrets: %w", err)
	}
	// 2) Config: setup handler / declarative config_mounts.
	if err := setup.Run(ctx, inj, prof); err != nil {
		return Agent{}, fmt.Errorf("setup: %w", err)
	}
	// 3) Install the agent CLI.
	if strings.TrimSpace(prof.Install) != "" {
		if err := f.Backend.Exec(ctx, id, []string{"sh", "-c", prof.Install}); err != nil {
			return Agent{}, fmt.Errorf("install agent: %w", err)
		}
	}
	// 4) Launch the agent, backgrounded (exec-into-idle).
	if err := f.Backend.ExecDetached(ctx, id, launchWrapper(prof.RenderLaunch(prompt))); err != nil {
		return Agent{}, fmt.Errorf("launch agent: %w", err)
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
	if len(cs) == 0 {
		return backend.Container{}, fmt.Errorf("no agent named %q", name)
	}
	return cs[0], nil
}

// Attach returns attach info for a named agent.
func (f *Fleet) Attach(ctx context.Context, name string) (backend.AttachInfo, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return backend.AttachInfo{}, err
	}
	return f.Backend.AttachInfo(ctx, c.ID)
}

// Stop stops a named agent's container.
func (f *Fleet) Stop(ctx context.Context, name string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	return f.Backend.Stop(ctx, c.ID)
}

// Remove force-removes a named agent's container.
func (f *Fleet) Remove(ctx context.Context, name string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	return f.Backend.Remove(ctx, c.ID)
}
