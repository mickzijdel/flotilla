// internal/forge/fake.go
package forge

import (
	"context"

	"github.com/mickzijdel/flotilla/internal/gitops"
)

// Fake is an in-memory Forge for unit tests (this package and internal/fleet).
type Fake struct {
	Result        PRResult
	Err           error
	AvailableFlag bool
	Calls         []string // branches passed to EnsurePR
}

func (f *Fake) Available(context.Context) bool { return f.AvailableFlag }

func (f *Fake) EnsurePR(_ context.Context, _, branch string, _ gitops.WorkState) (PRResult, error) {
	f.Calls = append(f.Calls, branch)
	return f.Result, f.Err
}
