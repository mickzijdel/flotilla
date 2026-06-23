package gitops

import "context"

// Push creates/moves a local branch at HEAD and force-pushes it to origin using
// --force-with-lease, so re-submitting updates the branch in place without
// clobbering a ref someone else moved. The local branch matters: PR tooling such
// as `gh pr create --fill` resolves the head ref by name to compute the commit
// range, and a detached `HEAD:refs/heads/<branch>` push leaves no local ref for
// it to name. Reads local objects only; the engine holds the credentials, never
// the container.
func Push(ctx context.Context, dir, branch string) error {
	// -f so a re-submit moves the existing local branch to the new HEAD. The
	// clone is checked out on the base branch, never on <branch>, so this never
	// fails on "branch checked out".
	if _, err := git(ctx, dir, "branch", "-f", branch, "HEAD"); err != nil {
		return err
	}
	_, err := git(ctx, dir, "push", "--force-with-lease", "origin", branch)
	return err
}
