package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// devcontainer runs the devcontainer CLI and returns combined stdout.
func devcontainer(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "devcontainer", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("devcontainer %s: %w: %s", strings.Join(args, " "), err, errb.String())
	}
	return out.String(), nil
}

// Up provisions the repo's devcontainer (auto-discovered from the workspace's
// .devcontainer/, or a bundled default the engine wrote there), overlays the
// additional Features, and returns the container ID + remote user.
func (d *dockerBackend) Up(ctx context.Context, o UpOpts) (UpResult, error) {
	args, err := upArgs(o)
	if err != nil {
		return UpResult{}, err
	}
	out, err := devcontainer(ctx, args...)
	if err != nil {
		return UpResult{}, err
	}
	if res := upResultFromOutput(out); res.ID != "" {
		return res, nil
	}
	// Fallback: resolve by the agent label we just applied (remote user unknown).
	id, err := run(ctx, "ps", "-aq", "--no-trunc", "--filter", "status=running", "--filter", "label="+LabelAgent+"="+o.Labels[LabelAgent])
	if err != nil {
		return UpResult{}, err
	}
	return UpResult{ID: id}, nil
}

// upArgs builds the `devcontainer up` argument list: workspace, optional
// additional-features, bind mounts, and id-labels.
func upArgs(o UpOpts) ([]string, error) {
	args := []string{"up", "--workspace-folder", o.WorkspaceFolder}
	if len(o.AdditionalFeatures) > 0 {
		b, err := json.Marshal(o.AdditionalFeatures)
		if err != nil {
			return nil, fmt.Errorf("marshal additional-features: %w", err)
		}
		args = append(args, "--additional-features", string(b))
	}
	for _, m := range o.Mounts {
		args = append(args, "--mount", "type=bind,source="+m.Source+",target="+m.Target)
	}
	for k, v := range o.Labels {
		args = append(args, "--id-label", k+"="+v)
	}
	return args, nil
}

// ReadConfig reads the devcontainer's merged configuration without starting it,
// so the engine can resolve remoteUser before `up`.
func (d *dockerBackend) ReadConfig(ctx context.Context, workspaceFolder string) (ConfigInfo, error) {
	out, err := devcontainer(ctx, "read-configuration", "--workspace-folder", workspaceFolder, "--include-merged-configuration")
	if err != nil {
		return ConfigInfo{}, err
	}
	return ConfigInfo{RemoteUser: remoteUserFromConfig(out)}, nil
}

// remoteUserFromConfig pulls remoteUser from the trailing JSON line that
// `devcontainer read-configuration` emits (mergedConfiguration first, then the
// top-level field). Best-effort: "" when absent.
func remoteUserFromConfig(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r struct {
			MergedConfiguration struct {
				RemoteUser string `json:"remoteUser"`
			} `json:"mergedConfiguration"`
			RemoteUser string `json:"remoteUser"`
		}
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			if r.MergedConfiguration.RemoteUser != "" {
				return r.MergedConfiguration.RemoteUser
			}
			return r.RemoteUser
		}
	}
	return ""
}

// upResultFromOutput parses the trailing JSON line devcontainer up emits.
func upResultFromOutput(out string) UpResult {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r struct {
			ContainerID           string `json:"containerId"`
			RemoteUser            string `json:"remoteUser"`
			RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`
		}
		if err := json.Unmarshal([]byte(line), &r); err == nil && r.ContainerID != "" {
			return UpResult{ID: r.ContainerID, RemoteUser: r.RemoteUser, RemoteWorkspaceFolder: r.RemoteWorkspaceFolder}
		}
	}
	return UpResult{}
}

// ExecDetached runs cmd in the container without waiting (the backgrounded launch).
func (d *dockerBackend) ExecDetached(ctx context.Context, id string, cmd []string) error {
	_, err := run(ctx, append([]string{"exec", "-d", id}, cmd...)...)
	return err
}

// CopyTo copies a host file/dir into the container (no contents on argv).
func (d *dockerBackend) CopyTo(ctx context.Context, id, hostPath, destPath string) error {
	_, err := run(ctx, "cp", hostPath, id+":"+destPath)
	return err
}

// CopyFrom copies a file/dir out of the container to the host (docker cp).
func (d *dockerBackend) CopyFrom(ctx context.Context, id, srcPath, hostPath string) error {
	_, err := run(ctx, "cp", id+":"+srcPath, hostPath)
	return err
}
