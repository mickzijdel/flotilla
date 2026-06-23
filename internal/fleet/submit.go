// internal/fleet/submit.go
package fleet

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/mickzijdel/flotilla/internal/forge"
	"github.com/mickzijdel/flotilla/internal/gitops"
)

// Submission is the outcome of `flotilla submit`.
type Submission struct {
	Agent    string `json:"agent"`
	Branch   string `json:"branch"`
	PRURL    string `json:"prURL"`
	Created  bool   `json:"created"`
	PushOnly bool   `json:"pushOnly"`
	Note     string `json:"note,omitempty"`
}

func (f *Fleet) workDir(name string) string {
	return filepath.Join(f.workRoot(), name)
}

// Submit pushes a finished agent's commits to flotilla/<name> and ensures a PR.
// It is status-gated on the process-exit done-signal (container exited) unless
// force is set, and strictly refuses a dirty tree or zero commits.
func (f *Fleet) Submit(ctx context.Context, name string, force bool) (Submission, error) {
	c, err := f.resolve(ctx, name)
	if err != nil {
		return Submission{}, err
	}
	if c.Status != "exited" && !force {
		return Submission{}, fmt.Errorf("agent %q is still running; wait for it to finish or pass --force", name)
	}

	dir := f.workDir(name)
	st, err := gitops.Inspect(ctx, dir)
	if err != nil {
		return Submission{}, err
	}
	if st.Dirty {
		return Submission{}, fmt.Errorf("agent %q has uncommitted changes; commit them inside the container first", name)
	}
	if st.CommitsAhead == 0 {
		return Submission{}, fmt.Errorf("nothing to submit: agent %q has no commits beyond %s", name, st.Base)
	}

	branch := "flotilla/" + name
	if err := gitops.Push(ctx, dir, branch); err != nil {
		return Submission{}, err
	}

	sub := Submission{Agent: name, Branch: branch}
	if f.Forge == nil {
		cmp, _ := forge.CompareURL(st.RemoteURL, st.Base, branch)
		sub.PushOnly = true
		sub.PRURL = cmp
		return sub, nil
	}
	res, perr := f.Forge.EnsurePR(ctx, dir, branch, st)
	if perr != nil {
		// Push succeeded; PR automation didn't. Degrade to push-only, keep the reason.
		cmp, _ := forge.CompareURL(st.RemoteURL, st.Base, branch)
		sub.PushOnly = true
		sub.PRURL = cmp
		sub.Note = perr.Error()
		return sub, nil
	}
	sub.PRURL = res.URL
	sub.Created = res.Created
	sub.PushOnly = res.PushOnly
	return sub, nil
}
