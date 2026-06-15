package naming

import (
	"strings"
	"testing"
)

func TestPickAvoidsTakenNames(t *testing.T) {
	taken := map[string]bool{}
	for _, w := range Words {
		taken[w] = true
	}
	delete(taken, "atlas") // leave exactly one free
	got := Pick(taken)
	if got != "atlas" {
		t.Errorf("Pick = %q, want the only free word 'atlas'", got)
	}
}

func TestPickPrefersUniqueFirstLetter(t *testing.T) {
	taken := map[string]bool{}
	// Take everything not starting with 'b'.
	for _, w := range Words {
		if !strings.HasPrefix(w, "b") {
			taken[w] = true
		}
	}
	got := Pick(taken)
	if !strings.HasPrefix(got, "b") {
		t.Errorf("Pick = %q, want a free 'b' word", got)
	}
}

func TestPickFallsBackToSuffixWhenAllTaken(t *testing.T) {
	taken := map[string]bool{}
	for _, w := range Words {
		taken[w] = true
	}
	got := Pick(taken)
	if !strings.Contains(got, "-") {
		t.Errorf("Pick = %q, want a suffixed fallback when all words taken", got)
	}
	if taken[got] {
		t.Errorf("Pick returned a taken name %q", got)
	}
}
