// Package preflight checks the host prerequisites flotilla needs to spawn
// agents: the docker CLI, a reachable docker daemon, and the devcontainer CLI.
package preflight

import (
	"context"
	"os/exec"
)

// Report is the outcome of the prerequisite checks.
type Report struct {
	Docker       bool
	DockerDaemon bool
	Devcontainer bool
}

// OK reports whether every prerequisite is satisfied.
func (r Report) OK() bool { return r.Docker && r.DockerDaemon && r.Devcontainer }

// Deps are the host seams the checks use (swapped out in tests).
type Deps struct {
	Look   func(string) (string, error)
	Daemon func(context.Context) error
}

// Real wires Deps to the host.
func Real() Deps {
	return Deps{
		Look: exec.LookPath,
		Daemon: func(ctx context.Context) error {
			return exec.CommandContext(ctx, "docker", "info").Run()
		},
	}
}

// Check runs the prerequisite checks.
func Check(ctx context.Context, d Deps) Report {
	var r Report
	if _, err := d.Look("docker"); err == nil {
		r.Docker = true
		if d.Daemon(ctx) == nil {
			r.DockerDaemon = true
		}
	}
	if _, err := d.Look("devcontainer"); err == nil {
		r.Devcontainer = true
	}
	return r
}

// Messages returns one human-readable status line per check.
func (r Report) Messages() []string {
	line := func(ok bool, name, fix string) string {
		if ok {
			return "ok       " + name
		}
		return "MISSING  " + name + " — " + fix
	}
	return []string{
		line(r.Docker, "docker CLI", "install Docker"),
		line(r.DockerDaemon, "docker daemon", "start Docker"),
		line(r.Devcontainer, "devcontainer CLI", "npm i -g @devcontainers/cli"),
	}
}
