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
