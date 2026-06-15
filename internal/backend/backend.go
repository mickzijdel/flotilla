package backend

import (
	"context"
	"time"
)

// Label keys applied to every flotilla-managed container.
const (
	LabelAgent   = "flotilla.agent"
	LabelRepo    = "flotilla.repo"
	LabelCreated = "flotilla.created"
	LabelHost    = "flotilla.host"
)

// Mount is a host->container bind mount.
type Mount struct {
	Source string
	Target string
}

// CreateOpts describes a container to create.
type CreateOpts struct {
	Name    string
	Image   string
	Cmd     []string
	Workdir string
	Mounts  []Mount
	Env     map[string]string
	Labels  map[string]string
}

// Container is a flotilla-managed container as seen by a backend.
type Container struct {
	ID      string
	Name    string
	Repo    string
	Status  string // "running" | "exited"
	Created time.Time
	Labels  map[string]string
}

// AttachInfo tells a client how to attach to a container.
type AttachInfo struct {
	ContainerID string
	DockerExec  string
	VSCode      string
}

// Backend abstracts the compute substrate (local Docker for v1).
type Backend interface {
	Create(ctx context.Context, opts CreateOpts) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Exec(ctx context.Context, id string, cmd []string) error
	List(ctx context.Context, labelFilter map[string]string) ([]Container, error)
	AttachInfo(ctx context.Context, id string) (AttachInfo, error)
}

// NewDocker is implemented in docker.go (Task 5).
