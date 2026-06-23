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
	// LabelProxy marks a per-agent egress proxy sidecar (value = agent name).
	// Proxy containers also carry LabelAgent so the docker backend's always-on
	// flotilla.agent scope can find them, so the fleet layer must exclude
	// LabelProxy-tagged containers from agent listings/resolution.
	LabelProxy = "flotilla.proxy"
	// LabelLogDir records the host session-log dir for an agent so `flotilla
	// logs` can find container.log without date math.
	LabelLogDir = "flotilla.logdir"
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
	Network string // network to attach at create ("" = default bridge)
}

// Container is a flotilla-managed container as seen by a backend.
type Container struct {
	ID      string
	Name    string
	Repo    string
	Status  string // docker state: "running", "exited", "created", "paused", …
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
	Mounts             []Mount // host->container bind mounts added at `up`
}

// UpResult is the outcome of provisioning a devcontainer.
type UpResult struct {
	ID                    string
	RemoteUser            string
	RemoteWorkspaceFolder string
}

// ConfigInfo is the subset of a devcontainer's merged configuration the engine
// needs before `up` (to resolve the live transcript mount target).
type ConfigInfo struct {
	RemoteUser string
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
	ReadConfig(ctx context.Context, workspaceFolder string) (ConfigInfo, error)
	CopyFrom(ctx context.Context, id, srcPath, hostPath string) error
	ExecDetached(ctx context.Context, id string, cmd []string) error
	CopyTo(ctx context.Context, id, hostPath, destPath string) error
	NetworkCreate(ctx context.Context, name string, internal bool) error
	NetworkRemove(ctx context.Context, name string) error
	NetworkConnect(ctx context.Context, network, id string) error
	NetworkDisconnect(ctx context.Context, network, id string) error
	ContainerNetworks(ctx context.Context, id string) ([]string, error)
}
