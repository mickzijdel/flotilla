package backend

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func devcontainerAvailable() bool {
	if !dockerAvailable() {
		return false
	}
	_, err := exec.LookPath("devcontainer")
	return err == nil
}

func TestContainerIDFromUpParsesTrailingJSON(t *testing.T) {
	out := "Building...\nsome log line\n{\"outcome\":\"success\",\"containerId\":\"abc123\",\"remoteUser\":\"root\"}\n"
	if got := containerIDFromUp(out); got != "abc123" {
		t.Errorf("containerIDFromUp = %q, want abc123", got)
	}
	if got := containerIDFromUp("no json here"); got != "" {
		t.Errorf("containerIDFromUp = %q, want empty", got)
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
