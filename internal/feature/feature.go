// Package feature embeds the flotilla toolchain Dev Container Feature and
// extracts it to disk so `devcontainer up --additional-features` can reference
// it by absolute path.
package feature

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed toolchain
var toolchainFS embed.FS

// Extract writes the embedded toolchain Feature into destDir/flotilla-toolchain
// and returns its absolute path. install.sh is made executable.
func Extract(destDir string) (string, error) {
	root := filepath.Join(destDir, "flotilla-toolchain")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(toolchainFS, "toolchain")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := toolchainFS.ReadFile("toolchain/" + e.Name())
		if err != nil {
			return "", err
		}
		mode := os.FileMode(0o644)
		if filepath.Ext(e.Name()) == ".sh" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(root, e.Name()), b, mode); err != nil {
			return "", err
		}
	}
	return filepath.Abs(root)
}
