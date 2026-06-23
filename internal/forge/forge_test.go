// internal/forge/forge_test.go
package forge

import (
	"context"
	"testing"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

func TestCompareURLFromHTTPSAndSSH(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo.git": "https://github.com/owner/repo/compare/main...flotilla/atlas",
		"https://github.com/owner/repo":     "https://github.com/owner/repo/compare/main...flotilla/atlas",
		"git@github.com:owner/repo.git":     "https://github.com/owner/repo/compare/main...flotilla/atlas",
	}
	for remote, want := range cases {
		got, err := CompareURL(remote, "main", "flotilla/atlas")
		if err != nil {
			t.Fatalf("CompareURL(%q): %v", remote, err)
		}
		if got != want {
			t.Errorf("CompareURL(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestCompareURLRejectsUnknownRemote(t *testing.T) {
	if _, err := CompareURL("file:///tmp/x", "main", "b"); err == nil {
		t.Error("expected error for non-GitHub-style remote")
	}
}

func TestIsGitHub(t *testing.T) {
	if !isGitHub("git@github.com:o/r.git") {
		t.Error("git@github.com should be GitHub")
	}
	if isGitHub("https://gitlab.com/o/r.git") {
		t.Error("gitlab should not be GitHub")
	}
}

func TestFakeForgeReturnsConfiguredResult(t *testing.T) {
	f := &Fake{Result: PRResult{URL: "https://x/pr/1", Created: true}, AvailableFlag: true}
	got, err := f.EnsurePR(context.Background(), "/tmp/dir", "flotilla/atlas", gitops.WorkState{})
	if err != nil {
		t.Fatalf("EnsurePR: %v", err)
	}
	if !got.Created || got.URL != "https://x/pr/1" {
		t.Errorf("got %+v", got)
	}
	if len(f.Calls) != 1 || f.Calls[0] != "flotilla/atlas" {
		t.Errorf("Calls = %v, want [flotilla/atlas]", f.Calls)
	}
}
