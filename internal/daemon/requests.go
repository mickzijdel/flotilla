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
	Status  string         `json:"status"` // "ok" | "error"
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

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

// dispatchRequests scans <sessionDir>/requests/*.json and answers any whose
// <sessionDir>/responses/<id>.json does not yet exist. Best-effort + idempotent.
func dispatchRequests(ctx context.Context, reg *Registry, agent, sessionDir string) {
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
		respPath := filepath.Join(respDir, id+".json")
		if _, err := os.Stat(respPath); err == nil {
			continue // already answered
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
		resp := reg.dispatch(ctx, agent, req)
		rb, _ := json.Marshal(resp)
		if err := os.MkdirAll(respDir, 0o777); err != nil {
			continue
		}
		_ = atomicWrite(respPath, rb, 0o644)
	}
}
