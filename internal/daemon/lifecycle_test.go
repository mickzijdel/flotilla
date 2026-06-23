package daemon

import (
	"os"
	"testing"
	"time"
)

func TestAcquireLockRejectsSecond(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	f1, err := acquireLock(p.Lock())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := acquireLock(p.Lock()); err == nil {
		t.Fatal("second acquire must fail while lock is held")
	}
	_ = f1.Close()
	f2, err := acquireLock(p.Lock())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = f2.Close()
}

func TestPidFileRoundTrip(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	if err := writePidFile(p.Pid(), 4242); err != nil {
		t.Fatalf("write: %v", err)
	}
	pid, err := readPidFile(p.Pid())
	if err != nil || pid != 4242 {
		t.Fatalf("got %d, %v", pid, err)
	}
}

func TestIsRunningSelf(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	if IsRunning(p) {
		t.Fatal("no pidfile ⇒ not running")
	}
	_ = writePidFile(p.Pid(), os.Getpid()) // our own pid is alive
	if !IsRunning(p) {
		t.Fatal("live pid ⇒ running")
	}
	_ = writePidFile(p.Pid(), 9999999) // unlikely-live pid
	if IsRunning(p) {
		t.Fatal("dead pid ⇒ not running")
	}
}

func TestShouldReexec(t *testing.T) {
	cases := []struct {
		stored, current string
		want            bool
	}{
		{"a", "b", true},
		{"a", "a", false},
		{"", "b", false},
		{"a", "", false},
	}
	for _, c := range cases {
		if got := shouldReexec(c.stored, c.current); got != c.want {
			t.Errorf("shouldReexec(%q,%q)=%v want %v", c.stored, c.current, got, c.want)
		}
	}
}

func TestReadStatus(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = writePidFile(p.Pid(), os.Getpid())
	_ = p.WriteVersion("42-7")
	_ = p.SaveAgent(AgentRecord{Name: "otter", LastStatus: "done"})
	_ = AppendEvent(p.Inbox(), InboxEvent{TS: time.Unix(1, 0).UTC(), Agent: "otter", Type: EventAgentDone})

	st := ReadStatus(p, 10)
	if !st.Running || st.PID != os.Getpid() || st.Version != "42-7" {
		t.Fatalf("status basics: %+v", st)
	}
	if st.WatchedAgents != 1 || len(st.Recent) != 1 {
		t.Fatalf("status counts: %+v", st)
	}
}
