package fleet

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// homeForUser returns the container home directory for a run user.
func homeForUser(user string) string {
	if user == "" || user == "root" {
		return "/root"
	}
	return "/home/" + user
}

// agentEnvFile is where the injected secret env-file lands, under the run user's home.
func agentEnvFile(home string) string {
	return path.Join(home, ".flotilla", "agent.env")
}

// runAsUser wraps a shell script to run as user via su; root/"" run it directly.
func runAsUser(user, script string) []string {
	if user == "" || user == "root" {
		return []string{"sh", "-c", script}
	}
	return []string{"su", user, "-c", script}
}

// launchScript cd's into the mounted workspace (the devcontainer's reported
// remoteWorkspaceFolder, or a /workspaces/* glob fallback), sets HOME, sources
// the injected env-file, then execs the agent launch.
func launchScript(launch, home, workdir string) string {
	cd := `cd "$(ls -d /workspaces/*/ 2>/dev/null | head -1)" 2>/dev/null`
	if workdir != "" {
		cd = fmt.Sprintf("cd '%s' 2>/dev/null", workdir)
	}
	return fmt.Sprintf(
		`%s; export HOME=%s; set -a; . %s 2>/dev/null; set +a; exec %s`,
		cd, home, agentEnvFile(home), launch)
}

// defaultDevcontainerJSON is the bundled config used when a repo ships none. It
// pins a non-root remoteUser so the agent does not run as root.
func defaultDevcontainerJSON(baseImage string) []byte {
	return []byte(fmt.Sprintf("{\n  \"name\": \"flotilla-default\",\n  \"image\": %q,\n  \"overrideCommand\": true,\n  \"remoteUser\": \"ubuntu\"\n}\n", baseImage))
}

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
