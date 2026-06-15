package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEnvOnlyPresentKeys(t *testing.T) {
	look := func(k string) (string, bool) {
		if k == "PRESENT" {
			return "v", true
		}
		return "", false
	}
	got := resolveEnv([]string{"PRESENT", "ABSENT"}, look)
	if len(got) != 1 || got["PRESENT"] != "v" {
		t.Errorf("resolveEnv = %v, want {PRESENT:v}", got)
	}
}

func TestEnvFileContentSortedKV(t *testing.T) {
	b := envFileContent(map[string]string{"B": "2", "A": "1"})
	if string(b) != "A=1\nB=2\n" {
		t.Errorf("envFileContent = %q", b)
	}
}

func TestLaunchScriptSourcesEnvFileThenExecs(t *testing.T) {
	got := launchScript(`claude -p "hi"`, "/home/ubuntu", "/workspaces/atlas")
	if !strings.Contains(got, agentEnvFile("/home/ubuntu")) {
		t.Errorf("launchScript should source the env-file: %q", got)
	}
	if !strings.Contains(got, `exec claude -p "hi"`) {
		t.Errorf("launchScript should exec the launch: %q", got)
	}
	if !strings.Contains(got, "cd '/workspaces/atlas'") {
		t.Errorf("launchScript should cd into remoteWorkspaceFolder: %q", got)
	}
	if !strings.Contains(got, "export HOME=/home/ubuntu") {
		t.Errorf("launchScript should set HOME: %q", got)
	}
	// With empty workdir, fall back to the glob.
	if g := launchScript("x", "/root", ""); !strings.Contains(g, "/workspaces/*/") {
		t.Errorf("empty workdir should fall back to the glob: %q", g)
	}
}

func TestHomeForUser(t *testing.T) {
	for _, c := range []struct{ user, want string }{
		{"root", "/root"}, {"", "/root"}, {"ubuntu", "/home/ubuntu"},
	} {
		if got := homeForUser(c.user); got != c.want {
			t.Errorf("homeForUser(%q) = %q, want %q", c.user, got, c.want)
		}
	}
}

func TestRunAsUser(t *testing.T) {
	if got := runAsUser("root", "echo hi"); len(got) != 3 || got[0] != "sh" {
		t.Errorf("runAsUser(root) = %v, want sh -c", got)
	}
	got := runAsUser("ubuntu", "echo hi")
	if len(got) != 4 || got[0] != "su" || got[1] != "ubuntu" || got[2] != "-c" || got[3] != "echo hi" {
		t.Errorf("runAsUser(ubuntu) = %v, want [su ubuntu -c echo hi]", got)
	}
}

func TestDefaultDevcontainerJSONIsValidWithImage(t *testing.T) {
	b := defaultDevcontainerJSON("ubuntu:24.04")
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	if m["image"] != "ubuntu:24.04" {
		t.Errorf("image = %v", m["image"])
	}
	if m["remoteUser"] != "ubuntu" {
		t.Errorf("remoteUser = %v", m["remoteUser"])
	}
}

func TestHasDevcontainerDetectsConfig(t *testing.T) {
	dir := t.TempDir()
	if hasDevcontainer(dir) {
		t.Fatal("empty dir should have no devcontainer")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasDevcontainer(dir) {
		t.Error("should detect .devcontainer/devcontainer.json")
	}
}
