# Flotilla — Logs & transcript mounts

> Design spec for backlog item #1 ("Logs / transcript mounts"). Realizes design-spec §4.9
> ([2026-06-14-flotilla-design.md](2026-06-14-flotilla-design.md)). Each backlog item is its own
> spec → plan → build cycle.

## 1. Goal

Persist, per agent session, a host-side log directory under `~/.flotilla/logs/` containing:

- a **live transcript** — the agent's own transcript directory, bind-mounted into the container so
  the session transcript lands on the host as it is written (openable in VS Code mid-run, no copy);
- a **`container.log`** — the agent process's stdout/stderr, teed by the launch wrapper (the
  universal fallback for agents that expose no transcript);
- a **`status`** file — `running` while the agent runs, `done` after it exits (daemon-free);

and add a **`flotilla logs <agent> [-f]`** command to stream `container.log`.

Logs are an *artifact*: they outlive the container (`flotilla rm` keeps them). This matches the
design-spec model — "the branch is the portable state; logs persist to a host dir."

## 2. On-disk layout

```
~/.flotilla/logs/<repo-slug>/<YYYY-MM-DD-HHMM>-<agent>/
  transcript/      ← bind-mounted to the agent's transcript_path (live; see §4)
  container.log    ← agent stdout/stderr, teed via the launch wrapper (§5)
  status           ← "running" | "done", written by the launch wrapper (§6)
```

- **`<repo-slug>`** is derived from the repo URL: strip scheme and a trailing `.git`, take the last
  two path segments as `owner-repo`, and replace any character outside `[A-Za-z0-9._-]` with `-`.
  Examples: `https://github.com/owner/repo.git` → `owner-repo`; `git@github.com:owner/repo.git` →
  `owner-repo`. If an owner segment can't be parsed, fall back to just the repo segment; if nothing
  usable remains, fall back to `repo`.
- **`<YYYY-MM-DD-HHMM>`** is the spawn time in the engine's local time, minute precision.
- **`<agent>`** is the curated agent name (the same name used everywhere else).

### 2.1 Recovery via a label

The absolute session-dir path is recorded on the agent container as a new label
**`flotilla.logdir`** (constant `backend.LabelLogDir = "flotilla.logdir"`). `flotilla logs` reads
this label to locate `container.log` with no date arithmetic and no directory scanning. The label
is set in the `Up` call's `Labels` map alongside the existing `flotilla.agent`/`flotilla.repo`/etc.

### 2.2 Persistence and cleanup

`flotilla rm` removes the container (and its egress proxy/network) but **does not** remove the log
directory — the logs are the keep-after artifact. Manual cleanup of `~/.flotilla/logs/` is the
user's call (a future `flotilla logs --prune` is out of scope here).

## 3. Backend mount plumbing

`backend.UpOpts` gains a `Mounts []Mount` field (reusing the existing `backend.Mount{Source,
Target}`). The docker backend's `Up` renders each as a `devcontainer up` flag:

```
--mount type=bind,source=<host>,target=<container>
```

(`devcontainer up` supports repeatable `--mount type=bind,source=,target=`, confirmed against the
installed CLI.) The in-memory `backend.Fake.Up` records `opts.Mounts` so fleet tests can assert what
would be mounted without Docker.

This is the only `Backend` interface change. The firewall sidecar already uses `backend.Mount` via
the `Create` path; this adds the same concept to the devcontainer `Up` path.

## 4. Live transcript mount (resolve target before `up`)

A Docker bind-mount target must be an **absolute container path**, but the profile declares
`transcript_path` relative to the run user's home (`~/.claude/projects`), and the run user is only
reported *by* `up`. The engine breaks the cycle by reading the merged config first:

1. **Resolve the remote user before `up`.** Add `Backend.ReadConfig(ctx, workspaceFolder)
   (ConfigInfo, error)`, implemented by shelling out to
   `devcontainer read-configuration --workspace-folder <dest> --include-merged-configuration` and
   parsing `mergedConfiguration.remoteUser` from the trailing JSON. `ConfigInfo{ RemoteUser string }`.
2. **Compute the target.** `home := homeForUser(remoteUser)`; expand the profile's
   `transcript_path` by replacing a leading `~` with `home`. Call this `transcriptTarget`.
3. **Mount it.** Create `<session>/transcript` on the host (0777, see §7) and add
   `Mount{Source: <session>/transcript, Target: transcriptTarget}` to `UpOpts.Mounts`.

### 4.1 Fallback (copy-out)

If `ReadConfig` fails or returns an empty `remoteUser`, or the profile's `transcript_path` is empty,
the engine **skips the live mount** and records that this session is copy-fallback. After the agent
finishes (detected on the next `flotilla logs`/`list` that sees the container `exited`, or
explicitly — see §6.1), the engine `docker cp`s the in-container transcript dir to
`<session>/transcript`. The fallback never fails the spawn; it logs an advisory. `container.log`
and `status` are unaffected by the fallback (they ride the fixed-path session mount, §5).

> Rationale: the live mount is the better experience but depends on a resolvable remote user; the
> copy-out fallback guarantees the transcript still lands on the host for any container.

## 5. `container.log` capture (fixed-path session mount)

The whole session dir is bind-mounted to a **fixed, user-agnostic** container path
`/flotilla/session` (`Mount{Source: <session>, Target: "/flotilla/session"}`). No `~`/user
resolution is needed for this one, so it always works even when §4 falls back.

The launch wrapper (`launchScript`) changes its tail from `exec <launch>` to redirect the agent's
output into the mounted dir. Because output is teed to a mounted file, it lands on the host live.

> Note: `transcript/` lives *inside* `<session>`, so it is also visible under
> `/flotilla/session/transcript` — but the agent writes its transcript via the §4 mount at its
> natural `transcript_path`, not via `/flotilla/session`. The two mounts point at overlapping host
> paths with different container targets, which Docker allows.

## 6. `status` file (daemon-free)

The launch wrapper writes the session status around the agent invocation:

```sh
<cd>; export HOME=<home>; <source env>; export FLOTILLA_PROMPT=...
echo running > /flotilla/session/status
<launch> > /flotilla/session/container.log 2>&1
echo done > /flotilla/session/status
```

`exec` is dropped so the wrapper shell survives the agent process and records completion. The
container itself records `done` — no engine daemon or polling. `flotilla list` continues to derive
live status from Docker (`running`/`exited`) as today; the `status` file is the *persisted* record
that survives `rm`. A `blocked` state (via an agent hook where supported) is explicitly out of scope.

### 6.1 Interaction with the copy-fallback

When §4 fell back to copy-out, the engine needs a moment to copy the transcript after the agent
exits. Since there is no daemon, the copy is performed lazily and in exactly one place — the
`Fleet.Logs` accessor (§8). When `Logs` resolves an agent whose container is `exited`, whose session
carries the copy-fallback sentinel, and whose `<session>/transcript` is still empty, it runs the
`docker cp` then. The copy is idempotent (the empty-dir guard makes a second call a no-op) and never
runs inside the shared `resolve` lookup (no hidden side effects in a path used by every command).
Flagging: the absence of a live mount is recorded by writing a sentinel file `<session>/.copy-fallback`
at spawn time.

## 7. Permissions

Bind-mounted **writable** dirs (`<session>` and `<session>/transcript`) must be writable by the run
user, whose uid may differ from the host user's. Two-part handling:

1. Create the host dirs `0777` so a uid mismatch can't block the agent on a fresh mount.
2. After `up`, run one `docker exec -u root <id> chown -R <remoteUser> /flotilla/session` so the
   agent owns its session tree (covers tools that re-create files with stricter modes). When the run
   user is root this is a no-op-equivalent. Failure here is surfaced but treated like the other
   best-effort log steps (advisory, non-fatal).

## 8. `flotilla logs` command

```
flotilla logs <agent> [-f] [--json]
```

- Resolves the agent (existing `f.resolve`), reads the `flotilla.logdir` label, opens
  `<logdir>/container.log`, and copies it to stdout.
- `-f` / `--follow`: poll-and-copy (Go-side: read to EOF, sleep, repeat) until `<logdir>/status`
  reads `done`, then drain the remaining bytes and exit. Without `-f`, print the current contents
  and exit.
- `--json`: emit a small envelope `{ "agent", "logDir", "status", "transcriptPath" }` instead of raw
  log bytes (for the VS Code extension / orchestrating agents). `-f` and `--json` are mutually
  exclusive (follow streams raw bytes).
- Missing label / missing `container.log` → a clear error
  (`no logs for agent "x"` / `log file not found`).

A new `Fleet.Logs`-style accessor returns the resolved `logDir`/`status`/`transcriptPath` so the CLI
layer holds no path logic; the streaming/follow loop lives in the CLI command.

## 9. Wiring in `Fleet.Spawn`

Ordered changes (all host-side, before/around the existing steps):

1. Compute `slug`, `session := <logs>/<slug>/<ts>-<name>`, and `transcript := <session>/transcript`.
   `MkdirAll(transcript, 0777)`.
2. `cfg, _ := f.Backend.ReadConfig(ctx, dest)`; compute `transcriptTarget` (§4). If unresolved/empty,
   write `<session>/.copy-fallback` and skip the transcript mount.
3. Build `UpOpts.Mounts`: always the fixed `/flotilla/session` mount; plus the transcript mount when
   resolved. Add `LabelLogDir: session` to `UpOpts.Labels`.
4. After `up`: `chown -R` the session tree (§7).
5. Launch via the updated `launchScript` (§5/§6) — `/flotilla/session` paths are constant, so the
   wrapper needs the fixed mount path, not the host path.
6. On spawn failure, the existing cleanup removes the container + clone; the **log dir is left**
   (it's the artifact, and a failed spawn's partial log is useful for debugging).

`Agent` (the fleet's view struct) optionally gains a `LogDir string` field surfaced in
`flotilla list --json`, sourced from the label — convenient for the extension. (Low-risk additive
field.)

## 10. Error handling summary

| Condition | Behavior |
| --- | --- |
| Session-dir `MkdirAll` fails | Fail the spawn early (before `up`) — we can't log, surface it. |
| `ReadConfig` fails / no `remoteUser` | Skip live mount, flag copy-fallback (advisory), continue. |
| Empty `transcript_path` in profile | No transcript mount at all (some agents have none); `container.log` still captures. No fallback copy (nothing to copy). |
| `chown` fails | Advisory, non-fatal (uid may already match via 0777). |
| `docker cp` fallback fails | Advisory, non-fatal. |
| `flotilla logs` with no `logdir` label / no file | Clear user-facing error. |

## 11. Testing

Real-git/Docker-free unit + fake-backend tests, plus the existing self-skipping Docker integration
path for the live mount:

- **`repoSlug`** unit test: https, scp-style `git@`, trailing `.git`, owner-less, and
  unsafe-character inputs.
- **Launch wrapper** unit test (`launchScript`): asserts the `running` write, the
  `> /flotilla/session/container.log 2>&1` redirect, the `done` write, and that `exec` is gone.
- **Fleet spawn** test against `backend.Fake`: asserts `UpOpts.Mounts` contains the fixed
  `/flotilla/session` mount and (when `ReadConfig` returns a user via the fake) the transcript mount
  with the expanded target; asserts the `flotilla.logdir` label is set to the computed session dir;
  asserts the host transcript dir was created.
- **Copy-fallback** test: fake `ReadConfig` returns empty `remoteUser` → `.copy-fallback` sentinel
  written, no transcript mount in `UpOpts.Mounts`.
- **`flotilla logs`** CLI test: seed a temp session dir with a `container.log` + `status`, set the
  label on a fake container, assert non-follow output matches and `--json` envelope is correct.
- **Docker integration** (self-skips without Docker, like the existing backend test): a real
  `devcontainer up` with the mounts, assert the transcript dir and `container.log` appear on the
  host and `status` flips to `done` after a trivial agent command.

`backend.Fake` is extended with a settable `ReadConfigResult ConfigInfo` and records `Up` mounts.

## 12. Out of scope (future)

- `blocked` status via agent hooks.
- `flotilla logs --prune` / retention policy.
- Streaming the *transcript* (vs `container.log`) through `flotilla logs` — VS Code opens the live
  transcript dir directly.
- Indexing / a `latest` symlink per repo.
