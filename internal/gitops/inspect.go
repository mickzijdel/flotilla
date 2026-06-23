package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// WorkState describes an agent's clone relative to its push base.
type WorkState struct {
	Base         string // base branch from origin/HEAD (e.g. "main")
	CommitsAhead int    // commits on HEAD not on origin/<Base>
	Dirty        bool   // uncommitted changes (tracked or untracked)
	RemoteURL    string // origin URL
}

// git runs a read/write-neutral git command against dir. It always scopes
// safe.directory to dir so container-written .git files don't trip Git's
// dubious-ownership guard. Returns trimmed stdout; wraps stderr on failure.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "safe.directory=" + dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// gitClean reports whether `git <args>` exited 0 (clean). Exit code 1 means
// "differences" (not clean); any other error is returned.
func gitClean(ctx context.Context, dir string, args ...string) (bool, error) {
	full := append([]string{"-C", dir, "-c", "safe.directory=" + dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

// Inspect reports the clone's base branch, commits ahead of origin/<base>, and
// whether the tree is dirty. Read-only: never writes .git.
func Inspect(ctx context.Context, dir string) (WorkState, error) {
	var st WorkState

	url, err := git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return st, err
	}
	st.RemoteURL = url

	if ref, err := git(ctx, dir, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		st.Base = strings.TrimPrefix(ref, "refs/remotes/origin/")
	} else {
		b, err2 := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
		if err2 != nil {
			return st, err2
		}
		st.Base = b
	}

	cnt, err := git(ctx, dir, "rev-list", "--count", "origin/"+st.Base+"..HEAD")
	if err != nil {
		return st, err
	}
	n, err := strconv.Atoi(cnt)
	if err != nil {
		return st, fmt.Errorf("parse commit count %q: %w", cnt, err)
	}
	st.CommitsAhead = n

	dirty, err := isDirty(ctx, dir)
	if err != nil {
		return st, err
	}
	st.Dirty = dirty
	return st, nil
}

// isDirty is true if there are staged changes, unstaged changes, or untracked
// (non-ignored) files. Uses only read-only plumbing.
func isDirty(ctx context.Context, dir string) (bool, error) {
	unstagedClean, err := gitClean(ctx, dir, "diff", "--quiet")
	if err != nil {
		return false, err
	}
	stagedClean, err := gitClean(ctx, dir, "diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	untracked, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return false, err
	}
	return !unstagedClean || !stagedClean || untracked != "", nil
}
