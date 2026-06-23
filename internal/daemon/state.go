package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AgentRecord is the daemon's per-agent bookkeeping (the state mirror).
type AgentRecord struct {
	Name             string    `json:"name"`
	LastStatus       string    `json:"lastStatus"`
	LastHandledSHA   string    `json:"lastHandledSHA"`   // HEAD at last done-handling (any outcome)
	LastSubmittedSHA string    `json:"lastSubmittedSHA"` // HEAD at last successful submit
	LastEventTS      time.Time `json:"lastEventTS"`
}

// LoadAgent reads a per-agent record; a missing file yields a zero record.
func (p Paths) LoadAgent(name string) (AgentRecord, error) {
	b, err := os.ReadFile(p.AgentRecord(name))
	if errors.Is(err, fs.ErrNotExist) {
		return AgentRecord{}, nil
	}
	if err != nil {
		return AgentRecord{}, err
	}
	var r AgentRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return AgentRecord{}, fmt.Errorf("parse agent record %s: %w", name, err)
	}
	return r, nil
}

// SaveAgent atomically writes a per-agent record (temp + rename).
func (p Paths) SaveAgent(r AgentRecord) error {
	if err := os.MkdirAll(p.AgentsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(p.AgentRecord(r.Name), b, 0o600)
}

// ListAgentRecords returns every saved record (missing dir ⇒ nil).
func (p Paths) ListAgentRecords() ([]AgentRecord, error) {
	entries, err := os.ReadDir(p.AgentsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AgentRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		r, err := p.LoadAgent(name)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// WriteVersion stamps the running binary's identity into the state dir.
func (p Paths) WriteVersion(stamp string) error {
	if err := os.MkdirAll(p.StateDir(), 0o700); err != nil {
		return err
	}
	return atomicWrite(p.Version(), []byte(stamp), 0o600)
}

// ReadVersion returns the stamped binary identity ("" if unset).
func (p Paths) ReadVersion() string {
	b, err := os.ReadFile(p.Version())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// BinaryStamp identifies a binary by size + mod time ("" on stat error).
func BinaryStamp(exePath string) string {
	fi, err := os.Stat(exePath)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// atomicWrite writes via a temp file + rename in the same dir.
func atomicWrite(path string, b []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
