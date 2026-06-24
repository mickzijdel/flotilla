package agent

import (
	"strings"
	"testing"
)

func TestWrapUpTextDefaultsAndDisable(t *testing.T) {
	if (Profile{}).WrapUpText() != DefaultWrapUp {
		t.Error("empty WrapUp should fall back to DefaultWrapUp")
	}
	if (Profile{WrapUp: "custom"}).WrapUpText() != "custom" {
		t.Error("explicit WrapUp should win")
	}
	if (Profile{WrapUp: "-"}).WrapUpText() != "" {
		t.Error("'-' sentinel should disable the wrap-up contract")
	}
	if (Profile{WrapUp: "   "}).WrapUpText() != DefaultWrapUp {
		t.Error("whitespace-only WrapUp should fall back to default, not be dropped")
	}
	if (Profile{WrapUp: "  -  "}).WrapUpText() != "" {
		t.Error("'-' with surrounding whitespace should still disable")
	}
}

func TestPromptWithWrapUpAppendsDelimitedBlock(t *testing.T) {
	got := PromptWithWrapUp("do the thing", DefaultWrapUp)
	if !strings.HasPrefix(got, "do the thing") {
		t.Error("user prompt must come first")
	}
	if !strings.Contains(got, "commit") {
		t.Error("wrap-up contract should mention committing")
	}
	if !strings.Contains(got, "[Flotilla submission contract]") {
		t.Error("contract should be appended under its delimiter header")
	}
	if PromptWithWrapUp("just this", "") != "just this" {
		t.Error("empty wrap-up should leave the prompt unchanged")
	}
}

func TestPromptWithAskHintAppendsBlock(t *testing.T) {
	got := PromptWithAskHint("do the task")
	if !strings.HasPrefix(got, "do the task") {
		t.Errorf("user prompt must come first: %q", got)
	}
	if !strings.Contains(got, "flotilla-ask") {
		t.Errorf("ask hint missing the command name: %q", got)
	}
	if !strings.Contains(got, "[Flotilla ask-the-operator]") {
		t.Errorf("ask hint block marker missing: %q", got)
	}
}

func TestPromptWithFetchHintAppendsBlock(t *testing.T) {
	got := PromptWithFetchHint("do the task")
	if !strings.HasPrefix(got, "do the task") {
		t.Errorf("user prompt must come first: %q", got)
	}
	if !strings.Contains(got, "flotilla-fetch") {
		t.Errorf("fetch hint missing the command name: %q", got)
	}
	if !strings.Contains(got, "[Flotilla on-demand fetch]") {
		t.Errorf("fetch hint block marker missing: %q", got)
	}
}
