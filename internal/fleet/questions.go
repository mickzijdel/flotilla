package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// PendingQuestion is one operator question a running agent is blocked on,
// derived purely from the filesystem (a requests/<id>.json of type "question"
// with no matching responses/<id>.json).
type PendingQuestion struct {
	Agent string    `json:"agent"`
	ID    string    `json:"id"`
	Text  string    `json:"text"`
	Asked time.Time `json:"asked"`
}

// answerEnvelope is the fixed-field-order response written for an answered
// question. The field order (status, then data.answer) keeps "answer" as the
// last string before the closing braces so the in-container flotilla-ask shim's
// greedy POSIX sed extracts it correctly — do NOT marshal a map here (Go sorts
// map keys, which would put "data" before "status" and break the shim).
type answerEnvelope struct {
	Status string     `json:"status"`
	Data   answerData `json:"data"`
}

type answerData struct {
	Answer string `json:"answer"`
}

// pendingQuestions returns every unanswered question in one agent's session dir.
// A missing/empty requests dir yields nil.
func pendingQuestions(agent, logDir string) []PendingQuestion {
	if logDir == "" {
		return nil
	}
	reqDir := filepath.Join(logDir, "requests")
	respDir := filepath.Join(logDir, "responses")
	entries, err := os.ReadDir(reqDir)
	if err != nil {
		return nil
	}
	var out []PendingQuestion
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if _, err := os.Stat(filepath.Join(respDir, id+".json")); err == nil {
			continue // already answered
		}
		b, err := os.ReadFile(filepath.Join(reqDir, e.Name()))
		if err != nil {
			continue
		}
		var req struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Data struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		if json.Unmarshal(b, &req) != nil || req.Type != "question" {
			continue
		}
		asked := time.Time{}
		if fi, err := e.Info(); err == nil {
			asked = fi.ModTime()
		}
		out = append(out, PendingQuestion{Agent: agent, ID: id, Text: req.Data.Text, Asked: asked})
	}
	return out
}

// hasPendingQuestion reports whether an agent's session dir holds ≥1 unanswered
// question (the filesystem-derived "blocked" signal).
func hasPendingQuestion(logDir string) bool {
	return len(pendingQuestions("", logDir)) > 0
}

// Questions lists every pending operator question across the fleet, derived
// purely from the filesystem so it works with the daemon down.
func (f *Fleet) Questions(ctx context.Context) ([]PendingQuestion, error) {
	agents, err := f.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []PendingQuestion
	for _, a := range agents {
		out = append(out, pendingQuestions(a.Name, a.LogDir)...)
	}
	return out, nil
}

// Answer writes the operator's reply to a pending question directly into the
// agent's session dir, terminating the deferred request the in-container shim is
// blocking on. Daemon-independent: it's a scoped file write, so it works whether
// or not the daemon runs. When id is empty there must be exactly one pending
// question; otherwise id selects among several.
func (f *Fleet) Answer(ctx context.Context, name, id, text string) error {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	logDir := c.Labels[backend.LabelLogDir]
	if logDir == "" {
		return fmt.Errorf("no session dir for agent %q (no logs recorded)", name)
	}
	pending := pendingQuestions(name, logDir)
	if len(pending) == 0 {
		return fmt.Errorf("no pending question for agent %q", name)
	}
	if id == "" {
		if len(pending) > 1 {
			ids := make([]string, len(pending))
			for i, q := range pending {
				ids[i] = q.ID
			}
			return fmt.Errorf("agent %q has %d pending questions; pass --id <id> (one of: %s)", name, len(pending), strings.Join(ids, ", "))
		}
		id = pending[0].ID
	} else {
		found := false
		for _, q := range pending {
			if q.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no pending question %q for agent %q", id, name)
		}
	}

	respDir := filepath.Join(logDir, "responses")
	if err := os.MkdirAll(respDir, 0o777); err != nil {
		return err
	}
	b, err := json.Marshal(answerEnvelope{Status: "ok", Data: answerData{Answer: text}})
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(respDir, id+".json"), b)
}

// atomicWriteFile writes b to path via a temp file + rename in the same dir, so
// the in-container shim never observes a half-written response.
func atomicWriteFile(path string, b []byte) error {
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
