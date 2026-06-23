package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func seedLoggedContainer(t *testing.T, fake *backend.Fake, name, logBody, status string) string {
	t.Helper()
	dir := t.TempDir()
	if logBody != "" {
		if err := os.WriteFile(filepath.Join(dir, "container.log"), []byte(logBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if status != "" {
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = fake.Create(context.Background(), backend.CreateOpts{Labels: map[string]string{
		backend.LabelAgent:  name,
		backend.LabelRepo:   "r",
		backend.LabelLogDir: dir,
	}})
	return dir
}

func TestLogsCmdPrintsContainerLog(t *testing.T) {
	fake := backend.NewFake()
	seedLoggedContainer(t, fake, "atlas", "hello world\n", "done")
	f := &fleet.Fleet{Backend: fake}

	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logs", "atlas"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("logs: %v: %s", err, out.String())
	}
	if out.String() != "hello world\n" {
		t.Errorf("output = %q, want 'hello world\\n'", out.String())
	}
}

func TestLogsCmdJSONEnvelope(t *testing.T) {
	fake := backend.NewFake()
	seedLoggedContainer(t, fake, "atlas", "x\n", "done")
	f := &fleet.Fleet{Backend: fake}

	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logs", "atlas", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("logs --json: %v: %s", err, out.String())
	}
	var info fleet.LogInfo
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if info.Agent != "atlas" || info.Status != "done" {
		t.Errorf("info = %+v", info)
	}
}

func TestLogsCmdJSONAndFollowMutuallyExclusive(t *testing.T) {
	fake := backend.NewFake()
	seedLoggedContainer(t, fake, "atlas", "hello\n", "done")
	f := &fleet.Fleet{Backend: fake}

	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logs", "atlas", "--json", "-f"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for --json and -f together, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "json") || !strings.Contains(msg, "follow") {
		t.Errorf("error %q should mention both 'json' and 'follow'", msg)
	}
}

func TestLogsCmdFollowDrainsUntilDone(t *testing.T) {
	fake := backend.NewFake()
	// status already "done", so follow drains once and exits immediately.
	seedLoggedContainer(t, fake, "atlas", "line1\nline2\n", "done")
	f := &fleet.Fleet{Backend: fake}

	root := BuildRoot(f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logs", "atlas", "-f"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("logs -f: %v: %s", err, out.String())
	}
	if out.String() != "line1\nline2\n" {
		t.Errorf("follow output = %q", out.String())
	}
}
