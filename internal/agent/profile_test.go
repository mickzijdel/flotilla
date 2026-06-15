package agent

import "testing"

func TestBuiltinsIncludeClaudeAndCodex(t *testing.T) {
	got, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins() error: %v", err)
	}
	if _, ok := got["claude"]; !ok {
		t.Errorf("missing claude builtin; have %v", keys(got))
	}
	if _, ok := got["codex"]; !ok {
		t.Errorf("missing codex builtin; have %v", keys(got))
	}
}

func TestParseProfileFields(t *testing.T) {
	p, err := Parse([]byte(`
name = "codex"
launch = 'codex exec "{prompt}"'
env = ["OPENAI_API_KEY"]
egress_allow = ["api.openai.com"]
`))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if p.Name != "codex" {
		t.Errorf("Name = %q, want codex", p.Name)
	}
	if p.Launch != `codex exec "{prompt}"` {
		t.Errorf("Launch = %q", p.Launch)
	}
	if len(p.Env) != 1 || p.Env[0] != "OPENAI_API_KEY" {
		t.Errorf("Env = %v", p.Env)
	}
}

func TestRenderLaunchSubstitutesPrompt(t *testing.T) {
	p := Profile{Launch: `claude -p "{prompt}"`}
	if got := p.RenderLaunch("fix bug"); got != `claude -p "fix bug"` {
		t.Errorf("RenderLaunch = %q", got)
	}
}

func keys(m map[string]Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
