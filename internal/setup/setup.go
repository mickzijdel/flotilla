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
type Handler func(ctx context.Context, inj Injector, prof agent.Profile) error

var registry = map[string]Handler{
	"builtin:claude": claudeSetup,
	"builtin:codex":  codexSetup,
}

// Run dispatches to the profile's setup handler. "" or "declarative" copies
// config_mounts only.
func Run(ctx context.Context, inj Injector, prof agent.Profile) error {
	switch prof.Setup {
	case "", "declarative":
		return declarative(ctx, inj, prof)
	default:
		h, ok := registry[prof.Setup]
		if !ok {
			return fmt.Errorf("unknown setup handler %q", prof.Setup)
		}
		return h(ctx, inj, prof)
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

func claudeSetup(ctx context.Context, inj Injector, _ agent.Profile) error {
	const home = "/root/.claude"
	if err := inj.Exec(ctx, []string{"mkdir", "-p", home}); err != nil {
		return err
	}
	// Minimal, container-safe settings; auth is the headless token (injected by fleet).
	// Task 1's spike may extend this with keys headless Claude needs.
	if err := inj.WriteFile(ctx, []byte("{}\n"), filepath.Join(home, "settings.json")); err != nil {
		return err
	}
	// Carry the global CLAUDE.md if the host has one.
	if md := expandHome("~/.claude/CLAUDE.md"); fileExists(md) {
		if err := inj.CopyTo(ctx, md, filepath.Join(home, "CLAUDE.md")); err != nil {
			return err
		}
	}
	return nil
}

func codexSetup(ctx context.Context, inj Injector, _ agent.Profile) error {
	const home = "/root/.codex"
	if err := inj.Exec(ctx, []string{"mkdir", "-p", home}); err != nil {
		return err
	}
	if err := inj.WriteFile(ctx, []byte("# flotilla-managed codex config\n"), filepath.Join(home, "config.toml")); err != nil {
		return err
	}
	// Carry an existing OAuth auth.json if present; otherwise OPENAI_API_KEY (env) is used.
	if auth := expandHome("~/.codex/auth.json"); fileExists(auth) {
		if err := inj.CopyTo(ctx, auth, filepath.Join(home, "auth.json")); err != nil {
			return err
		}
	}
	return nil
}
