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

// UpOpts describes a devcontainer to provision (build + inject Feature + start,
// idling). It replaces Create+Start for the agent path.
type UpOpts struct {
	Name               string
	WorkspaceFolder    string         // engine clone dir → devcontainer --workspace-folder
	AdditionalFeatures map[string]any // e.g. {"./flotilla-toolchain": {}} (relative to .devcontainer/)
	Labels             map[string]string
}

// UpResult is the outcome of provisioning a devcontainer.
type UpResult struct {
	ID                    string
	RemoteUser            string
	RemoteWorkspaceFolder string
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
	Up(ctx context.Context, opts UpOpts) (UpResult, error)
	ExecDetached(ctx context.Context, id string, cmd []string) error
	CopyTo(ctx context.Context, id, hostPath, destPath string) error
}
