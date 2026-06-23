package daemon

import (
	"path/filepath"
	"testing"
	"time"
)

func TestInboxAppendAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.jsonl")
	t0 := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC)
	must := func(e InboxEvent) {
		if err := AppendEvent(path, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	must(InboxEvent{TS: t0, Agent: "a", Type: EventAgentDone, Message: "done"})
	must(InboxEvent{TS: t0.Add(time.Minute), Agent: "a", Type: EventPROpened, Message: "opened", Data: map[string]any{"prURL": "u"}})

	all, err := ReadEvents(path, time.Time{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 events, got %d", len(all))
	}
	if all[1].Type != EventPROpened || all[1].Data["prURL"] != "u" {
		t.Fatalf("bad second event: %+v", all[1])
	}

	since, err := ReadEvents(path, t0)
	if err != nil {
		t.Fatalf("read since: %v", err)
	}
	if len(since) != 1 || since[0].Type != EventPROpened {
		t.Fatalf("since filter: got %+v", since)
	}
}

func TestReadEventsMissingFile(t *testing.T) {
	got, err := ReadEvents(filepath.Join(t.TempDir(), "nope.jsonl"), time.Time{})
	if err != nil || got != nil {
		t.Fatalf("want (nil,nil), got (%v,%v)", got, err)
	}
}
