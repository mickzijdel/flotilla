package fleet

import (
	"context"
	"os"

	"github.com/mickzijdel/flotilla/internal/backend"
)

// fetchShimPath is where the in-container `flotilla-fetch` command lands. It is
// on the default PATH, so no launch-wrapper change is needed.
const fetchShimPath = "/usr/local/bin/flotilla-fetch"

// fetchShim is the POSIX-sh `flotilla-fetch` command. The credential-less agent
// runs it to ask the engine (via the daemon's request channel on the session
// mount) to fetch origin into its clone, then integrates locally. It writes the
// request atomically (tmp + mv) and blocks until the daemon writes the response,
// capped so it can't hang forever if the daemon is down. The /flotilla/session
// path MUST match containerSessionDir (guarded by TestFetchShimTargetsSessionMount).
const fetchShim = `#!/bin/sh
# Ask the engine to fetch origin into this agent's clone (we have no git creds).
set -e
sess=/flotilla/session
id="$(date +%s%N)-$$"
mkdir -p "$sess/requests" "$sess/responses"
printf '{"type":"fetch","id":"%s"}' "$id" > "$sess/requests/.$id.tmp"
mv "$sess/requests/.$id.tmp" "$sess/requests/$id.json"
i=0
while [ ! -f "$sess/responses/$id.json" ]; do
  i=$((i+1)); [ "$i" -gt 120 ] && { echo "flotilla-fetch: timed out (is the daemon running?)" >&2; exit 1; }
  sleep 1
done
resp="$(cat "$sess/responses/$id.json")"
case "$resp" in
  *'"status":"ok"'*) echo "flotilla-fetch: origin fetched"; exit 0 ;;
  *) echo "flotilla-fetch: $resp" >&2; exit 1 ;;
esac
`

// installFetchShim writes the shim to a host temp file, copies it into the
// container at fetchShimPath, and marks it executable. Done as part of the
// root-capable install step; chmod makes it runnable by the agent regardless of
// the copied uid.
func installFetchShim(ctx context.Context, be backend.Backend, id string) error {
	tmp, err := os.CreateTemp("", "flotilla-fetch-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(fetchShim); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := be.CopyTo(ctx, id, tmp.Name(), fetchShimPath); err != nil {
		return err
	}
	return be.Exec(ctx, id, []string{"chmod", "0755", fetchShimPath})
}
