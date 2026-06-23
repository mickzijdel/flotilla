// internal/forge/forge.go
// Package forge turns a pushed branch into a pull request via the gh CLI, with a
// push-only fallback (print a compare URL) when gh is unavailable or the remote
// is not GitHub. The Forge interface is faked in tests the way backend.Backend is.
package forge

import (
	"context"
	"fmt"
	"strings"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// PRResult is the outcome of ensuring a PR for a pushed branch.
type PRResult struct {
	URL      string // PR URL, or compare URL on fallback
	Created  bool   // true = a new PR was opened (false = existing PR, or push-only)
	PushOnly bool   // true = gh unavailable / non-GitHub; no PR opened
}

// Forge ensures a pull request exists for a pushed branch.
type Forge interface {
	Available(ctx context.Context) bool
	EnsurePR(ctx context.Context, dir, branch string, st gitops.WorkState) (PRResult, error)
}

// isGitHub reports whether the remote URL points at github.com.
func isGitHub(remoteURL string) bool { return strings.Contains(remoteURL, "github.com") }

// CompareURL builds the GitHub compare URL for opening a PR by hand, from either
// an https or scp-style (git@host:owner/repo) remote.
func CompareURL(remoteURL, base, branch string) (string, error) {
	h, err := httpsBase(remoteURL)
	if err != nil {
		return "", err
	}
	return h + "/compare/" + base + "..." + branch, nil
}

// httpsBase normalizes a git remote URL to https://host/owner/repo (no .git).
func httpsBase(remoteURL string) (string, error) {
	s := strings.TrimSuffix(remoteURL, ".git")
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		return s, nil
	case strings.HasPrefix(s, "ssh://git@"):
		rest := strings.TrimPrefix(s, "ssh://git@")
		host, path, ok := strings.Cut(rest, "/")
		if !ok {
			return "", fmt.Errorf("unrecognized remote %q", remoteURL)
		}
		return "https://" + host + "/" + path, nil
	case strings.HasPrefix(s, "git@"):
		rest := strings.TrimPrefix(s, "git@")
		host, path, ok := strings.Cut(rest, ":")
		if !ok {
			return "", fmt.Errorf("unrecognized remote %q", remoteURL)
		}
		return "https://" + host + "/" + path, nil
	default:
		return "", fmt.Errorf("unrecognized remote %q", remoteURL)
	}
}
