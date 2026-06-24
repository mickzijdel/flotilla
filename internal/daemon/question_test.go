package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// eventByType returns the first inbox event of the given type (or a zero event).
func eventByType(evs []InboxEvent, typ string) InboxEvent {
	for _, e := range evs {
		if e.Type == typ {
			return e
		}
	}
	return InboxEvent{}
}

// TestQuestionHandlerNotifiesAndDefers: a question request emits a question
// inbox event (with id+text), marks the agent blocked in the mirror, returns
// the deferred sentinel, and (through the seam) writes NO response file.
func TestQuestionHandlerNotifiesAndDefers(t *testing.T) {
	sess := t.TempDir()
	writeReq(t, sess, "q1", Request{ID: "q1", Type: "question", Data: map[string]any{"text": "Drop users_old?"}})

	s := dispSup(t, NewRegistry())
	s.registerHandlers()

	s.dispatchRequests(context.Background(), "otter", sess)

	// No response file (non-terminal).
	if _, err := os.Stat(filepath.Join(sess, "responses", "q1.json")); !os.IsNotExist(err) {
		t.Fatalf("question must not write a response file yet (err=%v)", err)
	}
	// Inbox question event with id + text.
	ev := eventByType(mustRead(t, s.Paths.Inbox()), EventQuestion)
	if ev.Type != EventQuestion {
		t.Fatalf("missing %s inbox event", EventQuestion)
	}
	if ev.Data["id"] != "q1" || ev.Data["text"] != "Drop users_old?" {
		t.Fatalf("question event data = %+v", ev.Data)
	}
	// Blocked mark in the mirror.
	rec, _ := s.Paths.LoadAgent("otter")
	if !rec.Blocked {
		t.Fatalf("agent should be marked blocked, rec=%+v", rec)
	}
}

// TestQuestionAnsweredEmitsEventAndClearsBlocked: once flotilla answer writes a
// response, the next scan emits question_answered (with the answer) and clears
// the blocked mark.
func TestQuestionAnsweredEmitsEventAndClearsBlocked(t *testing.T) {
	sess := t.TempDir()
	writeReq(t, sess, "q1", Request{ID: "q1", Type: "question", Data: map[string]any{"text": "Drop it?"}})

	s := dispSup(t, NewRegistry())
	s.registerHandlers()
	s.dispatchRequests(context.Background(), "otter", sess) // defers + blocks

	// Operator answers out-of-band.
	respDir := filepath.Join(sess, "responses")
	if err := os.MkdirAll(respDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(respDir, "q1.json"), []byte(`{"status":"ok","data":{"answer":"Yes, it's unused."}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s.dispatchRequests(context.Background(), "otter", sess) // observes the answer

	ev := eventByType(mustRead(t, s.Paths.Inbox()), EventQuestionAnswered)
	if ev.Type != EventQuestionAnswered {
		t.Fatalf("missing %s inbox event", EventQuestionAnswered)
	}
	if ev.Data["id"] != "q1" || ev.Data["answer"] != "Yes, it's unused." {
		t.Fatalf("answered event data = %+v", ev.Data)
	}
	rec, _ := s.Paths.LoadAgent("otter")
	if rec.Blocked {
		t.Fatalf("blocked mark should be cleared after answer, rec=%+v", rec)
	}
}
