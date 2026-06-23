package agent

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed builtin/*.toml
var builtinFS embed.FS

// Profile describes everything that varies between coding agents.
type Profile struct {
	Name           string   `toml:"name"`
	Install        string   `toml:"install"`
	Launch         string   `toml:"launch"`
	Setup          string   `toml:"setup"`
	ConfigMounts   []string `toml:"config_mounts"`
	Env            []string `toml:"env"`
	TranscriptPath string   `toml:"transcript_path"`
	EgressAllow    []string `toml:"egress_allow"`
	DoneSignal     string   `toml:"done_signal"`
	WrapUp         string   `toml:"wrap_up"`
}

// Parse decodes a profile from TOML bytes.
func Parse(b []byte) (Profile, error) {
	var p Profile
	if err := toml.Unmarshal(b, &p); err != nil {
		return Profile{}, fmt.Errorf("parse profile: %w", err)
	}
	return p, nil
}

// Builtins returns the embedded built-in profiles keyed by name.
func Builtins() (map[string]Profile, error) {
	out := map[string]Profile{}
	err := fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".toml") {
			return err
		}
		b, err := builtinFS.ReadFile(path)
		if err != nil {
			return err
		}
		p, err := Parse(b)
		if err != nil {
			return err
		}
		out[p.Name] = p
		return nil
	})
	return out, err
}

// RenderLaunch substitutes the {prompt} placeholder with a reference to the
// $FLOTILLA_PROMPT environment variable. The actual prompt is passed out-of-band
// (written to a file and loaded into that env var by the launch wrapper), so a
// prompt with shell metacharacters can never break or alter the command.
func (p Profile) RenderLaunch() string {
	return strings.ReplaceAll(p.Launch, "{prompt}", "$FLOTILLA_PROMPT")
}
