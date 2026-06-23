package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

// fetchTestFleet seeds an agent "otter" with a real clone (origin on main) and
// returns a Fleet wired to the fake backend, reusing the shared seedClone helper.
func fetchTestFleet(t *testing.T) *fleet.Fleet {
	t.Helper()
	fake := backend.NewFake()
	work := t.TempDir()
	root := t.TempDir()
	seedClone(t, root, work, "otter", fake)
	return &fleet.Fleet{Backend: fake, WorkRoot: work}
}

func TestFetchCmdHumanOutput(t *testing.T) {
	f := fetchTestFleet(t)
	cmd := fetchCmd(f)
	cmd.SetArgs([]string{"otter"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "Fetched origin for otter") {
		t.Fatalf("human output = %q", out.String())
	}
}

func TestFetchCmdJSONOutput(t *testing.T) {
	f := fetchTestFleet(t)
	cmd := fetchCmd(f)
	cmd.SetArgs([]string{"otter", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got struct {
		Agent   string `json:"agent"`
		Fetched bool   `json:"fetched"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (out=%q)", err, out.String())
	}
	if got.Agent != "otter" || !got.Fetched {
		t.Fatalf("json output = %+v", got)
	}
}

func TestFetchCmdUnknownAgentErrors(t *testing.T) {
	f := fetchTestFleet(t)
	cmd := fetchCmd(f)
	cmd.SetArgs([]string{"ghost"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}
