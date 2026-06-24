package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

// TestSpawnInjectsWrapUpContractIntoPrompt exercises the real Spawn path and
// asserts that the injected prompt file (the CopyCall whose DestPath ends with
// ".flotilla/prompt") contains both the user prompt and the wrap-up contract.
// This proves that fleet.go wires agent.PromptWithWrapUp into the spawn path.
func TestSpawnInjectsWrapUpContractIntoPrompt(t *testing.T) {
	fake := backend.NewFake()
	builtins, err := agent.Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	if _, err := f.Spawn(context.Background(), bareRepo(t), builtins["claude"], "do the task"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Find the CopyCall for the agent prompt file (DestPath ends with ".flotilla/prompt").
	var promptContent string
	var found bool
	for _, cp := range fake.CopyCalls {
		if strings.HasSuffix(cp.DestPath, ".flotilla/prompt") {
			promptContent = string(cp.Content)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no CopyCall for the agent prompt file (DestPath ending in .flotilla/prompt)")
	}

	if !strings.Contains(promptContent, "do the task") {
		t.Errorf("injected prompt missing user prompt; got: %q", promptContent)
	}
	if !strings.Contains(promptContent, "commit") {
		t.Errorf("injected prompt missing wrap-up contract (expected 'commit'); got: %q", promptContent)
	}
}

// TestSpawnDisabledWrapUpOmitsContract ensures the "-" sentinel suppresses the
// wrap-up block from the injected prompt file entirely.
func TestSpawnDisabledWrapUpOmitsContract(t *testing.T) {
	fake := backend.NewFake()
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	prof := agent.Profile{Name: "stub", Launch: `echo "{prompt}"`, WrapUp: "-"}
	if _, err := f.Spawn(context.Background(), bareRepo(t), prof, "do the task"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	for _, cp := range fake.CopyCalls {
		if strings.HasSuffix(cp.DestPath, ".flotilla/prompt") {
			content := string(cp.Content)
			if strings.Contains(content, "Flotilla submission contract") {
				t.Errorf("wrap-up contract present despite '-' sentinel; got: %q", content)
			}
			if !strings.Contains(content, "do the task") {
				t.Errorf("user prompt dropped; got: %q", content)
			}
			// The fetch + ask hints are always appended (independent of the wrap-up
			// contract), so a disabled wrap-up no longer means an unchanged prompt.
			if !strings.Contains(content, "flotilla-fetch") {
				t.Errorf("fetch hint should always be present; got: %q", content)
			}
			if !strings.Contains(content, "flotilla-ask") {
				t.Errorf("ask hint should always be present; got: %q", content)
			}
			return
		}
	}
	t.Fatal("no CopyCall for the agent prompt file")
}
