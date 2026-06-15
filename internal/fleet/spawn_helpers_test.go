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

func TestLaunchWrapperSourcesEnvFileThenExecs(t *testing.T) {
	got := launchWrapper(`claude -p "hi"`)
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("launchWrapper shape = %v", got)
	}
	if !strings.Contains(got[2], agentEnvFile) || !strings.Contains(got[2], `exec claude -p "hi"`) {
		t.Errorf("launchWrapper script = %q", got[2])
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
