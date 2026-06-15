package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
