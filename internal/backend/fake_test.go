package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFakeEvents(t *testing.T) {
	f := NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := f.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	f.PushEvent(Event{Type: "die", ID: "fake-1", Labels: map[string]string{LabelAgent: "brave-otter"}})
	select {
	case e := <-ch:
		if e.Type != "die" || e.Labels[LabelAgent] != "brave-otter" {
			t.Fatalf("unexpected event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should close on ctx cancel")
	}
}

func TestFakeLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	id, err := f.Create(ctx, CreateOpts{
		Name:   "atlas",
		Image:  "ubuntu:24.04",
		Cmd:    []string{"sleep", "infinity"},
		Labels: map[string]string{LabelAgent: "atlas", LabelRepo: "git@x:r.git"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := f.List(ctx, map[string]string{LabelAgent: "atlas"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("List = %+v, want one running container", got)
	}
	if err := f.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got, _ = f.List(ctx, nil)
	if got[0].Status != "exited" {
		t.Errorf("after Stop status = %q, want exited", got[0].Status)
	}
	if err := f.Remove(ctx, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = f.List(ctx, nil)
	if len(got) != 0 {
		t.Errorf("after Remove List = %+v, want empty", got)
	}
}

func TestFakeUpRecordsOptsAndRuns(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	res, err := f.Up(ctx, UpOpts{
		Name:               "atlas",
		WorkspaceFolder:    "/work/atlas",
		AdditionalFeatures: map[string]any{"/feat/flotilla-toolchain": map[string]any{}},
		Labels:             map[string]string{LabelAgent: "atlas"},
	})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if res.ID == "" {
		t.Fatalf("Up returned empty id")
	}
	if len(f.UpCalls) != 1 || f.UpCalls[0].WorkspaceFolder != "/work/atlas" {
		t.Fatalf("UpCalls = %+v", f.UpCalls)
	}
	got, _ := f.List(ctx, map[string]string{LabelAgent: "atlas"})
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("List = %+v, want one running", got)
	}
}

func TestFakeExecDetachedAndCopyToRecord(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if err := f.ExecDetached(ctx, "fake-1", []string{"sh", "-c", "echo hi"}); err != nil {
		t.Fatalf("ExecDetached: %v", err)
	}
	if len(f.DetachedCalls) != 1 || f.DetachedCalls[0][0] != "fake-1" {
		t.Fatalf("DetachedCalls = %+v", f.DetachedCalls)
	}

	src := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(src, []byte("CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.CopyTo(ctx, "fake-1", src, "/dst/payload"); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if len(f.CopyCalls) != 1 {
		t.Fatalf("CopyCalls = %+v", f.CopyCalls)
	}
	cp := f.CopyCalls[0]
	if cp.DestPath != "/dst/payload" || string(cp.Content) != "CONTENT" {
		t.Errorf("CopyCall = %+v", cp)
	}
}

func TestFakeNetworkAndContainerNetworks(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if err := f.NetworkCreate(ctx, "flotilla-net-atlas", true); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	id, _ := f.Create(ctx, CreateOpts{Name: "p", Image: "ubuntu/squid", Network: "bridge", Labels: map[string]string{LabelAgent: "atlas"}})
	if err := f.NetworkConnect(ctx, "flotilla-net-atlas", id); err != nil {
		t.Fatalf("NetworkConnect: %v", err)
	}
	if err := f.NetworkDisconnect(ctx, "bridge", id); err != nil {
		t.Fatalf("NetworkDisconnect: %v", err)
	}
	nets, err := f.ContainerNetworks(ctx, id)
	if err != nil {
		t.Fatalf("ContainerNetworks: %v", err)
	}
	// started on "bridge" (from Create.Network), connected internal, disconnected bridge.
	if len(nets) != 1 || nets[0] != "flotilla-net-atlas" {
		t.Errorf("ContainerNetworks = %v, want [flotilla-net-atlas]", nets)
	}
	if err := f.NetworkRemove(ctx, "flotilla-net-atlas"); err != nil {
		t.Fatalf("NetworkRemove: %v", err)
	}
	if len(f.NetworkCreates) != 1 || len(f.NetworkRemoves) != 1 {
		t.Errorf("network calls not recorded: creates=%v removes=%v", f.NetworkCreates, f.NetworkRemoves)
	}
}
