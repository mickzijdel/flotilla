package fleet

import (
	"context"
	"fmt"
	"os"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// Fetch asks the engine to re-fetch origin into a running agent's engine-side
// clone, so the agent (which holds no credentials) can pick up base-branch
// changes mid-session. Host-side only: no container is touched, and it never
// merges, rebases, or writes the working tree — the agent integrates locally.
// Mirrors Submit's host-side resolve+clone-check model.
func (f *Fleet) Fetch(ctx context.Context, name string) error {
	if _, err := f.resolve(ctx, name); err != nil {
		return err
	}
	dir := f.workDir(name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no workspace clone for agent %q at %s (was it removed?)", name, dir)
	}
	return gitops.Fetch(ctx, dir)
}
