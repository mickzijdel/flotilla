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
	"github.com/mickzijdel/flotilla/internal/forge"
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
	LogDir  string    `json:"logDir,omitempty"`
}

// Fleet orchestrates agents over a Backend.
type Fleet struct {
	Backend        backend.Backend
	BaseImage      string
	WorkRoot       string      // host dir holding per-agent clones; defaults under ~/.flotilla
	LogRoot        string      // host dir for per-session logs; defaults under ~/.flotilla
	EgressFirewall bool        // default-deny egress via a per-agent proxy (default true via main)
	EgressAllow    []string    // engine-wide extra allowlist entries
	Forge          forge.Forge // PR creation; nil → push-only
}

func (f *Fleet) workRoot() string {
	if f.WorkRoot != "" {
		return f.WorkRoot
	}
	return filepath.Join(homeDir(), ".flotilla", "work")
}

// LogsDir returns the resolved per-session logs root (exported for the daemon).
func (f *Fleet) LogsDir() string { return f.logsRoot() }

// HeadSHA returns the current HEAD SHA of agent name's engine-side clone.
func (f *Fleet) HeadSHA(ctx context.Context, name string) (string, error) {
	return gitops.HeadSHA(ctx, f.workDir(name))
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

	// Per-session host log dir: live transcript mount + container.log + status.
	session := filepath.Join(f.logsRoot(), repoSlug(repoURL), sessionDirName(name, time.Now()))
	transcript := filepath.Join(session, "transcript")
	if err := os.MkdirAll(transcript, 0o777); err != nil {
		_ = os.RemoveAll(dest)
		return Agent{}, fmt.Errorf("create log dir: %w", err)
	}
	_ = os.Chmod(session, 0o777)
	_ = os.Chmod(transcript, 0o777)

	// Always mount the session dir at a fixed, user-agnostic path (container.log
	// + status ride here). Add the live transcript mount only when we can resolve
	// the run user's home before `up` (Docker needs an absolute container path).
	mounts := []backend.Mount{{Source: session, Target: containerSessionDir}}
	liveMount := false
	mountUser := ""
	if cfg, err := f.Backend.ReadConfig(ctx, dest); err == nil && cfg.RemoteUser != "" {
		if target := transcriptTarget(prof.TranscriptPath, homeForUser(cfg.RemoteUser)); target != "" {
			mounts = append(mounts, backend.Mount{Source: transcript, Target: target})
			liveMount = true
			mountUser = cfg.RemoteUser
		}
	}

	res, err := f.Backend.Up(ctx, backend.UpOpts{
		Name:               name,
		WorkspaceFolder:    dest,
		AdditionalFeatures: map[string]any{"./flotilla-toolchain": map[string]any{}},
		Mounts:             mounts,
		Labels: map[string]string{
			backend.LabelAgent:   name,
			backend.LabelRepo:    repoURL,
			backend.LabelCreated: time.Now().UTC().Format(time.RFC3339),
			backend.LabelHost:    "local",
			backend.LabelLogDir:  session,
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

	// Make the mounted session tree writable by the run user (uid may differ
	// from the host). Best-effort.
	_ = f.Backend.Exec(ctx, id, []string{"chown", "-R", user, containerSessionDir})

	// Write a copy-fallback sentinel when the transcript wasn't live-mounted at the
	// right place: either we couldn't resolve a user pre-up, or the real post-up run
	// user differs from the one we mounted for (so the live mount points at the wrong
	// home). The lazy copy-out in Fleet.Logs then recovers the transcript after exit.
	if (!liveMount || user != mountUser) && strings.TrimSpace(prof.TranscriptPath) != "" {
		if target := transcriptTarget(prof.TranscriptPath, home); target != "" {
			_ = os.WriteFile(filepath.Join(session, ".copy-fallback"), []byte(target+"\n"), 0o644)
		}
	}

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
	// The fetch + ask hints are always appended so the agent knows it can ask the
	// engine to refresh origin (flotilla-fetch) and ask its operator a blocking
	// question (flotilla-ask), despite having no network or credentials.
	fullPrompt := agent.PromptWithAskHint(agent.PromptWithFetchHint(agent.PromptWithWrapUp(prompt, prof.WrapUpText())))
	if err := inj.WriteFile(ctx, []byte(fullPrompt), agentPromptFile(home)); err != nil {
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
	// 3.1) Install the flotilla-fetch shim (root step, on PATH): lets the
	// credential-less agent ask the engine to fetch origin via the daemon's
	// request channel. Independent of the agent profile.
	if err := installFetchShim(ctx, f.Backend, id); err != nil {
		return fail(fmt.Errorf("install fetch shim: %w", err))
	}
	// 3.2) Install the flotilla-ask shim (root step, on PATH): lets the agent ask
	// its operator a blocking question via the daemon's request channel.
	if err := installAskShim(ctx, f.Backend, id); err != nil {
		return fail(fmt.Errorf("install ask shim: %w", err))
	}
	// 3.5) Egress firewall: confine the agent to the allowlist (fail-closed).
	if f.EgressFirewall {
		allow := egress.Compose(egress.BakedAllowlist(), prof.EgressAllow, f.EgressAllow)
		if err := setupFirewall(ctx, f.Backend, id, name, allow); err != nil {
			return fail(fmt.Errorf("egress firewall: %w", err))
		}
	}
	// 4) Launch the agent as the non-root run user, backgrounded (exec-into-idle).
	if err := f.Backend.ExecDetached(ctx, id, runAsUser(user, launchScript(prof.RenderLaunch(), home, res.RemoteWorkspaceFolder, containerSessionDir))); err != nil {
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
		logDir := c.Labels[backend.LabelLogDir]
		status := c.Status
		// Overlay a derived "blocked" state on a running agent waiting on an
		// operator question — computed purely from the filesystem, so it needs no
		// daemon. The launch-wrapper status file (which the daemon reads for
		// done-detection) is untouched; this overlay is only on the listed Status.
		if status == "running" && hasPendingQuestion(logDir) {
			status = "blocked"
		}
		out = append(out, Agent{Name: c.Name, Repo: c.Repo, Status: status, Created: c.Created, ID: c.ID, LogDir: logDir})
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

// Attach returns attach info for a named agent, auto-starting it if it is not
// running (a process-exit done-signal leaves the container stopped but present,
// and docker exec needs it running).
func (f *Fleet) Attach(ctx context.Context, name string) (backend.AttachInfo, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return backend.AttachInfo{}, err
	}
	if c.Status != "running" {
		if err := f.Backend.Start(ctx, c.ID); err != nil {
			return backend.AttachInfo{}, fmt.Errorf("start agent %q (status %s): %w", name, c.Status, err)
		}
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
