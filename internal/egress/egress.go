// Package egress builds the per-agent egress allowlist and the squid proxy
// config that enforces it (default-deny, allow only listed hostnames).
package egress

import (
	"sort"
	"strings"
)

// BakedAllowlist is the default set of hostnames a coding agent needs at run
// time. The agent API endpoint comes from the profile, not here.
func BakedAllowlist() []string {
	return []string{
		// GitHub (read-only; no creds in the box means it still cannot push)
		"github.com", "api.github.com", "codeload.github.com",
		"objects.githubusercontent.com", "raw.githubusercontent.com",
		// Package registries / toolchains
		"registry.npmjs.org", "pypi.org", "files.pythonhosted.org",
		"ghcr.io", "deb.nodesource.com", "mise.jdx.dev",
		"crates.io", "static.crates.io", "proxy.golang.org", "sum.golang.org",
	}
}

// Compose unions the three allowlist sources, dropping blanks, deduping, and
// sorting for a deterministic result.
func Compose(baked, profile, fleet []string) []string {
	set := map[string]bool{}
	for _, src := range [][]string{baked, profile, fleet} {
		for _, d := range src {
			d = strings.TrimSpace(d)
			if d != "" {
				set[d] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
