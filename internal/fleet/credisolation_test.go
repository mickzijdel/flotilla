package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/backend"
)

// gitCredMarkers must never appear in anything Spawn sends into the container
// (env values/keys, exec args, mount/copy paths). The agent's own token may
// enter — only git/GitHub credentials are forbidden.
var gitCredMarkers = []string{
	"github_token", "gh_token", "github_pat", "git_askpass",
	".git-credentials", "/.config/gh", "/.ssh", "/.gitconfig",
	"credential.helper",
}

func TestSpawnInjectsNoGitCredentials(t *testing.T) {
	fake := backend.NewFake()
	builtins, err := agent.Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	f := &Fleet{Backend: fake, BaseImage: "ubuntu:24.04", WorkRoot: t.TempDir()}
	if _, err := f.Spawn(context.Background(), bareRepo(t), builtins["claude"], "do it"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Collect every string Spawn handed the backend.
	var blobs []string
	for _, up := range fake.UpCalls {
		blobs = append(blobs, up.WorkspaceFolder)
		for k, v := range up.Labels {
			blobs = append(blobs, k, v)
		}
		for feat := range up.AdditionalFeatures {
			blobs = append(blobs, feat)
		}
	}
	for _, call := range fake.ExecCalls {
		blobs = append(blobs, call...)
	}
	for _, call := range fake.DetachedCalls {
		blobs = append(blobs, call...)
	}
	for _, cp := range fake.CopyCalls {
		blobs = append(blobs, cp.HostPath, cp.DestPath)
		// Scan the content of engine-GENERATED injections (the env-file and any
		// generated config), which route through a "flotilla-inject-" temp file.
		// Copied USER config is non-secret prose and is intentionally not scanned.
		if strings.Contains(cp.HostPath, "flotilla-inject-") {
			blobs = append(blobs, string(cp.Content))
		}
	}

	hay := strings.ToLower(strings.Join(blobs, "\n"))
	for _, m := range gitCredMarkers {
		if strings.Contains(hay, m) {
			t.Errorf("git credential marker %q leaked into container inputs", m)
		}
	}

	// Positive control: the only host path that enters is the engine clone.
	if len(fake.UpCalls) == 0 {
		t.Fatal("expected an Up call")
	}
	for _, up := range fake.UpCalls {
		if !strings.HasPrefix(up.WorkspaceFolder, f.WorkRoot) {
			t.Errorf("WorkspaceFolder %q is not under the engine work root %q", up.WorkspaceFolder, f.WorkRoot)
		}
	}
}
