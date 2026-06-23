package gitops

import "context"

// Fetch updates origin's remote-tracking refs in the engine-side clone. It is
// read/write-neutral on the working tree: it writes only refs/remotes/origin/*,
// FETCH_HEAD, and new objects — never the index, HEAD, or any local branch — so
// it is safe to run while the agent has uncommitted work. The engine holds the
// credentials; the container never does. --prune drops refs deleted upstream.
func Fetch(ctx context.Context, dir string) error {
	_, err := git(ctx, dir, "fetch", "--prune", "origin")
	return err
}
