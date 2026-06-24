package fleet

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// seedQuestion drops a question request (and optionally its answer response) into
// an agent's session dir.
func seedQuestion(t *testing.T, logDir, id, text string) {
	t.Helper()
	reqDir := filepath.Join(logDir, "requests")
	if err := os.MkdirAll(reqDir, 0o777); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"type": "question", "id": id, "data": map[string]any{"text": text}})
	if err := os.WriteFile(filepath.Join(reqDir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAnswer(t *testing.T, logDir, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(logDir, "responses", id+".json"))
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Answer string `json:"answer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("parse response %q: %v", b, err)
	}
	if resp.Status != "ok" {
		t.Fatalf("response status = %q, want ok", resp.Status)
	}
	return resp.Data.Answer
}

func TestAnswerWritesResponseForLonePending(t *testing.T) {
	fake := backend.NewFake()
	dir := seedLoggedAgent(t, fake, "otter", "running")
	seedQuestion(t, dir, "q1", "Drop it?")
	f := &Fleet{Backend: fake}

	if err := f.Answer(context.Background(), "otter", "", `Yes, "drop" it\now`); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got := readAnswer(t, dir, "q1"); got != `Yes, "drop" it\now` {
		t.Fatalf("answer round-trip = %q", got)
	}
}

func TestAnswerErrorsWhenNoPending(t *testing.T) {
	fake := backend.NewFake()
	seedLoggedAgent(t, fake, "otter", "running")
	f := &Fleet{Backend: fake}
	err := f.Answer(context.Background(), "otter", "", "hi")
	if err == nil || !strings.Contains(err.Error(), "no pending question") {
		t.Fatalf("want no-pending error, got %v", err)
	}
}

func TestAnswerRequiresIDWhenSeveralPending(t *testing.T) {
	fake := backend.NewFake()
	dir := seedLoggedAgent(t, fake, "otter", "running")
	seedQuestion(t, dir, "q1", "A?")
	seedQuestion(t, dir, "q2", "B?")
	f := &Fleet{Backend: fake}

	if err := f.Answer(context.Background(), "otter", "", "x"); err == nil || !strings.Contains(err.Error(), "--id") {
		t.Fatalf("want --id disambiguation error, got %v", err)
	}
	if err := f.Answer(context.Background(), "otter", "q2", "x"); err != nil {
		t.Fatalf("Answer by id: %v", err)
	}
	if got := readAnswer(t, dir, "q2"); got != "x" {
		t.Fatalf("answer = %q", got)
	}
}

func TestQuestionsListsPendingAcrossAgents(t *testing.T) {
	fake := backend.NewFake()
	d1 := seedLoggedAgent(t, fake, "otter", "running")
	d2 := seedLoggedAgent(t, fake, "badger", "running")
	seedQuestion(t, d1, "q1", "first?")
	seedQuestion(t, d2, "q2", "second?")
	// An already-answered question must be excluded.
	seedQuestion(t, d1, "q0", "answered?")
	if err := os.MkdirAll(filepath.Join(d1, "responses"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d1, "responses", "q0.json"), []byte(`{"status":"ok","data":{"answer":"done"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &Fleet{Backend: fake}
	qs, err := f.Questions(context.Background())
	if err != nil {
		t.Fatalf("Questions: %v", err)
	}
	got := map[string]string{}
	for _, q := range qs {
		got[q.ID] = q.Agent + ":" + q.Text
		if q.Asked.IsZero() {
			t.Errorf("question %s has zero Asked time", q.ID)
		}
	}
	if len(qs) != 2 || got["q1"] != "otter:first?" || got["q2"] != "badger:second?" {
		t.Fatalf("Questions = %+v", qs)
	}
}

func TestListOverlaysBlockedStatus(t *testing.T) {
	fake := backend.NewFake()
	dir := seedLoggedAgent(t, fake, "otter", "running")
	cs, _ := fake.List(context.Background(), map[string]string{backend.LabelAgent: "otter"})
	_ = fake.SetStatus(cs[0].ID, "running")
	seedQuestion(t, dir, "q1", "blocked on this?")
	f := &Fleet{Backend: fake}

	statusOf := func() string {
		agents, err := f.List(context.Background())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, a := range agents {
			if a.Name == "otter" {
				return a.Status
			}
		}
		t.Fatal("otter not listed")
		return ""
	}
	if s := statusOf(); s != "blocked" {
		t.Fatalf("status with pending question = %q, want blocked", s)
	}
	// Answer it → reverts to running, no daemon involved.
	if err := f.Answer(context.Background(), "otter", "q1", "go ahead"); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(); s != "running" {
		t.Fatalf("status after answer = %q, want running", s)
	}
}
