package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dispSup builds a Supervisor wired to reg with a real (temp) Paths root, for
// exercising the dispatch seam directly.
func dispSup(t *testing.T, reg *Registry) *Supervisor {
	t.Helper()
	return &Supervisor{
		Fleet:    &fakeFleet{fakeSubmitter: &fakeSubmitter{}},
		Paths:    Paths{Root: t.TempDir()},
		Registry: reg,
		Now:      func() time.Time { return time.Unix(1000, 0).UTC() },
	}
}

func writeReq(t *testing.T, sess, id string, req Request) {
	t.Helper()
	reqDir := filepath.Join(sess, "requests")
	if err := os.MkdirAll(reqDir, 0o777); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	if err := os.WriteFile(filepath.Join(reqDir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchRequestsWritesResponse(t *testing.T) {
	sess := t.TempDir()
	writeReq(t, sess, "abc", Request{ID: "abc", Type: "ping", Data: map[string]any{"x": "y"}})

	reg := NewRegistry()
	var gotAgent string
	reg.Register("ping", func(_ context.Context, agent string, r Request) Response {
		gotAgent = agent
		return Response{Status: "ok", Message: "pong", Data: map[string]any{"echo": r.Data["x"]}}
	})

	dispSup(t, reg).dispatchRequests(context.Background(), "otter", sess)

	if gotAgent != "otter" {
		t.Fatalf("handler got agent %q", gotAgent)
	}
	respPath := filepath.Join(sess, "responses", "abc.json")
	rb, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("no response written: %v", err)
	}
	var resp Response
	_ = json.Unmarshal(rb, &resp)
	if resp.Status != "ok" || resp.Data["echo"] != "y" {
		t.Fatalf("bad response: %+v", resp)
	}

	// Idempotent: a second dispatch with the response present is a no-op.
	called := false
	reg2 := NewRegistry()
	reg2.Register("ping", func(_ context.Context, _ string, _ Request) Response {
		called = true
		return Response{Status: "ok"}
	})
	dispSup(t, reg2).dispatchRequests(context.Background(), "otter", sess)
	if called {
		t.Fatal("already-answered request must not be re-dispatched")
	}
}

func TestDispatchUnknownType(t *testing.T) {
	sess := t.TempDir()
	writeReq(t, sess, "z", Request{ID: "z", Type: "nope"})

	dispSup(t, NewRegistry()).dispatchRequests(context.Background(), "otter", sess)

	rb, err := os.ReadFile(filepath.Join(sess, "responses", "z.json"))
	if err != nil {
		t.Fatalf("unknown type should still get an error response: %v", err)
	}
	var resp Response
	_ = json.Unmarshal(rb, &resp)
	if resp.Status != "error" {
		t.Fatalf("want error status, got %+v", resp)
	}
}

// TestDispatchDeferredWritesNoResponseAndDispatchesOnce proves the §4.1 seam
// change: a handler returning the deferred sentinel writes NO response file and
// is invoked exactly once even across repeated scans (notified once, not per
// tick) — until a real response appears out-of-band.
func TestDispatchDeferredWritesNoResponseAndDispatchesOnce(t *testing.T) {
	sess := t.TempDir()
	writeReq(t, sess, "q1", Request{ID: "q1", Type: "defer"})

	reg := NewRegistry()
	calls := 0
	reg.Register("defer", func(_ context.Context, _ string, _ Request) Response {
		calls++
		return Response{Status: StatusDeferred}
	})
	s := dispSup(t, reg)

	s.dispatchRequests(context.Background(), "otter", sess)
	s.dispatchRequests(context.Background(), "otter", sess) // re-scan: must not re-dispatch

	if calls != 1 {
		t.Fatalf("deferred handler dispatched %d times, want exactly 1", calls)
	}
	if _, err := os.Stat(filepath.Join(sess, "responses", "q1.json")); !os.IsNotExist(err) {
		t.Fatalf("deferred return must not write a response file (err=%v)", err)
	}
}

// TestDispatchDeferredAnsweredOutOfBand proves that once a response file appears
// for a deferred request (as flotilla answer writes it), the next scan observes
// the transition via onAnswered exactly once.
func TestDispatchDeferredAnsweredOutOfBand(t *testing.T) {
	sess := t.TempDir()
	writeReq(t, sess, "q1", Request{ID: "q1", Type: "defer"})

	reg := NewRegistry()
	reg.Register("defer", func(_ context.Context, _ string, _ Request) Response {
		return Response{Status: StatusDeferred}
	})
	s := dispSup(t, reg)

	var answered []string
	s.onAnsweredHook = func(agent, id, typ, _ string) {
		answered = append(answered, agent+"/"+id+"/"+typ)
	}

	s.dispatchRequests(context.Background(), "otter", sess) // defers
	// Operator answers out-of-band.
	respDir := filepath.Join(sess, "responses")
	if err := os.MkdirAll(respDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(respDir, "q1.json"), []byte(`{"status":"ok","data":{"answer":"yes"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s.dispatchRequests(context.Background(), "otter", sess) // observes the answer
	s.dispatchRequests(context.Background(), "otter", sess) // and only once

	if len(answered) != 1 || answered[0] != "otter/q1/defer" {
		t.Fatalf("onAnswered fired %v, want exactly [otter/q1/defer]", answered)
	}
}
