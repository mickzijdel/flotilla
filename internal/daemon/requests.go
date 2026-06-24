package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Request is an agent→daemon control message (filesystem-mediated).
type Request struct {
	ID   string         `json:"id"`
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// Response is the daemon's reply to a Request.
type Response struct {
	Status  string         `json:"status"` // "ok" | "error" | "deferred"
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// StatusDeferred is the non-terminal sentinel a handler returns when it has
// notified an operator and is waiting for an out-of-band response (written by
// e.g. `flotilla answer`). The dispatch loop writes NO response file for it and
// does not re-dispatch the request until that out-of-band response appears.
const StatusDeferred = "deferred"

// Handler reacts to one request type for a given agent.
type Handler func(ctx context.Context, agent string, req Request) Response

// Registry maps request types to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry { return &Registry{handlers: map[string]Handler{}} }

// Register binds a handler to a request type.
func (r *Registry) Register(typ string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[typ] = h
}

func (r *Registry) dispatch(ctx context.Context, agent string, req Request) Response {
	r.mu.RLock()
	h, ok := r.handlers[req.Type]
	r.mu.RUnlock()
	if !ok {
		return Response{Status: "error", Message: fmt.Sprintf("unknown request type %q", req.Type)}
	}
	return h(ctx, agent, req)
}

// deferKey identifies a deferred (still-pending) request across scans.
func deferKey(agent, id string) string { return agent + "\x00" + id }

// dispatchRequests scans <sessionDir>/requests/*.json and answers any whose
// <sessionDir>/responses/<id>.json does not yet exist. Best-effort + idempotent.
//
// Terminal handlers (ok/error) get their Response written to responses/<id>.json
// as before. A handler that returns StatusDeferred is non-terminal: NO response
// is written and (agent,id) is remembered so the request is dispatched only once
// — until a response appears out-of-band (e.g. `flotilla answer`), at which point
// the answered transition is observed via onAnswered exactly once.
func (s *Supervisor) dispatchRequests(ctx context.Context, agent, sessionDir string) {
	if s.Registry == nil {
		return
	}
	if s.deferred == nil {
		s.deferred = map[string]string{}
	}
	reqDir := filepath.Join(sessionDir, "requests")
	respDir := filepath.Join(sessionDir, "responses")
	entries, err := os.ReadDir(reqDir)
	if err != nil {
		return // no requests dir yet
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		key := deferKey(agent, id)
		respPath := filepath.Join(respDir, id+".json")
		if _, err := os.Stat(respPath); err == nil {
			// Already answered. If we were waiting on it (deferred), the response
			// just arrived out-of-band: observe the transition once, then forget.
			if typ, ok := s.deferred[key]; ok {
				s.onAnswered(agent, id, typ, respPath)
				delete(s.deferred, key)
			}
			continue
		}
		if _, ok := s.deferred[key]; ok {
			continue // pending deferred (e.g. a question awaiting the operator) — notified once
		}
		b, err := os.ReadFile(filepath.Join(reqDir, e.Name()))
		if err != nil {
			continue
		}
		var req Request
		if err := json.Unmarshal(b, &req); err != nil {
			continue
		}
		if req.ID == "" {
			req.ID = id
		}
		resp := s.Registry.dispatch(ctx, agent, req)
		if resp.Status == StatusDeferred {
			s.deferred[key] = req.Type // non-terminal: write no response, wait for the out-of-band reply
			continue
		}
		rb, _ := json.Marshal(resp)
		if err := os.MkdirAll(respDir, 0o777); err != nil {
			continue
		}
		_ = atomicWrite(respPath, rb, 0o644)
	}
}
