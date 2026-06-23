package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDispatchRequestsWritesResponse(t *testing.T) {
	sess := t.TempDir()
	reqDir := filepath.Join(sess, "requests")
	if err := os.MkdirAll(reqDir, 0o777); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "abc", Type: "ping", Data: map[string]any{"x": "y"}}
	b, _ := json.Marshal(req)
	if err := os.WriteFile(filepath.Join(reqDir, "abc.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	var gotAgent string
	reg.Register("ping", func(_ context.Context, agent string, r Request) Response {
		gotAgent = agent
		return Response{Status: "ok", Message: "pong", Data: map[string]any{"echo": r.Data["x"]}}
	})

	dispatchRequests(context.Background(), reg, "otter", sess)

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
	dispatchRequests(context.Background(), reg2, "otter", sess)
	if called {
		t.Fatal("already-answered request must not be re-dispatched")
	}
}

func TestDispatchUnknownType(t *testing.T) {
	sess := t.TempDir()
	reqDir := filepath.Join(sess, "requests")
	_ = os.MkdirAll(reqDir, 0o777)
	b, _ := json.Marshal(Request{ID: "z", Type: "nope"})
	_ = os.WriteFile(filepath.Join(reqDir, "z.json"), b, 0o644)

	dispatchRequests(context.Background(), NewRegistry(), "otter", sess)

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
