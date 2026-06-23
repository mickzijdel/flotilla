package gitops

import (
	"context"
	"testing"
)

func TestHeadSHA(t *testing.T) {
	dir := cloneWithCommits(t, 2) // helper from inspect_test.go: bare remote + clone + N commits
	sha, err := HeadSHA(context.Background(), dir)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("want 40-char sha, got %q (len %d)", sha, len(sha))
	}
	// Stable across calls.
	sha2, _ := HeadSHA(context.Background(), dir)
	if sha != sha2 {
		t.Fatalf("non-deterministic: %q vs %q", sha, sha2)
	}
}
