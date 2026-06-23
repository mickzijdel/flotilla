package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type dockerBackend struct{}

// NewDocker returns a Backend backed by the local `docker` CLI.
func NewDocker() Backend { return &dockerBackend{} }

func run(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func (d *dockerBackend) Create(ctx context.Context, o CreateOpts) (string, error) {
	args := []string{"create", "--name", o.Name}
	for k, v := range o.Labels {
		args = append(args, "--label", k+"="+v)
	}
	for k, v := range o.Env {
		args = append(args, "--env", k+"="+v)
	}
	for _, m := range o.Mounts {
		args = append(args, "--volume", m.Source+":"+m.Target)
	}
	if o.Network != "" {
		args = append(args, "--network", o.Network)
	}
	if o.Workdir != "" {
		args = append(args, "--workdir", o.Workdir)
	}
	args = append(args, o.Image)
	args = append(args, o.Cmd...)
	return run(ctx, args...)
}

func (d *dockerBackend) Start(ctx context.Context, id string) error {
	_, err := run(ctx, "start", id)
	return err
}

func (d *dockerBackend) Stop(ctx context.Context, id string) error {
	_, err := run(ctx, "stop", id)
	return err
}

func (d *dockerBackend) Remove(ctx context.Context, id string) error {
	_, err := run(ctx, "rm", "-f", id)
	return err
}

func (d *dockerBackend) Exec(ctx context.Context, id string, cmd []string) error {
	_, err := run(ctx, append([]string{"exec", id}, cmd...)...)
	return err
}

// psLine is the subset of `docker inspect`/`ps` fields we read.
type psLine struct {
	ID      string `json:"ID"`
	Names   string `json:"Names"`
	State   string `json:"State"`
	Created string `json:"CreatedAt"`
	Labels  string `json:"Labels"`
}

func (d *dockerBackend) List(ctx context.Context, filter map[string]string) ([]Container, error) {
	args := []string{"ps", "-a", "--no-trunc", "--format", "{{json .}}"}
	for k, v := range filter {
		args = append(args, "--filter", "label="+k+"="+v)
	}
	// Always scope to flotilla-managed containers.
	if _, ok := filter[LabelAgent]; !ok {
		args = append(args, "--filter", "label="+LabelAgent)
	}
	out, err := run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var result []Container
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p psLine
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return nil, fmt.Errorf("parse ps line: %w", err)
		}
		labels := parseLabels(p.Labels)
		// Report the real container state ("running", "exited", "created",
		// "paused", …) rather than collapsing everything non-running to "exited",
		// so the submit gate can tell a finished agent from one that never ran.
		status := p.State
		result = append(result, Container{
			ID:      p.ID,
			Name:    labels[LabelAgent],
			Repo:    labels[LabelRepo],
			Status:  status,
			Created: parseDockerTime(p.Created),
			Labels:  labels,
		})
	}
	return result, nil
}

func (d *dockerBackend) AttachInfo(_ context.Context, id string) (AttachInfo, error) {
	return AttachInfo{
		ContainerID: id,
		DockerExec:  "docker exec -it " + id + " bash",
		VSCode:      "Run 'Dev Containers: Attach to Running Container' and pick " + id,
	}, nil
}

// dockerEventLine is the subset of `docker events --format '{{json .}}'` we read.
type dockerEventLine struct {
	Status string `json:"status"` // "die" | "stop" | "start" | ...
	ID     string `json:"id"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"` // includes labels + "name"
	} `json:"Actor"`
}

func (d *dockerBackend) Events(ctx context.Context) (<-chan Event, error) {
	cmd := exec.CommandContext(ctx, "docker", "events",
		"--format", "{{json .}}",
		"--filter", "label="+LabelAgent,
		"--filter", "event=die",
		"--filter", "event=stop",
		"--filter", "event=start",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		defer func() { _ = cmd.Wait() }()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			var l dockerEventLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				continue
			}
			ev := Event{Type: l.Status, ID: l.ID, Labels: map[string]string{}}
			for k, v := range l.Actor.Attributes {
				ev.Labels[k] = v
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

func parseDockerTime(s string) time.Time {
	// docker ps CreatedAt: "2026-06-14 09:30:00 +0000 UTC"
	if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
