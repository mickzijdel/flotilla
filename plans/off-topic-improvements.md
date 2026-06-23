# Off-topic improvements

Out-of-scope observations noticed while working on other things. Capture, don't fix here.

## `done_signal = "process-exit"` is declared but not wired to container exit

Built-in profiles set `done_signal = "process-exit"` ([claude.toml](../internal/agent/builtin/claude.toml),
[codex.toml](../internal/agent/builtin/codex.toml)), and `flotilla submit` gates on the container being
`exited` ([submit.go](../internal/fleet/submit.go)). But the agent is launched via
`Backend.ExecDetached` = `docker exec -d` ([devcontainer.go](../internal/backend/devcontainer.go),
[fleet.go:158](../internal/fleet/fleet.go#L158)), so the agent process is a *backgrounded exec*: when it
finishes, the container's idle main process keeps running and the container stays `running`. Nothing
flips it to `exited` on its own — only a manual `flotilla stop` does (the submit test simulates "done"
with `fake.Stop()`, [submit_test.go:51](../internal/fleet/submit_test.go#L51)).

Net effect today: after an agent finishes, the user must `flotilla stop` then `flotilla submit` (or
`submit --force`). The "process-exit done-signal" is effectively manual.

Resolution path (now planned): the **logs/transcript-mounts** spec adds a launch-wrapper
`/flotilla/session/status` file that flips to `done` when the agent process exits, and the **daemon**
spec consumes that as the real done-signal (and auto-submits). See
[daemon design](../docs/specs/2026-06-23-flotilla-daemon-design.md). Sequencing: logs → daemon → fetch.
Independently, consider making the launch wrapper stop the container on agent exit so `process-exit`
truly maps to container `exited` and the manual-`stop` step disappears for non-daemon users too.
