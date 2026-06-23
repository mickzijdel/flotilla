// internal/forge/gh.go
package forge

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// GH ensures PRs via the gh CLI.
type GH struct{}

// NewGH returns a gh-backed Forge.
func NewGH() *GH { return &GH{} }

// GHAvailable reports whether gh is installed and authenticated.
func GHAvailable(ctx context.Context) bool {
	if _, err := exec.LookPath("gh"); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "gh", "auth", "status").Run() == nil
}

func (g *GH) Available(ctx context.Context) bool { return GHAvailable(ctx) }

// EnsurePR opens a PR for branch via `gh pr create --fill`, or returns the
// existing PR if one is already open. Falls back to push-only (compare URL) when
// gh is unavailable, the remote is not GitHub, or pr-create fails — the branch
// is already pushed, so no work is ever lost.
func (g *GH) EnsurePR(ctx context.Context, dir, branch string, st gitops.WorkState) (PRResult, error) {
	cmp, _ := CompareURL(st.RemoteURL, st.Base, branch)
	if !isGitHub(st.RemoteURL) || !GHAvailable(ctx) {
		return PRResult{URL: cmp, PushOnly: true}, nil
	}
	if url := ghOut(ctx, dir, "pr", "view", branch, "--json", "url", "-q", ".url"); url != "" {
		return PRResult{URL: url, Created: false}, nil
	}
	url, err := ghOut2(ctx, dir, "pr", "create", "--fill", "--head", branch)
	if err != nil {
		return PRResult{URL: cmp, PushOnly: true}, fmt.Errorf("gh pr create: %w", err)
	}
	return PRResult{URL: strings.TrimSpace(url), Created: true}, nil
}

// ghOut runs gh in dir and returns trimmed stdout, or "" on any error.
func ghOut(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ghOut2 runs gh in dir, returning stdout and wrapping stderr on error.
func ghOut2(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
