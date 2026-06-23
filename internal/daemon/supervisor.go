package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

// fleetAPI is the slice of *fleet.Fleet the supervisor reacts with.
type fleetAPI interface {
	Submit(ctx context.Context, name string, force bool) (fleet.Submission, error)
	HeadSHA(ctx context.Context, name string) (string, error)
	List(ctx context.Context) ([]fleet.Agent, error)
}

// Supervisor reacts to agent done-signals: it auto-submits and records events.
type Supervisor struct {
	Fleet   fleetAPI
	Backend backend.Backend // for the secondary die/stop event trigger (may be nil)
	Paths   Paths
	Now     func() time.Time // injectable clock (tests); nil ⇒ time.Now
}

func (s *Supervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Supervisor) emit(name, typ, msg string, data map[string]any) {
	_ = AppendEvent(s.Paths.Inbox(), InboxEvent{
		TS: s.now(), Agent: name, Type: typ, Message: msg, Data: data,
	})
}

// handle reacts to a done-signal for agent name: dedup by SHA, then force-submit,
// always recording an agent_done event plus the submit outcome. Best-effort: all
// failures are surfaced as inbox events, never returned.
func (s *Supervisor) handle(ctx context.Context, name string) {
	sha, _ := s.Fleet.HeadSHA(ctx, name) // "" on error → still handled once
	rec, _ := s.Paths.LoadAgent(name)

	// Dedup: same HEAD already handled (covers per-tick rescans and restarts).
	if sha != "" && sha == rec.LastHandledSHA {
		return
	}

	rec.Name = name
	rec.LastStatus = "done"
	rec.LastEventTS = s.now()

	s.emit(name, EventAgentDone, "agent finished", nil)

	sub, err := s.Fleet.Submit(ctx, name, true)
	if err != nil {
		s.emit(name, EventSubmitSkipped, err.Error(), nil)
		rec.LastHandledSHA = sha
		_ = s.Paths.SaveAgent(rec)
		return
	}

	typ, msg := EventPRUpdated, "updated existing PR"
	if sub.Created || sub.PushOnly {
		typ, msg = EventPROpened, "opened PR"
	}
	data := map[string]any{"branch": sub.Branch, "prURL": sub.PRURL}
	if strings.TrimSpace(sub.Note) != "" {
		data["note"] = sub.Note
	}
	s.emit(name, typ, msg, data)

	rec.LastHandledSHA = sha
	rec.LastSubmittedSHA = sha
	_ = s.Paths.SaveAgent(rec)
}

// statusOf reads the launch-wrapper status file in an agent's log dir.
func statusOf(logDir string) string {
	if logDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(logDir, "status"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// scanOnce handles every agent whose status file reads "done", and dispatches
// any pending agent→daemon requests through the registered handlers.
func (s *Supervisor) scanOnce(ctx context.Context) {
	agents, err := s.Fleet.List(ctx)
	if err != nil {
		return
	}
	for _, a := range agents {
		if statusOf(a.LogDir) == "done" {
			s.handle(ctx, a.Name)
		}
	}
}

// handleEvent reacts to a die/stop container event as a done-signal fallback.
func (s *Supervisor) handleEvent(ctx context.Context, ev backend.Event) {
	name := ev.Labels[backend.LabelAgent]
	if name == "" {
		return
	}
	switch ev.Type {
	case "die", "stop":
		s.handle(ctx, name)
	}
}

// Run scans on startup, then ticks every interval (re-scanning, draining the
// Backend event stream, and re-checking the binary version) until ctx is
// cancelled.
func (s *Supervisor) Run(ctx context.Context, interval time.Duration) error {
	s.scanOnce(ctx) // catch agents that finished while the daemon was down

	var events <-chan backend.Event
	if s.Backend != nil {
		if ch, err := s.Backend.Events(ctx); err == nil {
			events = ch
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.scanOnce(ctx)
		case ev, ok := <-events:
			if !ok {
				events = nil // stream closed; keep ticking on status files
				continue
			}
			s.handleEvent(ctx, ev)
		}
	}
}
