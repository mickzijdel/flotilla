package fleet

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runAskShim runs the ask shim with its session dir pointed at sessDir and the
// given question as $1, returning combined output + err.
func runAskShim(t *testing.T, sessDir, question string) (string, error) {
	t.Helper()
	script := strings.Replace(askShim, "sess=/flotilla/session", "sess="+sessDir, 1)
	cmd := exec.Command("sh", "-c", script, "flotilla-ask", question)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAskShimRequiresArgument: no question → usage error, exit 2, no request.
func TestAskShimRequiresArgument(t *testing.T) {
	sess := t.TempDir()
	out, err := runAskShim(t, sess, "")
	if err == nil {
		t.Fatalf("shim must fail without a question; out=%q", out)
	}
	if !strings.Contains(out, "usage:") {
		t.Fatalf("want usage error, got %q", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(sess, "requests")); len(entries) != 0 {
		t.Fatalf("no request should be written on usage error; got %v", entries)
	}
}

// TestAskShimBlocksThenPrintsAnswer: the shim blocks until a response appears,
// then prints exactly the answer text on stdout.
func TestAskShimBlocksThenPrintsAnswer(t *testing.T) {
	sess := t.TempDir()
	done := mirrorRequestsTo(t, sess, `{"status":"ok","data":{"answer":"go ahead"}}`)
	defer close(done)

	out, err := runAskShim(t, sess, "Should I proceed?")
	if err != nil {
		t.Fatalf("shim should succeed, got err=%v out=%q", err, out)
	}
	if strings.TrimSpace(out) != "go ahead" {
		t.Fatalf("answer on stdout = %q, want \"go ahead\"", out)
	}
}

// TestAskShimQuestionEscapingRoundTrips: a question with quotes and a backslash
// is written as valid JSON whose text field equals the original.
func TestAskShimQuestionEscapingRoundTrips(t *testing.T) {
	sess := t.TempDir()
	done := mirrorRequestsTo(t, sess, `{"status":"ok","data":{"answer":"ok"}}`)
	defer close(done)

	question := `Drop "users" \ table?`
	if _, err := runAskShim(t, sess, question); err != nil {
		t.Fatalf("shim: %v", err)
	}

	entries, _ := os.ReadDir(filepath.Join(sess, "requests"))
	var reqFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			reqFile = filepath.Join(sess, "requests", e.Name())
		}
	}
	if reqFile == "" {
		t.Fatalf("no request file written; entries=%v", entries)
	}
	b, err := os.ReadFile(reqFile)
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Type string `json:"type"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("request is not valid JSON (%q): %v", b, err)
	}
	if req.Type != "question" {
		t.Fatalf("request type = %q, want question", req.Type)
	}
	if req.Data.Text != question {
		t.Fatalf("question text round-trip = %q, want %q", req.Data.Text, question)
	}
}
