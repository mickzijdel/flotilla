package daemon

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestRunForegroundSingleInstance(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	_ = os.MkdirAll(p.Root, 0o700)
	// Hold the lock as if a daemon were already running.
	held, err := acquireLock(p.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	sup := &Supervisor{Paths: p}
	// Already-locked ⇒ RunForeground returns promptly without error.
	if err := RunForeground(context.Background(), sup, p, "/bin/true", time.Second); err != nil {
		t.Fatalf("expected clean no-op when already running, got %v", err)
	}
}

func TestRunForegroundWritesPidThenCleansUp(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	fs := &fakeSubmitter{}
	sup := &Supervisor{Fleet: &fakeFleet{fakeSubmitter: fs}, Paths: p}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunForeground(ctx, sup, p, "/bin/true", 10*time.Millisecond) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := readPidFile(p.Pid()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid, err := readPidFile(p.Pid()); err != nil || pid != os.Getpid() {
		t.Fatalf("pidfile not written with our pid: %d %v", pid, err)
	}
	if p.ReadVersion() == "" {
		t.Fatal("version stamp not written")
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("RunForeground: %v", err)
	}
	if _, err := readPidFile(p.Pid()); err == nil {
		t.Fatal("pidfile should be removed on clean shutdown")
	}
}

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
