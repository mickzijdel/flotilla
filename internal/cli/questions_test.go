package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

// questionTestFleet registers a running agent with a session dir and seeds the
// given questions into it, returning the Fleet and the session dir.
func questionTestFleet(t *testing.T, agentName string, questions map[string]string) (*fleet.Fleet, string) {
	t.Helper()
	fake := backend.NewFake()
	dir := t.TempDir()
	reqDir := filepath.Join(dir, "requests")
	if err := os.MkdirAll(reqDir, 0o777); err != nil {
		t.Fatal(err)
	}
	for id, text := range questions {
		b, _ := json.Marshal(map[string]any{"type": "question", "id": id, "data": map[string]any{"text": text}})
		if err := os.WriteFile(filepath.Join(reqDir, id+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := fake.Up(context.Background(), backend.UpOpts{Labels: map[string]string{
		backend.LabelAgent:  agentName,
		backend.LabelLogDir: dir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	return &fleet.Fleet{Backend: fake}, dir
}

func TestQuestionsCmdHumanOutput(t *testing.T) {
	f, _ := questionTestFleet(t, "otter", map[string]string{"q1": "Drop the table?"})
	cmd := questionsCmd(f)
	cmd.SetArgs(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "otter") || !strings.Contains(s, "q1") || !strings.Contains(s, "Drop the table?") {
		t.Fatalf("human output = %q", s)
	}
}

func TestQuestionsCmdJSONOutput(t *testing.T) {
	f, _ := questionTestFleet(t, "otter", map[string]string{"q1": "Drop it?"})
	cmd := questionsCmd(f)
	cmd.SetArgs([]string{"--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got []fleet.PendingQuestion
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (out=%q)", err, out.String())
	}
	if len(got) != 1 || got[0].ID != "q1" || got[0].Agent != "otter" {
		t.Fatalf("json output = %+v", got)
	}
}

func TestQuestionsCmdJSONEmptyIsArray(t *testing.T) {
	f, _ := questionTestFleet(t, "otter", nil)
	cmd := questionsCmd(f)
	cmd.SetArgs([]string{"--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty JSON should be [], got %q", out.String())
	}
}
