package feature

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractWritesFeatureFiles(t *testing.T) {
	dir := t.TempDir()
	path, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	manifest := filepath.Join(path, "devcontainer-feature.json")
	script := filepath.Join(path, "install.sh")
	for _, p := range []string{manifest, script} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
	// install.sh must be executable.
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("install.sh not executable: mode %v", info.Mode())
	}
	// Manifest must parse and carry the expected id.
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest not JSON: %v", err)
	}
	if m.ID != "flotilla-toolchain" {
		t.Errorf("id = %q, want flotilla-toolchain", m.ID)
	}
}
