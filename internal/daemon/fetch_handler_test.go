package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mickzijdel/flotilla/internal/fleet"
)

func TestFetchHandlerFetchesAndNotifies(t *testing.T) {
	fs := &fakeSubmitter{}
	s := newSup(t, fs)
	s.Registry = NewRegistry()
	s.registerHandlers()

	resp := s.fetchHandler(context.Background(), "otter", Request{ID: "1", Type: "fetch"})
	if resp.Status != "ok" {
		t.Fatalf("want ok, got %+v", resp)
	}
	if len(fs.fetches) != 1 || fs.fetches[0] != "otter" {
		t.Fatalf("Fetch not called for otter: %v", fs.fetches)
	}
	if !eventTypes(mustRead(t, s.Paths.Inbox()))[EventFetchDone] {
		t.Fatalf("missing fetch_done inbox event")
	}
}

func TestFetchHandlerSurfacesError(t *testing.T) {
	fs := &fakeSubmitter{fetchErr: map[string]error{"otter": errors.New(`no workspace clone for agent "otter"`)}}
	s := newSup(t, fs)
	resp := s.fetchHandler(context.Background(), "otter", Request{ID: "1", Type: "fetch"})
	if resp.Status != "error" || !strings.Contains(resp.Message, "no workspace clone") {
		t.Fatalf("want error response, got %+v", resp)
	}
	if !eventTypes(mustRead(t, s.Paths.Inbox()))[EventFetchDone] {
		t.Fatalf("fetch_done should be emitted even on failure")
	}
}

// TestScanOnceServicesFetchRequest drives the full seam: a request file in the
// agent's session dir gets a response written and triggers a fetch.
func TestScanOnceServicesFetchRequest(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-otter")
	if err := os.MkdirAll(filepath.Join(logDir, "requests"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "requests", "req1.json"),
		[]byte(`{"type":"fetch","id":"req1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSubmitter{}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter", Status: "running", LogDir: logDir}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}, Registry: NewRegistry(), Now: func() time.Time { return time.Unix(1, 0).UTC() }}
	s.registerHandlers()

	s.scanOnce(context.Background())

	if len(fs.fetches) != 1 {
		t.Fatalf("want exactly 1 fetch via the seam, got %d", len(fs.fetches))
	}
	rb, err := os.ReadFile(filepath.Join(logDir, "responses", "req1.json"))
	if err != nil {
		t.Fatalf("no response written: %v", err)
	}
	if !strings.Contains(string(rb), `"status":"ok"`) {
		t.Fatalf("response not ok: %s", rb)
	}
}
