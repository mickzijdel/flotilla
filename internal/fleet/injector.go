package fleet

import (
	"context"
	"os"
	"path"
	"strings"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// injector adapts a Backend + container id to setup.Injector. File content is
// routed through the backend's CopyTo (`docker cp`), never via argv, and the
// destination's parent dir is created first.
type injector struct {
	be   backend.Backend
	id   string
	user string
}

func (j *injector) Exec(ctx context.Context, cmd []string) error {
	return j.be.Exec(ctx, j.id, runAsUser(j.user, strings.Join(cmd, " ")))
}

func (j *injector) CopyTo(ctx context.Context, hostPath, destPath string) error {
	if err := j.mkdirParent(ctx, destPath); err != nil {
		return err
	}
	return j.be.CopyTo(ctx, j.id, hostPath, destPath)
}

// WriteFile writes generated content to a 0600 host temp file and copies it in.
func (j *injector) WriteFile(ctx context.Context, content []byte, destPath string) error {
	tmp, err := os.CreateTemp("", "flotilla-inject-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := j.mkdirParent(ctx, destPath); err != nil {
		return err
	}
	return j.be.CopyTo(ctx, j.id, tmp.Name(), destPath)
}

func (j *injector) mkdirParent(ctx context.Context, destPath string) error {
	dir := path.Dir(destPath)
	if dir == "" || dir == "." || dir == "/" {
		return nil
	}
	return j.Exec(ctx, []string{"mkdir", "-p", dir})
}
