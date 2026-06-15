package fleet

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/gitops"
	"github.com/mickzijdel/flotilla/internal/naming"
)

// Agent is a flotilla-managed agent as the engine sees it.
type Agent struct {
	Name    string
	Repo    string
	Status  string
	Created time.Time
	ID      string
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

// Spawn clones repoURL engine-side, then creates+starts a container that runs
// the profile's launch command on the mounted clone.
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

	const containerWork = "/workspace"
	id, err := f.Backend.Create(ctx, backend.CreateOpts{
		Name:    "flotilla-" + name,
		Image:   f.BaseImage,
		Cmd:     []string{"sh", "-c", prof.RenderLaunch(prompt)},
		Workdir: containerWork,
		Mounts:  []backend.Mount{{Source: dest, Target: containerWork}},
		Labels: map[string]string{
			backend.LabelAgent:   name,
			backend.LabelRepo:    repoURL,
			backend.LabelCreated: time.Now().UTC().Format(time.RFC3339),
			backend.LabelHost:    "local",
		},
	})
	if err != nil {
		return Agent{}, fmt.Errorf("create container: %w", err)
	}
	if err := f.Backend.Start(ctx, id); err != nil {
		return Agent{}, fmt.Errorf("start container: %w", err)
	}
	return Agent{Name: name, Repo: repoURL, Status: "running", Created: time.Now().UTC(), ID: id}, nil
}

// List is implemented in Task 8.
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
