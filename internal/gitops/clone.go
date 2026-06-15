package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Clone does a fresh engine-side clone of repoURL into dest. The engine holds
// the git credentials; the container never does.
func Clone(ctx context.Context, repoURL, dest string) error {
	var errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", repoURL, dest)
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w: %s", repoURL, err, errb.String())
	}
	return nil
}
