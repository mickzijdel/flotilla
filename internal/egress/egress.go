// Package egress builds the per-agent egress allowlist and the squid proxy
// config that enforces it (default-deny, allow only listed hostnames).
package egress

import (
	"fmt"
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

// SquidConf renders a squid config that default-denies egress and allows HTTP(S)
// only to the allowlisted hostnames (as dstdomain suffixes, so api.x.com matches
// .x.com). CONNECT is restricted to 443. Caching is off.
func SquidConf(allowlist []string, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "http_port %d\n", port)
	b.WriteString("acl SSL_ports port 443\n")
	b.WriteString("acl CONNECT method CONNECT\n")
	b.WriteString("http_access deny CONNECT !SSL_ports\n")
	if doms := minimalDomains(allowlist); len(doms) > 0 {
		b.WriteString("acl allowed dstdomain")
		for _, d := range doms {
			fmt.Fprintf(&b, " .%s", d)
		}
		b.WriteString("\nhttp_access allow allowed\n")
	}
	b.WriteString("http_access deny all\n")
	b.WriteString("cache deny all\n")
	return b.String()
}

// minimalDomains normalizes hostnames (strips a leading dot, dedupes) and drops
// any domain already covered by a broader entry — a squid dstdomain ACL treats
// ".github.com" as covering all of *.github.com, and FATALs if the list also
// names a subdomain like "api.github.com". The result is sorted for determinism.
func minimalDomains(allowlist []string) []string {
	set := map[string]bool{}
	for _, d := range allowlist {
		if d = strings.TrimPrefix(strings.TrimSpace(d), "."); d != "" {
			set[d] = true
		}
	}
	var out []string
	for d := range set {
		covered := false
		for parent := range set {
			if parent != d && strings.HasSuffix(d, "."+parent) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
