package fleet

import (
	"context"
	"os"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// askShimPath is where the in-container `flotilla-ask` command lands. It is on
// the default PATH, so no launch-wrapper change is needed.
const askShimPath = "/usr/local/bin/flotilla-ask"

// askShim is the POSIX-sh `flotilla-ask` command (spec §7). The agent runs it to
// ask its operator a question and BLOCK indefinitely for the answer — an
// unanswered question must not let the agent proceed (the whole point is to stop
// it guessing); the operator aborts with `flotilla stop`/`rm` if they won't
// answer. It writes the request atomically (tmp + mv), polls for the response,
// and prints just the answer text. The /flotilla/session path MUST match
// containerSessionDir (guarded by TestAskShimTargetsSessionMount). The JSON
// escaping is intentionally minimal — the engine-side handler parses defensively.
const askShim = `#!/bin/sh
# Ask the operator a question and block for the answer (no network needed).
set -e
[ -n "$1" ] || { echo "usage: flotilla-ask \"your question\"" >&2; exit 2; }
sess=/flotilla/session
id="$(date +%s%N)-$$"
mkdir -p "$sess/requests" "$sess/responses"
q="$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"
printf '{"type":"question","id":"%s","data":{"text":"%s"}}' "$id" "$q" > "$sess/requests/.$id.tmp"
mv "$sess/requests/.$id.tmp" "$sess/requests/$id.json"
# Block until the operator answers — indefinitely, on purpose.
while [ ! -f "$sess/responses/$id.json" ]; do sleep 1; done
# Emit just the answer text to stdout for the agent to read.
sed -n 's/.*"answer":"\(.*\)".*/\1/p' "$sess/responses/$id.json"
`

// installAskShim writes the shim to a host temp file, copies it into the
// container at askShimPath, and marks it executable (root-capable install step;
// chmod makes it runnable by the agent regardless of the copied uid).
func installAskShim(ctx context.Context, be backend.Backend, id string) error {
	tmp, err := os.CreateTemp("", "flotilla-ask-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(askShim); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := be.CopyTo(ctx, id, tmp.Name(), askShimPath); err != nil {
		return err
	}
	return be.Exec(ctx, id, []string{"chmod", "0755", askShimPath})
}
