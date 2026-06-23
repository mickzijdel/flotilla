package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runShim runs the fetch shim with its session dir pointed at sessDir (by
// rewriting the hard-coded sess= assignment) and returns combined output + err.
func runShim(t *testing.T, sessDir string) (string, error) {
	t.Helper()
	script := strings.Replace(fetchShim, "sess=/flotilla/session", "sess="+sessDir, 1)
	cmd := exec.Command("sh", "-c", script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mirrorRequestsTo polls sess/requests and writes resp into sess/responses for
// each completed request id until the returned channel is closed. It skips the
// .tmp atomic-write staging file so it only answers fully-written requests.
func mirrorRequestsTo(t *testing.T, sess, resp string) chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		reqDir := filepath.Join(sess, "requests")
		respDir := filepath.Join(sess, "responses")
		for {
			select {
			case <-done:
				return
			default:
			}
			entries, _ := os.ReadDir(reqDir)
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				id := strings.TrimSuffix(e.Name(), ".json")
				_ = os.MkdirAll(respDir, 0o777)
				final := filepath.Join(respDir, id+".json")
				if _, err := os.Stat(final); err == nil {
					continue // already answered — write exactly once
				}
				// Atomic write (tmp + rename) so the shim never cats a partial
				// or truncated response file.
				tmp := filepath.Join(respDir, "."+id+".tmp")
				if os.WriteFile(tmp, []byte(resp), 0o644) == nil {
					_ = os.Rename(tmp, final)
				}
			}
		}
	}()
	return done
}

// TestShimSucceedsOnOkResponse: a mirrored ok response makes the shim exit 0 and
// print its success message. It also proves the shim wrote a request file.
func TestShimSucceedsOnOkResponse(t *testing.T) {
	sess := t.TempDir()
	done := mirrorRequestsTo(t, sess, `{"status":"ok"}`)
	defer close(done)

	out, err := runShim(t, sess)
	if err != nil {
		t.Fatalf("shim should succeed, got err=%v out=%q", err, out)
	}
	if !strings.Contains(out, "origin fetched") {
		t.Fatalf("want success message, got %q", out)
	}
	// A request file (non-tmp) should have been written.
	entries, _ := os.ReadDir(filepath.Join(sess, "requests"))
	var sawRequest bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			sawRequest = true
		}
	}
	if !sawRequest {
		t.Fatalf("shim did not write a request file; entries=%v", entries)
	}
}

// TestShimReportsErrorResponse: an error response makes the shim exit non-zero
// and echo the payload.
func TestShimReportsErrorResponse(t *testing.T) {
	sess := t.TempDir()
	done := mirrorRequestsTo(t, sess, `{"status":"error","message":"boom"}`)
	defer close(done)

	out, err := runShim(t, sess)
	if err == nil {
		t.Fatalf("shim should fail on error response; out=%q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("want error payload echoed, got %q", out)
	}
}
