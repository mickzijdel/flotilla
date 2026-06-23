package backend

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func devcontainerAvailable() bool {
	if !dockerAvailable() {
		return false
	}
	_, err := exec.LookPath("devcontainer")
	return err == nil
}

func TestUpResultFromOutputParsesTrailingJSON(t *testing.T) {
	out := "Building...\nsome log line\n{\"outcome\":\"success\",\"containerId\":\"abc123\",\"remoteUser\":\"ubuntu\",\"remoteWorkspaceFolder\":\"/workspaces/repo\"}\n"
	got := upResultFromOutput(out)
	if got.ID != "abc123" || got.RemoteUser != "ubuntu" {
		t.Errorf("upResultFromOutput = %+v, want {abc123 ubuntu}", got)
	}
	if got.RemoteWorkspaceFolder != "/workspaces/repo" {
		t.Errorf("upResultFromOutput.RemoteWorkspaceFolder = %q, want /workspaces/repo", got.RemoteWorkspaceFolder)
	}
	if got := upResultFromOutput("no json here"); got.ID != "" {
		t.Errorf("upResultFromOutput = %+v, want empty", got)
	}
}

func TestDockerCopyToIntegration(t *testing.T) {
	if !devcontainerAvailable() {
		t.Skip("docker+devcontainer not available; skipping integration test")
	}
	ctx := context.Background()
	d := NewDocker()
	id, err := d.Create(ctx, CreateOpts{
		Name:   "flotilla-copyto-test",
		Image:  "alpine:3.20",
		Cmd:    []string{"sleep", "60"},
		Labels: map[string]string{LabelAgent: "copyto-test"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer d.Remove(ctx, id) //nolint:errcheck
	if err := d.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	src := t.TempDir() + "/payload"
	if err := osWriteFile(src, "hi"); err != nil {
		t.Fatal(err)
	}
	if err := d.Exec(ctx, id, []string{"mkdir", "-p", "/run/flotilla"}); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := d.CopyTo(ctx, id, src, "/run/flotilla/payload"); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if err := d.Exec(ctx, id, []string{"test", "-f", "/run/flotilla/payload"}); err != nil {
		t.Errorf("copied file missing in container: %v", err)
	}
}

func osWriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestUpArgsRendersBindMount(t *testing.T) {
	args, err := upArgs(UpOpts{
		WorkspaceFolder: "/work",
		Mounts:          []Mount{{Source: "/host/sess", Target: "/flotilla/session"}},
	})
	if err != nil {
		t.Fatalf("upArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "up --workspace-folder /work") {
		t.Errorf("missing up/workspace in %q", joined)
	}
	if !strings.Contains(joined, "--mount type=bind,source=/host/sess,target=/flotilla/session") {
		t.Errorf("missing --mount in %q", joined)
	}
}

func TestRemoteUserFromMergedConfig(t *testing.T) {
	out := "build log line\n{\"mergedConfiguration\":{\"remoteUser\":\"ubuntu\"}}\n"
	if got := remoteUserFromConfig(out); got != "ubuntu" {
		t.Errorf("remoteUserFromConfig = %q, want ubuntu", got)
	}
	if got := remoteUserFromConfig("no json here"); got != "" {
		t.Errorf("remoteUserFromConfig(no json) = %q, want empty", got)
	}
}
