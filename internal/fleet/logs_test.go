package fleet

import (
	"testing"
	"time"
)

func TestRepoSlug(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo.git": "owner-repo",
		"https://github.com/owner/repo":     "owner-repo",
		"git@github.com:owner/repo.git":     "owner-repo",
		"ssh://git@github.com/owner/repo":   "owner-repo",
		"":                                  "repo",
	}
	for in, want := range cases {
		if got := repoSlug(in); got != want {
			t.Errorf("repoSlug(%q) = %q, want %q", in, got, want)
		}
	}
	// Unsafe characters collapse to '-'.
	if got := repoSlug("https://h/o w/r$x"); got != "o-w-r-x" {
		t.Errorf("repoSlug(unsafe) = %q, want o-w-r-x", got)
	}
}

func TestSessionDirName(t *testing.T) {
	ts := time.Date(2026, 6, 23, 19, 5, 0, 0, time.UTC)
	if got := sessionDirName("atlas", ts); got != "2026-06-23-1905-atlas" {
		t.Errorf("sessionDirName = %q, want 2026-06-23-1905-atlas", got)
	}
}

func TestTranscriptTarget(t *testing.T) {
	if got := transcriptTarget("~/.claude/projects", "/home/ubuntu"); got != "/home/ubuntu/.claude/projects" {
		t.Errorf("transcriptTarget(~) = %q", got)
	}
	if got := transcriptTarget("~", "/home/ubuntu"); got != "/home/ubuntu" {
		t.Errorf("transcriptTarget(bare ~) = %q", got)
	}
	if got := transcriptTarget("/abs/path", "/home/ubuntu"); got != "/abs/path" {
		t.Errorf("transcriptTarget(abs) = %q", got)
	}
	if got := transcriptTarget("", "/home/ubuntu"); got != "" {
		t.Errorf("transcriptTarget(empty) = %q, want empty", got)
	}
}
