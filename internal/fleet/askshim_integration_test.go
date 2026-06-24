package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAskShimBlocksThenAnswerUnblocksInContainer is the Docker-specific claim of
// the question/answer channel: the in-container flotilla-ask shim writes its
// request and BLOCKS, and because the session dir is bind-mounted, a host-side
// answer (the same envelope Fleet.Answer writes) lands in the container instantly
// and unblocks the shim, which prints exactly the answer text. Self-skips without
// Docker. Exercises the real askShim constant + response envelope over a real
// container shell + bind mount (no devcontainer needed).
func TestAskShimBlocksThenAnswerUnblocksInContainer(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available; skipping integration test")
	}

	sess := t.TempDir() // bind-mount source = the agent's session dir
	// Run as the host uid/gid so files in the bind-mounted (host-owned) session
	// dir are user-owned — both the host answer write and TempDir cleanup work.
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	cid := dockerRun(t, "run", "-d", "--rm", "--user", user, "--entrypoint", "sh",
		"-v", sess+":/flotilla/session", "alpine", "-c", "sleep 120")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", cid).Run() })

	// Run flotilla-ask inside the container; it blocks until we answer.
	type result struct {
		out string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		cmd := exec.Command("docker", "exec", cid, "sh", "-c", askShim, "flotilla-ask", "Should I proceed?")
		out, err := cmd.CombinedOutput()
		resCh <- result{strings.TrimSpace(string(out)), err}
	}()

	// Wait for the request file to appear, and confirm the shim is still blocking.
	reqPath := waitForRequest(t, filepath.Join(sess, "requests"))
	select {
	case r := <-resCh:
		t.Fatalf("shim returned before being answered: out=%q err=%v", r.out, r.err)
	case <-time.After(500 * time.Millisecond):
	}

	// Host answers, writing the same envelope Fleet.Answer produces.
	id := strings.TrimSuffix(filepath.Base(reqPath), ".json")
	respDir := filepath.Join(sess, "responses")
	if err := os.MkdirAll(respDir, 0o777); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(answerEnvelope{Status: "ok", Data: answerData{Answer: "yes, go ahead"}})
	if err := atomicWriteFile(filepath.Join(respDir, id+".json"), b); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("shim failed after answer: %v (out=%q)", r.err, r.out)
		}
		if r.out != "yes, go ahead" {
			t.Fatalf("shim stdout = %q, want \"yes, go ahead\"", r.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shim did not unblock within 10s of being answered")
	}
}

// waitForRequest polls reqDir for the first fully-written (non-dot) request file.
func waitForRequest(t *testing.T, reqDir string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(reqDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
				return filepath.Join(reqDir, e.Name())
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no request file appeared within 10s")
	return ""
}
