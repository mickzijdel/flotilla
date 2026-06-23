package daemon

import (
	"os"
	"path/filepath"
)

// Paths resolves every daemon file under the ~/.flotilla root.
type Paths struct{ Root string }

// DefaultPaths roots the daemon under ~/.flotilla (or "." if home is unknown).
func DefaultPaths() Paths {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return Paths{Root: filepath.Join(home, ".flotilla")}
}

func (p Paths) Pid() string       { return filepath.Join(p.Root, "daemon.pid") }
func (p Paths) Lock() string      { return filepath.Join(p.Root, "daemon.lock") }
func (p Paths) Log() string       { return filepath.Join(p.Root, "daemon.log") }
func (p Paths) Inbox() string     { return filepath.Join(p.Root, "inbox.jsonl") }
func (p Paths) StateDir() string  { return filepath.Join(p.Root, "daemon") }
func (p Paths) Version() string   { return filepath.Join(p.StateDir(), "version") }
func (p Paths) AgentsDir() string { return filepath.Join(p.StateDir(), "agents") }
func (p Paths) AgentRecord(name string) string {
	return filepath.Join(p.AgentsDir(), name+".json")
}
func (p Paths) LogsRoot() string { return filepath.Join(p.Root, "logs") }
