package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// agentEnvFile is where the injected secret env-file lands in the container.
const agentEnvFile = "/run/flotilla/agent.env"

// resolveEnv returns the subset of allowlisted keys present in the environment.
// Only named keys can enter the container — the allowlist is the boundary.
func resolveEnv(keys []string, look func(string) (string, bool)) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := look(k); ok {
			out[k] = v
		}
	}
	return out
}

// envFileContent renders KEY=VALUE lines, sorted for determinism.
func envFileContent(env map[string]string) []byte {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return []byte(b.String())
}

// launchWrapper cd's into the container's mounted workspace, sources the
// injected env-file, then execs the agent launch. The workspace is the single
// directory devcontainer mounts under /workspaces, resolved at run time so the
// agent operates on the repo regardless of its name.
func launchWrapper(launch string) []string {
	script := fmt.Sprintf(
		`cd "$(ls -d /workspaces/*/ 2>/dev/null | head -1)" 2>/dev/null; set -a; . %s 2>/dev/null; set +a; exec %s`,
		agentEnvFile, launch)
	return []string{"sh", "-c", script}
}

// defaultDevcontainerJSON is the bundled config used when a repo ships none.
func defaultDevcontainerJSON(baseImage string) []byte {
	return []byte(fmt.Sprintf("{\n  \"name\": \"flotilla-default\",\n  \"image\": %q,\n  \"overrideCommand\": true\n}\n", baseImage))
}

// hasDevcontainer reports whether dir already ships a devcontainer config.
func hasDevcontainer(dir string) bool {
	for _, p := range []string{
		filepath.Join(dir, ".devcontainer", "devcontainer.json"),
		filepath.Join(dir, ".devcontainer.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
