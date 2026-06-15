package preflight

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestCheckAllPresent(t *testing.T) {
	d := Deps{
		Look:   func(string) (string, error) { return "/usr/bin/x", nil },
		Daemon: func(context.Context) error { return nil },
	}
	r := Check(context.Background(), d)
	if !r.OK() {
		t.Fatalf("want OK, got %+v", r)
	}
}

func TestCheckMissingDevcontainer(t *testing.T) {
	d := Deps{
		Look: func(name string) (string, error) {
			if name == "devcontainer" {
				return "", exec.ErrNotFound
			}
			return "/usr/bin/" + name, nil
		},
		Daemon: func(context.Context) error { return nil },
	}
	r := Check(context.Background(), d)
	if r.OK() {
		t.Fatal("want not OK when devcontainer missing")
	}
	if !strings.Contains(strings.Join(r.Messages(), "\n"), "devcontainers/cli") {
		t.Errorf("messages should hint the install command: %v", r.Messages())
	}
}

func TestCheckDaemonDown(t *testing.T) {
	d := Deps{
		Look:   func(string) (string, error) { return "/usr/bin/x", nil },
		Daemon: func(context.Context) error { return errors.New("cannot connect") },
	}
	if Check(context.Background(), d).DockerDaemon {
		t.Error("DockerDaemon should be false when daemon errors")
	}
}
