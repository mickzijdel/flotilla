package setup

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
)

// recInjector records what a handler does.
type recInjector struct {
	execs  [][]string
	copies [][2]string // {hostPath, destPath}
	writes map[string]string
}

func newRec() *recInjector { return &recInjector{writes: map[string]string{}} }

func (r *recInjector) Exec(_ context.Context, cmd []string) error {
	r.execs = append(r.execs, cmd)
	return nil
}
func (r *recInjector) CopyTo(_ context.Context, hostPath, destPath string) error {
	r.copies = append(r.copies, [2]string{hostPath, destPath})
	return nil
}
func (r *recInjector) WriteFile(_ context.Context, content []byte, destPath string) error {
	r.writes[destPath] = string(content)
	return nil
}

func TestClaudeSetupWritesSettingsAndMakesDir(t *testing.T) {
	r := newRec()
	if err := Run(context.Background(), r, agent.Profile{Setup: "builtin:claude"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := r.writes["/root/.claude/settings.json"]; !ok {
		t.Errorf("expected settings.json write, writes=%v", r.writes)
	}
	foundMkdir := false
	for _, c := range r.execs {
		if len(c) >= 3 && c[0] == "mkdir" && c[2] == "/root/.claude" {
			foundMkdir = true
		}
	}
	if !foundMkdir {
		t.Errorf("expected mkdir -p /root/.claude, execs=%v", r.execs)
	}
}

func TestDeclarativeCopiesConfigMounts(t *testing.T) {
	r := newRec()
	prof := agent.Profile{Setup: "declarative", ConfigMounts: []string{"/etc/hostcfg:/root/.cfg"}}
	if err := Run(context.Background(), r, prof); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(r.copies) != 1 || r.copies[0] != [2]string{"/etc/hostcfg", "/root/.cfg"} {
		t.Errorf("copies = %v", r.copies)
	}
}

func TestUnknownSetupHandlerErrors(t *testing.T) {
	err := Run(context.Background(), newRec(), agent.Profile{Setup: "builtin:nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown setup handler") {
		t.Errorf("want unknown-handler error, got %v", err)
	}
}
