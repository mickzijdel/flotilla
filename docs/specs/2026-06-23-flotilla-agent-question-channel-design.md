# Flotilla — Agent question/answer channel

**Date:** 2026-06-23
**Status:** Implemented (2026-06-24) — see the
[implementation plan](../plans/2026-06-24-flotilla-agent-question-channel.md).
**Scope:** Lets a running agent ask its human/AI operator a question mid-session and block for the
answer — with no network and no credentials in the container. Builds on the **daemon's request-handler
seam** ([2026-06-23-flotilla-daemon-design.md](2026-06-23-flotilla-daemon-design.md) §9), its **inbox**
(§7) and **state mirror** (§8), and finishes the `flotilla answer` CLI that spec declared-and-deferred
(§11). Also realises the **`blocked` status** the logs spec deferred
([2026-06-23-flotilla-logs-transcript-mounts-design.md](2026-06-23-flotilla-logs-transcript-mounts-design.md) §12).

## 1. Goal

A sandboxed agent sometimes hits a decision only its operator can make — an ambiguous requirement, a
risky irreversible step, a missing credential it must not be handed. Today it has no way to ask: the
container has no network path to the operator, and the only signal it can emit is "process exited." So
it either guesses or gives up.

This slice adds a **question/answer channel**: the agent runs `flotilla-ask "…"`, which blocks while the
question surfaces to the operator (inbox + `flotilla questions`); the operator replies with
`flotilla answer <agent> "…"`; the answer lands back in the container and the agent continues. It rides
the same filesystem-mediated session channel that on-demand fetch uses, so it adds **no new transport** —
just a new request `type` and the human-in-the-loop reply path.

## 2. Decisions locked (from brainstorming, 2026-06-23)

| # | Area | Decision |
|---|---|---|
| 1 | Transport | **Reuse the daemon §9 seam.** New request `type: "question"`. No new channel, mount, or socket. |
| 2 | Handler shape | **Non-terminal handler.** Unlike `fetch` (which answers immediately), the `question` handler does *not* produce the response — it returns the **`deferred`** sentinel, notifies the operator, and waits; the response is produced **out-of-band** by `flotilla answer`. This needs a small seam change (the shipped dispatch loop always writes a response) — see §4.1. |
| 3 | Answer path | **`flotilla answer <agent> [--id <id>] "text"`** writes the response file **directly** into the agent's session dir (daemon-independent, like `flotilla fetch`'s host path). The agent's shim is already blocking on it. |
| 4 | Discovery | **`flotilla questions [--json] [--watch]`** lists pending questions, derived purely from the filesystem (a `requests/*.json` with no matching `responses/*.json`), so it works even if the daemon is down. |
| 5 | Blocked status | A pending question makes the agent **`blocked`** — surfaced as a derived status in `flotilla list`/`status`, computed from the filesystem. This is the realisation of the logs spec's deferred `blocked` state. |
| 6 | Wait semantics | The `flotilla-ask` shim **blocks indefinitely** until the operator answers — an unanswered question must **not** let the agent proceed (the whole point is to stop it guessing). The agent stays `blocked`; the operator aborts with `flotilla stop`/`rm <agent>` if they don't want to answer. |
| 7 | Payload | **Free-text question → free-text answer** in v1. Structured/multiple-choice questions are future. |

## 3. Architecture

```
  container: `flotilla-ask "Should I drop the legacy table?"`
        │  write requests/<id>.json {"type":"question","data":{"text":...}}  (atomic: tmp+rename)
        │  then BLOCK polling responses/<id>.json   (until answered)
        ▼
  ┌────────────────────────────┐         ┌──────────────────────────────────────────┐
  │ daemon `question` handler   │         │  operator                                  │
  │  (§9 dispatch)              │         │   sees it via inbox / `flotilla questions`  │
  │  • inbox `question` event   │────────▶│   runs `flotilla answer <agent> "Yes, …"`   │
  │  • mark agent blocked       │         └───────────────────┬────────────────────────┘
  │  • returns "deferred"       │                             │ write responses/<id>.json
  │    (loop writes no response)│                             │  {"status":"ok",            (atomic)
  └────────────┬───────────────┘                             │   "data":{"answer":"Yes, …"}}
               │ observes responses/<id>.json appears        │
               │  • inbox `question_answered`                ▼
               │  • clear blocked              ┌──────────────────────────────┐
               └──────────────────────────────│ shim reads answer, prints it, │
                                              │ agent continues                │
                                              └──────────────────────────────┘
```

The agent→operator leg needs the **daemon** (something must watch the request and notify). The
operator→agent leg (`flotilla answer`) is a **direct filesystem write** and needs no daemon. So if the
daemon is down the agent still blocks and an operator who runs `flotilla questions` can still see and
answer it — they just don't get the proactive inbox notification.

## 4. The `question` request

Shim writes `/flotilla/session/requests/<id>.json` (atomic tmp+rename, matching the built
`daemon.Request{ID, Type, Data}` envelope — the question text rides under `data`):

```json
{ "type": "question", "id": "<id>", "data": { "text": "Should I drop the legacy `users_old` table?" } }
```

The daemon's `question` `Handler`, on dispatch:

1. Appends an inbox `question` event (daemon-spec §7) carrying `agent`, `id`, and `data.text`.
2. Marks the agent **blocked** in the state mirror (`~/.flotilla/daemon/agents/<name>.json`, daemon-spec §8).
3. **Returns the deferred sentinel `daemon.Response{Status: "deferred"}`** — it does *not* produce an
   answer (that comes out-of-band from `flotilla answer`). The request file stays in `requests/` as the
   pending marker until answered.

### 4.1 Seam change required (the built dispatch loop always writes a response)

The shipped `dispatchRequests` (`internal/daemon/requests.go`) writes `responses/<id>.json` from **every**
handler's return value, and re-scans `requests/` each tick for any request lacking a response. A
`question` handler that returned a normal `Response` would therefore (a) unblock the agent immediately
with a bogus answer, and (b) be re-dispatched — re-notifying — on every tick. So this slice makes two
small, backward-compatible changes to the seam:

- **Honour a `deferred` status.** When a handler returns `Response{Status: "deferred"}`, the loop writes
  **no** response file. (`fetch` and any other terminal handler are unaffected — they return
  `ok`/`error` as before.)
- **Don't re-dispatch a deferred request.** The supervisor keeps an in-memory set of `(agent, id)` pairs
  it has already dispatched-and-deferred, so a pending question is handled (notified) **once**, not every
  tick, until a real `responses/<id>.json` appears (the terminal state, written by `flotilla answer`).
  The set is process-lifetime only; on a daemon restart a still-pending question is re-notified once,
  which is acceptable (and the inbox dedups visually by `id`).

## 5. The answer path — `flotilla answer`

```
flotilla answer <agent> [--id <id>] "text"
```

- Resolves the agent (existing `resolve`), finds its session dir via the `flotilla.logdir` label
  (logs spec §2.1), and locates the pending question: the lone unanswered `requests/*.json` of
  `type:"question"`, or the one named by `--id` when several are pending.
- Writes `/flotilla/session/responses/<id>.json` atomically, in the **same `daemon.Response` envelope**
  the seam uses for every other type, with the answer under `data`:
  `{ "status": "ok", "data": { "answer": "text" } }`. That's the exact file the shim is blocking on, so
  it terminates the deferred request the same way a normal handler response would.
- **Daemon-independent** — it's just a scoped file write, so it works whether or not the daemon runs
  (mirrors `flotilla fetch`'s host path). The daemon, when up, *observes* the new response file and
  emits a `question_answered` inbox event + clears the blocked flag; when down, the agent unblocks
  anyway because the shim reads the file directly.
- Errors clearly when there is no pending question, or when `--id` is needed to disambiguate.

## 6. Discovering pending questions & blocked status

```
flotilla questions [--json] [--watch]
```

- Lists every pending question across all agents — derived **purely from the filesystem**: for each
  agent's session dir, the `requests/*.json` of `type:"question"` with no matching `responses/*.json`.
  Works with the daemon down. `--watch` polls-and-prints (same loop as `flotilla logs -f`).
- Output per question: `agent`, `id`, `text`, age. `--json` for the extension.

**Blocked status.** `flotilla list` (and `status`) overlay a derived **`blocked`** state on an agent
that has ≥1 pending question, computed the same filesystem way — no daemon required, no change to the
logs `status` file (which stays `running`/`done`; `blocked` is a higher-level overlay meaning "running,
but waiting on the operator"). This is the concrete realisation of the logs spec's deferred `blocked`
state, now that there's a real thing to block on.

## 7. The in-container `flotilla-ask` shim

A small POSIX-sh script injected to **`/usr/local/bin/flotilla-ask`** at spawn (the same root-capable
install path as `flotilla-fetch`), `chmod +x`. Usage: `flotilla-ask "question text"`.

```sh
#!/bin/sh
# Ask the operator a question and block for the answer (no network needed).
set -e
[ -n "$1" ] || { echo "usage: flotilla-ask \"your question\"" >&2; exit 2; }
sess=/flotilla/session
id="$(date +%s%N)-$$"
mkdir -p "$sess/requests" "$sess/responses"
# atomic write of the question
printf '{"type":"question","id":"%s","data":{"text":%s}}' "$id" "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/^/"/; s/$/"/')" \
  > "$sess/requests/.$id.tmp"
mv "$sess/requests/.$id.tmp" "$sess/requests/$id.json"
# block until the operator answers — indefinitely, on purpose: the agent must
# not proceed without an answer. (Operator can `flotilla stop <agent>` to abort.)
while [ ! -f "$sess/responses/$id.json" ]; do sleep 1; done
# emit just the answer text to stdout for the agent to read
sed -n 's/.*"answer":"\(.*\)".*/\1/p' "$sess/responses/$id.json"
```

(The JSON escaping is intentionally minimal — the engine-side handler re-parses defensively; see §9
testing. A richer shim can shell out to a JSON tool when one is present in the image.)

**Agent awareness.** A line is appended to the injected prompt (the `wrap_up`/fetch-preamble mechanism):
*"If you're blocked on a decision only the operator can make — an ambiguous requirement or a risky
irreversible action — run `flotilla-ask \"your question\"` and wait for the answer rather than guessing."*

## 8. Inbox events

The daemon adds two event `type`s to the existing inbox (daemon-spec §7), no format change:

```json
{"ts":"…","agent":"brave-otter","type":"question","data":{"id":"…","text":"Should I drop …?"}}
{"ts":"…","agent":"brave-otter","type":"question_answered","data":{"id":"…","answer":"Yes, it's unused."}}
```

`flotilla inbox --watch` surfaces questions in near-real-time; the VS Code extension watches the same
file (and `flotilla questions --json`) to render a prompt-the-operator UI.

## 9. Trust boundary

- The question text and the answer text are **opaque operator/agent strings** relayed through files —
  never executed. The handler does a fixed, typed thing (notify + wait); `flotilla answer` does a fixed,
  typed thing (write one response file scoped to one agent's session dir). No command execution, no
  cross-agent access — same posture as the fetch handler (daemon-spec §12).
- The answer is **untrusted operator input** from the agent's perspective; it's plain text the agent
  reads, exactly as if typed into its prompt. Nothing about the channel grants the operator code
  execution in the container beyond what answering a prompt already implies.
- The agent can only write into **its own** session dir (already true), and can only create bounded,
  typed requests there.

## 10. Error handling

| Condition | Behaviour |
|---|---|
| `flotilla-ask` with no argument | usage error, exit 2 (no request written). |
| Operator never answers | the agent stays `blocked` indefinitely, by design (it must not proceed without an answer); abort via `flotilla stop`/`rm <agent>`. |
| `flotilla answer` with no pending question | clear error: "no pending question for agent %q". |
| Multiple pending, no `--id` | error listing the pending ids; require `--id`. |
| `flotilla answer` for an unknown agent / missing session dir | clear error (existing `resolve` + label lookup). |
| Daemon down | agent still blocks; `flotilla questions`/`answer` still work (filesystem-derived); only the proactive inbox notification is missing. |
| Malformed question JSON (shim escaping edge) | handler parses defensively; an unparseable request is surfaced as a `question` event with raw text and still answerable by `--id`. |

## 11. Testing

Docker-free where possible (temp session dirs + fake daemon/backend), plus the self-skipping Docker path.

- **`question` handler** (temp session dir + fake inbox): a `requests/<id>.json` of `type:"question"`
  produces an inbox `question` event and a blocked mark, and **no** response file (the handler returns
  `deferred`); a later `responses/<id>.json` produces `question_answered` and clears blocked.
- **Seam: deferred is non-terminal and dispatched once** — `dispatchRequests` writes no file for a
  `deferred` return, and re-scanning the same still-pending request does **not** re-invoke the handler
  (one inbox event, not one per tick); a terminal (`ok`/`error`) return still writes a response as before
  (no regression for `fetch`).
- **`flotilla answer`** — writes the correctly-named response with the answer text; picks the lone
  pending question without `--id`; errors on none / requires `--id` on several; is daemon-independent
  (works with no daemon process).
- **`flotilla questions`** — filesystem-derived listing across multiple agents; pending vs answered;
  `--json` shape; `--watch` drains new questions.
- **Blocked status** — `flotilla list` shows `blocked` for an agent with a pending question and reverts
  to `running` once answered, with no daemon involved.
- **Shim** — script-level: argument-required guard; atomic request write; blocks until a response
  appears then unblocks; correct answer text on stdout; quote/backslash escaping in the question text
  round-trips through the handler.
- **Prompt preamble** — the ask-awareness line is appended to the injected prompt.
- **Docker integration** (self-skips without Docker): a real agent runs `flotilla-ask`, the operator
  `flotilla answer`s, and the agent receives the answer.

## 12. Sequencing & dependencies

1. **Logs / transcript mounts** (done) — the `/flotilla/session` mount + `flotilla.logdir` label the
   channel and `flotilla answer`/`questions` rely on.
2. **Daemon** — the request-handler seam, inbox, and state mirror this plugs into. **Hard prerequisite**
   for the notification half (the agent-blocks/operator-sees flow).
3. **On-demand fetch** — sibling handler on the same seam; this spec reuses its shim pattern and
   `/usr/local/bin` injection path. Either can land first; both depend on the daemon.

## 13. Out of scope (future)

- **Structured questions** — multiple-choice / typed answers (boolean, enum, file path) with validation.
  v1 is free-text both ways.
- **Desktop / push notification** — beyond the filesystem inbox (the Option-C socket would carry true
  push to the extension).
- **Routing to a specific operator** or multi-operator arbitration — v1 is "whoever runs `flotilla`".
- **Auto-answer / policy replies** (e.g. an AI operator auto-resolving common questions) — a later slice
  could register an automated answerer on the same `question` stream.
- **Question history / audit view** beyond the append-only inbox.
