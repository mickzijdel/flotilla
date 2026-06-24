package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

func TestInstallAskShimCopiesAndChmods(t *testing.T) {
	fake := backend.NewFake()
	res, err := fake.Up(context.Background(), backend.UpOpts{Labels: map[string]string{backend.LabelAgent: "otter"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := installAskShim(context.Background(), fake, res.ID); err != nil {
		t.Fatalf("installAskShim: %v", err)
	}

	var copied *backend.CopyCall
	for i := range fake.CopyCalls {
		if fake.CopyCalls[i].DestPath == askShimPath {
			copied = &fake.CopyCalls[i]
			break
		}
	}
	if copied == nil {
		t.Fatalf("shim not copied to %s; copies=%v", askShimPath, fake.CopyCalls)
	}
	if !strings.Contains(string(copied.Content), "flotilla-ask") {
		t.Errorf("shim content missing marker; got %q", copied.Content)
	}

	var chmodded bool
	for _, c := range fake.ExecCalls {
		if len(c) >= 3 && c[1] == "chmod" && c[len(c)-1] == askShimPath {
			chmodded = true
		}
	}
	if !chmodded {
		t.Errorf("shim not chmod'd executable; execs=%v", fake.ExecCalls)
	}
}

// TestAskShimTargetsSessionMount guards the shim's hard-coded /flotilla/session
// against drift from containerSessionDir.
func TestAskShimTargetsSessionMount(t *testing.T) {
	if !strings.Contains(askShim, containerSessionDir) {
		t.Fatalf("shim must reference the session mount %q", containerSessionDir)
	}
}

// TestSpawnInstallsAskShim proves Spawn wires installAskShim in.
func TestSpawnInstallsAskShim(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do it"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	var found bool
	for _, cp := range fake.CopyCalls {
		if cp.DestPath == askShimPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("Spawn did not install the ask shim")
	}
}
