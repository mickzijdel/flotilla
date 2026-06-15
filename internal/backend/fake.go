package backend

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// CopyCall records a CopyTo for assertions (Content is read from HostPath).
type CopyCall struct {
	ID, HostPath, DestPath string
	Content                []byte
}

// Fake is an in-memory Backend for unit tests.
type Fake struct {
	mu    sync.Mutex
	seq   int
	items map[string]*Container
	// ExecCalls records (id, cmd) for assertions.
	ExecCalls     [][]string
	UpCalls       []UpOpts
	DetachedCalls [][]string
	CopyCalls     []CopyCall
}

func NewFake() *Fake { return &Fake{items: map[string]*Container{}} }

func (f *Fake) Create(_ context.Context, o CreateOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("fake-%d", f.seq)
	f.items[id] = &Container{
		ID:      id,
		Name:    o.Labels[LabelAgent],
		Repo:    o.Labels[LabelRepo],
		Status:  "created",
		Created: time.Unix(int64(f.seq), 0).UTC(),
		Labels:  o.Labels,
	}
	return id, nil
}

func (f *Fake) Start(_ context.Context, id string) error { return f.setStatus(id, "running") }
func (f *Fake) Stop(_ context.Context, id string) error  { return f.setStatus(id, "exited") }

func (f *Fake) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return fmt.Errorf("no such container %q", id)
	}
	delete(f.items, id)
	return nil
}

func (f *Fake) Exec(_ context.Context, id string, cmd []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ExecCalls = append(f.ExecCalls, append([]string{id}, cmd...))
	return nil
}

func (f *Fake) List(_ context.Context, filter map[string]string) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Container
	for _, c := range f.items {
		if matches(c.Labels, filter) {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *Fake) AttachInfo(_ context.Context, id string) (AttachInfo, error) {
	return AttachInfo{
		ContainerID: id,
		DockerExec:  "docker exec -it " + id + " bash",
		VSCode:      "Dev Containers: Attach to Running Container -> " + id,
	}, nil
}

func (f *Fake) setStatus(id, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.items[id]
	if !ok {
		return fmt.Errorf("no such container %q", id)
	}
	c.Status = status
	return nil
}

func matches(labels, filter map[string]string) bool {
	for k, v := range filter {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (f *Fake) Up(_ context.Context, o UpOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UpCalls = append(f.UpCalls, o)
	f.seq++
	id := fmt.Sprintf("fake-%d", f.seq)
	f.items[id] = &Container{
		ID:      id,
		Name:    o.Labels[LabelAgent],
		Repo:    o.Labels[LabelRepo],
		Status:  "running",
		Created: time.Unix(int64(f.seq), 0).UTC(),
		Labels:  o.Labels,
	}
	return id, nil
}

func (f *Fake) ExecDetached(_ context.Context, id string, cmd []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DetachedCalls = append(f.DetachedCalls, append([]string{id}, cmd...))
	return nil
}

func (f *Fake) CopyTo(_ context.Context, id, hostPath, destPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, _ := os.ReadFile(hostPath) // best-effort, for test assertions
	f.CopyCalls = append(f.CopyCalls, CopyCall{ID: id, HostPath: hostPath, DestPath: destPath, Content: content})
	return nil
}
