package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/daemon"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func seedInbox(t *testing.T) {
	t.Helper()
	p := daemon.DefaultPaths()
	_ = daemon.AppendEvent(p.Inbox(), daemon.InboxEvent{TS: time.Unix(100, 0).UTC(), Agent: "otter", Type: daemon.EventAgentDone, Message: "done"})
	_ = daemon.AppendEvent(p.Inbox(), daemon.InboxEvent{TS: time.Unix(200, 0).UTC(), Agent: "otter", Type: daemon.EventPROpened, Message: "opened"})
}

func TestInboxText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedInbox(t)
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"inbox"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "agent_done") || !strings.Contains(s, "pr_opened") {
		t.Fatalf("missing events in %q", s)
	}
}

func TestInboxJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedInbox(t)
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"inbox", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl lines, got %d: %q", len(lines), out.String())
	}
}

func TestInboxSince(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedInbox(t)
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"inbox", "--since", time.Unix(150, 0).UTC().Format(time.RFC3339)})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	s := out.String()
	if strings.Contains(s, "agent_done") || !strings.Contains(s, "pr_opened") {
		t.Fatalf("since filter wrong: %q", s)
	}
}
