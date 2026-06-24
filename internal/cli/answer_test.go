package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnswerCmdHumanOutputWritesResponse(t *testing.T) {
	f, dir := questionTestFleet(t, "otter", map[string]string{"q1": "Drop it?"})
	cmd := answerCmd(f)
	cmd.SetArgs([]string{"otter", "Yes", "go", "ahead"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "Answered otter") {
		t.Fatalf("human output = %q", out.String())
	}
	// The response file the shim blocks on now exists with the joined answer.
	b, err := os.ReadFile(filepath.Join(dir, "responses", "q1.json"))
	if err != nil {
		t.Fatalf("no response written: %v", err)
	}
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Answer string `json:"answer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Status != "ok" || resp.Data.Answer != "Yes go ahead" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestAnswerCmdErrorsWithoutPending(t *testing.T) {
	f, _ := questionTestFleet(t, "otter", nil)
	cmd := answerCmd(f)
	cmd.SetArgs([]string{"otter", "hello"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no pending question") {
		t.Fatalf("want no-pending error, got %v", err)
	}
}
