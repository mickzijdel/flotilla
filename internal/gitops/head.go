package gitops

import "context"

// HeadSHA returns the full commit SHA of HEAD in dir. Read-only.
func HeadSHA(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "rev-parse", "HEAD")
}
