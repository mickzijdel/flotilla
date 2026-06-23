package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// acquireLock takes an exclusive, non-blocking flock on path. The returned file
// must stay open for as long as the lock is needed; closing it releases the lock.
func acquireLock(path string) (*os.File, error) {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("daemon already running (lock held): %w", err)
	}
	return f, nil
}

func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
}

func writePidFile(path string, pid int) error {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return err
	}
	return atomicWrite(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func readPidFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// IsRunning reports whether a live daemon process is recorded in the pidfile.
func IsRunning(p Paths) bool {
	pid, err := readPidFile(p.Pid())
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// shouldReexec is true when both stamps are known and they differ.
func shouldReexec(stored, current string) bool {
	return stored != "" && current != "" && stored != current
}

// Status is the daemon's externally-visible state (for `flotilla daemon status`).
type Status struct {
	Running       bool         `json:"running"`
	PID           int          `json:"pid"`
	Version       string       `json:"version"`
	WatchedAgents int          `json:"watchedAgents"`
	Recent        []InboxEvent `json:"recent"`
}

// ReadStatus assembles daemon status from the pidfile + state mirror + inbox.
func ReadStatus(p Paths, recent int) Status {
	st := Status{Running: IsRunning(p), Version: p.ReadVersion()}
	if pid, err := readPidFile(p.Pid()); err == nil {
		st.PID = pid
	}
	if recs, err := p.ListAgentRecords(); err == nil {
		st.WatchedAgents = len(recs)
	}
	if evs, err := ReadEvents(p.Inbox(), time.Time{}); err == nil {
		if len(evs) > recent {
			evs = evs[len(evs)-recent:]
		}
		st.Recent = evs
	}
	return st
}

// StopDaemon SIGTERMs the recorded pid and polls until it exits (or wait elapses).
func StopDaemon(p Paths, wait time.Duration) error {
	pid, err := readPidFile(p.Pid())
	if err != nil {
		return fmt.Errorf("daemon not running (no pidfile)")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if proc.Signal(syscall.Signal(0)) != nil {
			return nil // gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon (pid %d) did not exit within %s", pid, wait)
}
