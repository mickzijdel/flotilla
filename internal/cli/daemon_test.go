package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func TestDaemonStatusJSONWhenStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.flotilla
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"daemon", "status", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var st struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("bad json %q: %v", out.String(), err)
	}
	if st.Running {
		t.Fatal("no daemon started ⇒ running should be false")
	}
}

func TestDaemonStatusTextWhenStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"daemon", "status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "not running") {
		t.Fatalf("want 'not running' in %q", out.String())
	}
}

func TestDaemonStopWhenStoppedErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fleet.Fleet{Backend: backend.NewFake()}
	root := BuildRoot(f)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"daemon", "stop"})
	if err := root.Execute(); err == nil {
		t.Fatal("stop with no daemon should error")
	}
}
