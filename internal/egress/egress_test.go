package egress

import (
	"reflect"
	"strings"
	"testing"
)

func TestComposeUnionsDedupesSorts(t *testing.T) {
	got := Compose([]string{"github.com", "npmjs.org"}, []string{"api.anthropic.com"}, []string{"github.com", "extra.example"})
	want := []string{"api.anthropic.com", "extra.example", "github.com", "npmjs.org"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose = %v, want %v", got, want)
	}
}

func TestComposeDropsEmpty(t *testing.T) {
	got := Compose([]string{"github.com", ""}, nil, []string{"  "})
	want := []string{"github.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose = %v, want %v", got, want)
	}
}

func TestBakedAllowlistHasEssentials(t *testing.T) {
	baked := BakedAllowlist()
	for _, must := range []string{"github.com", "registry.npmjs.org", "pypi.org", "ghcr.io"} {
		found := false
		for _, d := range baked {
			if d == must {
				found = true
			}
		}
		if !found {
			t.Errorf("baked allowlist missing %q", must)
		}
	}
}

func TestSquidConfDefaultDenyAndAllowlist(t *testing.T) {
	conf := SquidConf([]string{"api.anthropic.com", "github.com"}, 3128)
	for _, must := range []string{
		"http_port 3128",
		"acl CONNECT method CONNECT",
		"http_access deny CONNECT !SSL_ports",
		"acl allowed dstdomain .api.anthropic.com .github.com",
		"http_access allow allowed",
		"http_access deny all",
		"cache deny all",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("squid.conf missing %q\n---\n%s", must, conf)
		}
	}
}

func TestSquidConfEmptyAllowlistStillDenies(t *testing.T) {
	conf := SquidConf(nil, 3128)
	if !strings.Contains(conf, "http_access deny all") {
		t.Errorf("empty allowlist must still default-deny:\n%s", conf)
	}
}

// squid FATALs if a dstdomain list contains both a domain and one of its
// subdomains (".github.com" already covers ".api.github.com"). SquidConf must
// drop the redundant children so the proxy can actually start.
func TestSquidConfDropsRedundantSubdomains(t *testing.T) {
	conf := SquidConf([]string{
		"github.com", "api.github.com", "codeload.github.com",
		"crates.io", "static.crates.io", "pypi.org",
	}, 3128)
	for _, gone := range []string{".api.github.com", ".codeload.github.com", ".static.crates.io"} {
		if strings.Contains(conf, gone) {
			t.Errorf("redundant subdomain %q should be dropped (covered by parent):\n%s", gone, conf)
		}
	}
	for _, kept := range []string{".github.com", ".crates.io", ".pypi.org"} {
		if !strings.Contains(conf, kept) {
			t.Errorf("covering domain %q should be kept:\n%s", kept, conf)
		}
	}
}

// The shipped baked allowlist must render a squid-valid dstdomain line: no entry
// may be a subdomain of another (that is exactly what crashes squid).
func TestSquidConfBakedAllowlistHasNoOverlaps(t *testing.T) {
	doms := domainsFromConf(SquidConf(BakedAllowlist(), 3128))
	for _, a := range doms {
		for _, b := range doms {
			if a != b && strings.HasSuffix(a, "."+b) {
				t.Errorf("baked allowlist renders overlapping dstdomains: %q is under %q", a, b)
			}
		}
	}
}

// domainsFromConf extracts the dstdomain tokens (without leading dots) from a
// rendered squid config.
func domainsFromConf(conf string) []string {
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		if rest, ok := strings.CutPrefix(line, "acl allowed dstdomain"); ok {
			for _, tok := range strings.Fields(rest) {
				out = append(out, strings.TrimPrefix(tok, "."))
			}
		}
	}
	return out
}
