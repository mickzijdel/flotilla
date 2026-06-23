package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

// fakeSubmitter records Submit calls and returns scripted results per agent.
type fakeSubmitter struct {
	heads    map[string]string
	results  map[string]fleet.Submission
	errs     map[string]error
	calls    []string
	fetches  []string
	fetchErr map[string]error
}

func (f *fakeSubmitter) Fetch(_ context.Context, name string) error {
	f.fetches = append(f.fetches, name)
	if f.fetchErr != nil {
		return f.fetchErr[name]
	}
	return nil
}

func (f *fakeSubmitter) HeadSHA(_ context.Context, name string) (string, error) {
	return f.heads[name], nil
}

func (f *fakeSubmitter) Submit(_ context.Context, name string, force bool) (fleet.Submission, error) {
	if !force {
		return fleet.Submission{}, errors.New("daemon must force-submit")
	}
	f.calls = append(f.calls, name)
	if e := f.errs[name]; e != nil {
		return fleet.Submission{}, e
	}
	return f.results[name], nil
}

// fakeFleet satisfies fleetAPI: a fakeSubmitter plus a static agent list.
type fakeFleet struct {
	*fakeSubmitter
	agents []fleet.Agent
}

func (f *fakeFleet) List(_ context.Context) ([]fleet.Agent, error) { return f.agents, nil }

func newSup(t *testing.T, fs *fakeSubmitter) *Supervisor {
	t.Helper()
	return &Supervisor{
		Fleet: &fakeFleet{fakeSubmitter: fs},
		Paths: Paths{Root: t.TempDir()},
		Now:   func() time.Time { return time.Unix(1000, 0).UTC() },
	}
}

func eventTypes(evs []InboxEvent) map[string]bool {
	m := map[string]bool{}
	for _, e := range evs {
		m[e.Type] = true
	}
	return m
}

func TestHandleCleanTreeOpensPRAndRecordsSHA(t *testing.T) {
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Agent: "otter", Branch: "flotilla/otter", PRURL: "http://pr/1", Created: true}},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "otter")

	if len(fs.calls) != 1 {
		t.Fatalf("want exactly 1 submit, got %d", len(fs.calls))
	}
	rec, _ := s.Paths.LoadAgent("otter")
	if rec.LastSubmittedSHA != "sha1" || rec.LastHandledSHA != "sha1" {
		t.Fatalf("record not updated: %+v", rec)
	}
	types := eventTypes(mustRead(t, s.Paths.Inbox()))
	if !types[EventAgentDone] || !types[EventPROpened] {
		t.Fatalf("missing inbox events, got %v", types)
	}
}

func TestHandleDirtyTreeSkipsSubmit(t *testing.T) {
	fs := &fakeSubmitter{
		heads: map[string]string{"finch": "sha9"},
		errs:  map[string]error{"finch": errors.New(`agent "finch" has uncommitted changes; commit them inside the container first`)},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "finch")

	rec, _ := s.Paths.LoadAgent("finch")
	if rec.LastSubmittedSHA != "" {
		t.Fatalf("dirty tree must not record a submitted SHA: %+v", rec)
	}
	if rec.LastHandledSHA != "sha9" {
		t.Fatalf("handled SHA should be recorded even on skip: %+v", rec)
	}
	types := eventTypes(mustRead(t, s.Paths.Inbox()))
	if !types[EventSubmitSkipped] || !types[EventAgentDone] {
		t.Fatalf("want agent_done+submit_skipped, got %v", types)
	}
}

func TestHandleDedupBySHA(t *testing.T) {
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Created: true, PRURL: "u"}},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "otter")
	s.handle(context.Background(), "otter") // same HEAD ⇒ no second submit, no new events
	if len(fs.calls) != 1 {
		t.Fatalf("want 1 submit after dedup, got %d", len(fs.calls))
	}
	if got := len(mustRead(t, s.Paths.Inbox())); got != 2 {
		t.Fatalf("dedup should suppress repeat events, got %d", got)
	}
}

func TestHandleAdvancedHEADResubmits(t *testing.T) {
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Created: true, PRURL: "u"}},
	}
	s := newSup(t, fs)
	s.handle(context.Background(), "otter")
	fs.heads["otter"] = "sha2"
	fs.results["otter"] = fleet.Submission{Created: false, PRURL: "u"} // existing PR ⇒ pr_updated
	s.handle(context.Background(), "otter")
	if len(fs.calls) != 2 {
		t.Fatalf("advanced HEAD should re-submit, got %d calls", len(fs.calls))
	}
}

func writeStatus(t *testing.T, logDir, status string) {
	t.Helper()
	if err := os.MkdirAll(logDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "status"), []byte(status+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanOnceHandlesDoneAgents(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-otter")
	writeStatus(t, logDir, "done")
	fs := &fakeSubmitter{
		heads:   map[string]string{"otter": "sha1"},
		results: map[string]fleet.Submission{"otter": {Created: true, PRURL: "u"}},
	}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter", Status: "running", LogDir: logDir}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}, Now: func() time.Time { return time.Unix(1, 0).UTC() }}

	s.scanOnce(context.Background())
	if len(fs.calls) != 1 {
		t.Fatalf("done agent should be submitted once, got %d", len(fs.calls))
	}
}

func TestScanOnceIgnoresRunningAgents(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-busy")
	writeStatus(t, logDir, "running")
	fs := &fakeSubmitter{heads: map[string]string{"busy": "sha1"}}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "busy", LogDir: logDir}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}}
	s.scanOnce(context.Background())
	if len(fs.calls) != 0 {
		t.Fatalf("running agent must not be submitted, got %d", len(fs.calls))
	}
}

func TestRunStartupScanThenCancel(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs", "o-r", "sess-otter")
	writeStatus(t, logDir, "done")
	fs := &fakeSubmitter{heads: map[string]string{"otter": "sha1"}, results: map[string]fleet.Submission{"otter": {Created: true}}}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter", LogDir: logDir}}}
	be := backend.NewFake()
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}, Backend: be}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, 10*time.Millisecond) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if len(fs.calls) < 1 {
		t.Fatalf("startup scan should have submitted, got %d", len(fs.calls))
	}
}

func TestDrainEventsHandlesDie(t *testing.T) {
	tmp := t.TempDir()
	fs := &fakeSubmitter{heads: map[string]string{"otter": "sha1"}, results: map[string]fleet.Submission{"otter": {Created: true}}}
	ff := &fakeFleet{fakeSubmitter: fs, agents: []fleet.Agent{{Name: "otter"}}}
	s := &Supervisor{Fleet: ff, Paths: Paths{Root: tmp}}
	ev := backend.Event{Type: "die", Labels: map[string]string{backend.LabelAgent: "otter"}}
	s.handleEvent(context.Background(), ev)
	if len(fs.calls) != 1 {
		t.Fatalf("die event should trigger handle, got %d", len(fs.calls))
	}
}

func mustRead(t *testing.T, path string) []InboxEvent {
	t.Helper()
	evs, err := ReadEvents(path, time.Time{})
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	return evs
}
