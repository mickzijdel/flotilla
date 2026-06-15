package egress

import (
	"reflect"
	"testing"
)

func TestComposeUnionsDedupesSorts(t *testing.T) {
	got := Compose([]string{"github.com", "npmjs.org"}, []string{"api.anthropic.com"}, []string{"github.com", "extra.example"})
	want := []string{"api.anthropic.com", "extra.example", "github.com", "npmjs.org"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose = %v, want %v", got, want)
	}
}

func TestComposeDropsEmpty(t *testing.T) {
	got := Compose([]string{"github.com", ""}, nil, []string{"  "})
	want := []string{"github.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose = %v, want %v", got, want)
	}
}

func TestBakedAllowlistHasEssentials(t *testing.T) {
	baked := BakedAllowlist()
	for _, must := range []string{"github.com", "registry.npmjs.org", "pypi.org", "ghcr.io"} {
		found := false
		for _, d := range baked {
			if d == must {
				found = true
			}
		}
		if !found {
			t.Errorf("baked allowlist missing %q", must)
		}
	}
}
