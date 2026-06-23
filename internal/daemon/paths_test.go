package daemon

import (
	"path/filepath"
	"testing"
)

func TestPaths(t *testing.T) {
	p := Paths{Root: "/home/u/.flotilla"}
	cases := map[string]string{
		p.Pid():                "/home/u/.flotilla/daemon.pid",
		p.Lock():               "/home/u/.flotilla/daemon.lock",
		p.Log():                "/home/u/.flotilla/daemon.log",
		p.Inbox():              "/home/u/.flotilla/inbox.jsonl",
		p.StateDir():           "/home/u/.flotilla/daemon",
		p.Version():            "/home/u/.flotilla/daemon/version",
		p.AgentsDir():          "/home/u/.flotilla/daemon/agents",
		p.AgentRecord("otter"): "/home/u/.flotilla/daemon/agents/otter.json",
		p.LogsRoot():           "/home/u/.flotilla/logs",
	}
	for got, want := range cases {
		if filepath.Clean(got) != filepath.Clean(want) {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
