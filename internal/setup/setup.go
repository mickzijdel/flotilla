// Package setup assembles an agent's in-container config home. Built-in handlers
// do smart assembly for first-class agents; declarative uses config_mounts only.
// Handlers never inject secrets — the agent token arrives via the env-file (fleet).
package setup

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mickzijdel/flotilla/internal/agent"
)

// Injector is how a handler assembles config inside the container. WriteFile and
// CopyTo route file content through `docker cp` (never via argv).
type Injector interface {
	Exec(ctx context.Context, cmd []string) error
	CopyTo(ctx context.Context, hostPath, destPath string) error
	WriteFile(ctx context.Context, content []byte, destPath string) error
}

// Handler assembles a specific agent's config home.
type Handler func(ctx context.Context, inj Injector, prof agent.Profile, home string) error

var registry = map[string]Handler{
	"builtin:claude": claudeSetup,
	"builtin:codex":  codexSetup,
}

// Run dispatches to the profile's setup handler. "" or "declarative" copies
// config_mounts only.
func Run(ctx context.Context, inj Injector, prof agent.Profile, home string) error {
	switch prof.Setup {
	case "", "declarative":
		return declarative(ctx, inj, prof)
	default:
		h, ok := registry[prof.Setup]
		if !ok {
			return fmt.Errorf("unknown setup handler %q", prof.Setup)
		}
		return h(ctx, inj, prof, home)
	}
}

func declarative(ctx context.Context, inj Injector, prof agent.Profile) error {
	for _, m := range prof.ConfigMounts {
		host, dest, ok := strings.Cut(m, ":")
		if !ok {
			return fmt.Errorf("invalid config_mount %q (want host:dest)", m)
		}
		if err := inj.CopyTo(ctx, expandHome(host), dest); err != nil {
			return err
		}
	}
	return nil
}

func claudeSetup(ctx context.Context, inj Injector, _ agent.Profile, home string) error {
	dir := filepath.Join(home, ".claude")
	if err := inj.Exec(ctx, []string{"mkdir", "-p", dir}); err != nil {
		return err
	}
	// Minimal settings + a Stop hook that commits anything the agent left
	// uncommitted (safety net behind the wrap_up prompt contract). `|| true`
	// keeps a no-op commit (nothing staged) from failing the hook.
	settings := `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "git add -A && (git diff --cached --quiet || git commit -m 'flotilla: wrap-up commit') || true" }
        ]
      }
    ]
  }
}
`
	if err := inj.WriteFile(ctx, []byte(settings), filepath.Join(dir, "settings.json")); err != nil {
		return err
	}
	// Carry the global CLAUDE.md if the host has one.
	if md := expandHome("~/.claude/CLAUDE.md"); fileExists(md) {
		if err := inj.CopyTo(ctx, md, filepath.Join(dir, "CLAUDE.md")); err != nil {
			return err
		}
	}
	return nil
}

func codexSetup(ctx context.Context, inj Injector, _ agent.Profile, home string) error {
	dir := filepath.Join(home, ".codex")
	if err := inj.Exec(ctx, []string{"mkdir", "-p", dir}); err != nil {
		return err
	}
	if err := inj.WriteFile(ctx, []byte("# flotilla-managed codex config\n"), filepath.Join(dir, "config.toml")); err != nil {
		return err
	}
	// Carry an existing OAuth auth.json if present; otherwise OPENAI_API_KEY (env) is used.
	if auth := expandHome("~/.codex/auth.json"); fileExists(auth) {
		if err := inj.CopyTo(ctx, auth, filepath.Join(dir, "auth.json")); err != nil {
			return err
		}
	}
	return nil
}
