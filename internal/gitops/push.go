package gitops

import "context"

// Push force-pushes the clone's HEAD to origin as refs/heads/<branch> using
// --force-with-lease, so re-submitting updates the branch in place without
// clobbering a ref someone else moved. Reads local objects only; the engine
// holds the credentials, never the container.
func Push(ctx context.Context, dir, branch string) error {
	_, err := git(ctx, dir, "push", "--force-with-lease", "origin", "HEAD:refs/heads/"+branch)
	return err
}
