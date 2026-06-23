package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Inbox event types (open set; later handlers add their own).
const (
	EventAgentDone     = "agent_done"
	EventPROpened      = "pr_opened"
	EventPRUpdated     = "pr_updated"
	EventSubmitSkipped = "submit_skipped"
)

// InboxEvent is one operator-facing notable event.
type InboxEvent struct {
	TS      time.Time      `json:"ts"`
	Agent   string         `json:"agent"`
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// AppendEvent appends e as one JSON line to path (created 0600 on first write).
func AppendEvent(path string, e InboxEvent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(line, '\n'))
	return err
}

// ReadEvents parses every line; if since is non-zero, keeps only newer events.
// A missing file is not an error (returns nil, nil).
func ReadEvents(path string, since time.Time) ([]InboxEvent, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []InboxEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e InboxEvent
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip malformed lines rather than failing the whole read
		}
		if !since.IsZero() && !e.TS.After(since) {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
