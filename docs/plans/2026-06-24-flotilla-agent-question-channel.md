# Flotilla Agent question/answer channel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a running, credential-less agent ask its operator a question mid-session (`flotilla-ask "…"`) and **block** for the reply; the operator answers with `flotilla answer <agent> "…"` and the agent continues. Realises the deferred **`blocked`** status.

**Architecture:** Reuse the daemon's §9 request-handler seam — the same filesystem channel on-demand fetch uses — with a new request `type:"question"`. Unlike `fetch` (terminal), the `question` handler is **non-terminal**: it notifies the operator (inbox `question` event + marks blocked) and returns the **`deferred`** sentinel; the dispatch loop writes **no** response. The response is produced **out-of-band** by `flotilla answer`, which writes `responses/<id>.json` directly into the agent's session dir (daemon-independent, like `flotilla fetch`'s host path). The `flotilla-ask` shim is already blocking on that file. `flotilla questions` and the `blocked` overlay in `flotilla list` are derived **purely from the filesystem**, so they work with the daemon down.

**Tech Stack:** Go 1.26, cobra CLI, in-memory `backend.Fake` for unit tests, temp session dirs for the seam/answer/questions tests, `sh` for the shim script test.

## Global Constraints

- Go 1.26.4; cobra v1.10.2; BurntSushi/toml v1.6.0 — do not add deps.
- The container holds **no** credentials and **no** network path to the operator; the only transport is the bind-mounted `/flotilla/session` (`containerSessionDir`) request/response channel.
- `internal/fleet` must **not** import `internal/daemon` (daemon imports fleet — cycle). `flotilla answer` therefore writes the `{"status":"ok","data":{"answer":…}}` envelope as plain JSON via fleet's own code, not `daemon.Response`.
- The answer envelope must keep `answer` as the **last** string before the closing braces so the shim's greedy POSIX `sed` extracts it correctly — use a fixed-field-order struct (`{status, data:{answer}}`), never a Go `map` (whose keys sort `data` before `status`).
- Tests Docker-free where possible; the live-Docker integration test self-skips when Docker is unavailable, matching `internal/backend`/`internal/fleet`'s existing pattern.
- Seam contract (reconciled with the shipped seam): handlers return a `daemon.Response`; `dispatchRequests` writes `responses/<id>.json` from terminal (`ok`/`error`) returns and is idempotent on an already-present response. **This slice** adds the `deferred` status (write no file) + dispatch-once tracking (spec §4.1). `fetch` and every other terminal handler are unaffected.

---

### Task 1: Seam change — `deferred` non-terminal status + dispatch-once

**Files:** Modify `internal/daemon/requests.go`, `internal/daemon/supervisor.go`; update `internal/daemon/requests_test.go`.

The shipped `dispatchRequests` is a free function that writes a response for **every** handler return and re-dispatches any request lacking a response each tick. Make it a `*Supervisor` method so it can own an in-memory `deferred` set and reach `emit`/the state mirror, then:

- Add `const StatusDeferred = "deferred"`.
- Honour `deferred`: when a handler returns `Status == StatusDeferred`, write **no** response file; record `(agent,id)→type` in `s.deferred` (lazy-init `map[string]string`).
- Dispatch-once: a request whose key is already in `s.deferred` and still has no response is **skipped** (notified once, not per tick).
- Answered transition: a request whose key is in `s.deferred` and now **has** a response was answered out-of-band — call `s.onAnswered(agent, id, typ, respPath)` and delete the key.

**Interfaces:** `func (s *Supervisor) dispatchRequests(ctx, agent, sessionDir string)`; `s.deferred map[string]string`; `func (s *Supervisor) onAnswered(agent, id, typ, respPath string)` (Task 2 fills the `question` branch — in Task 1 it can be an empty stub so the seam tests compile).

- [ ] **Step 1:** Update `requests_test.go`'s two tests to call `s.dispatchRequests` on a `&Supervisor{Registry: reg, Paths: Paths{Root: t.TempDir()}, Now: fixedClock}` instead of the free function. Add a new test `TestDispatchDeferredWritesNoResponseAndDispatchesOnce`: a handler returning `deferred` is invoked exactly once across two `dispatchRequests` calls and writes no `responses/<id>.json`; then dropping a `responses/<id>.json` in (simulating `flotilla answer`) makes the next scan observe the answer (assert via a spy `onAnswered` or the resulting inbox event in Task 2). Add a regression assertion that a terminal `ok` handler still writes the response across the same shape.
- [ ] **Step 2:** Run `go test ./internal/daemon/ -run 'Dispatch' -v` → FAIL (method undefined / deferred not honoured).
- [ ] **Step 3:** Implement: add `StatusDeferred`; convert `dispatchRequests` to a method with the deferred map + `onAnswered` hook; change `scanOnce` to call `s.dispatchRequests(ctx, a.Name, a.LogDir)`. Lazy-init `s.deferred` at the top of the method. Add an empty `onAnswered` stub.
- [ ] **Step 4:** `go test ./internal/daemon/ -v` → PASS.
- [ ] **Step 5:** Commit `feat(daemon): deferred (non-terminal) request status + dispatch-once seam`.

---

### Task 2: `question` handler, `blocked` mirror flag, inbox events, answered observer

**Files:** Modify `internal/daemon/inbox.go`, `internal/daemon/state.go`, `internal/daemon/supervisor.go`; extend `internal/daemon/supervisor_test.go`.

- [ ] **Step 1:** Add inbox events `EventQuestion = "question"`, `EventQuestionAnswered = "question_answered"`. Add `Blocked bool json:"blocked,omitempty"` to `AgentRecord`. Add `func (s *Supervisor) markBlocked(name string, blocked bool)` (load → set → save, best-effort).
- [ ] **Step 2:** Write failing tests (temp session dir + real `Paths`): `TestQuestionHandlerNotifiesAndDefers` — a `question` request yields an inbox `question` event carrying `id`+`text`, a blocked mark in the mirror, a `deferred` response, and **no** response file written via the seam; `TestQuestionAnsweredEmitsEventAndClearsBlocked` — after a `responses/<id>.json` with `data.answer` appears, the next `dispatchRequests` emits `question_answered` (carrying the answer) and clears the blocked mark. Drive these through `s.dispatchRequests` end-to-end (request file on disk → scan → assert), reusing the `eventTypes`/`mustRead` helpers.
- [ ] **Step 3:** Run `go test ./internal/daemon/ -run 'Question' -v` → FAIL.
- [ ] **Step 4:** Implement `questionHandler` (extract `data.text`, emit `EventQuestion` with `{id,text}`, `markBlocked(agent,true)`, return `Response{Status: StatusDeferred}`); register `"question"` in `registerHandlers`; fill `onAnswered`'s `question` branch (read the response, pull `data.answer`, emit `EventQuestionAnswered` with `{id,answer}`, `markBlocked(agent,false)`).
- [ ] **Step 5:** `go test ./internal/daemon/ -v` → PASS.
- [ ] **Step 6:** Commit `feat(daemon): question handler — notify+defer, question_answered, blocked mirror`.

---

### Task 3: `Fleet.Questions`, `Fleet.Answer`, pending-question helpers, `blocked` list overlay

**Files:** Create `internal/fleet/questions.go`; create `internal/fleet/questions_test.go`; modify `internal/fleet/fleet.go` (`List` overlay).

**Interfaces:**
- `type PendingQuestion struct { Agent, ID, Text string; Asked time.Time }`.
- `func (f *Fleet) Questions(ctx) ([]PendingQuestion, error)` — list agents, for each with a `LogDir`, scan `requests/*.json` of `type:"question"` lacking a matching `responses/*.json`; `Text` from the request, `Asked` from the request file mod time.
- `func (f *Fleet) Answer(ctx, name, id, text string) error` — `resolve` the agent, read its `LabelLogDir`, locate the lone pending question (or the one named by `id`; error on none / require `id` on several), write `responses/<id>.json` atomically as `{"status":"ok","data":{"answer":text}}` (fixed-field-order struct).
- Filesystem helpers `pendingQuestions(logDir string) []PendingQuestion` and `hasPendingQuestion(logDir string) bool` shared by `Questions`, `Answer`, and the `List` overlay.
- `List` overlay: when `c.Status == "running"` and `hasPendingQuestion(logDir)`, set `Agent.Status = "blocked"`. (The daemon's done-detection reads the `status` **file**, not `Agent.Status`, so this overlay is safe.)

- [ ] **Step 1:** Write failing tests: `Answer` writes the correctly-named response with the answer text and round-trips a quote/backslash; picks the lone pending question without `id`; errors on none / requires `id` on several; works with no daemon (pure file write). `Questions` lists across multiple agents, excludes answered, `Asked` populated. `List` shows `blocked` for a running agent with a pending question and `running` once answered. Use the existing fleet test helpers (`backend.Fake`, `registerAgent`, a temp `LogDir` set via the agent label).
- [ ] **Step 2:** Run `go test ./internal/fleet/ -run 'Question|Answer|BlockedOverlay' -v` → FAIL.
- [ ] **Step 3:** Implement `questions.go` + the `List` overlay.
- [ ] **Step 4:** `go test ./internal/fleet/ -v` → PASS.
- [ ] **Step 5:** Commit `feat(fleet): Questions/Answer (filesystem-derived) + blocked list overlay`.

---

### Task 4: `flotilla questions` + `flotilla answer` CLI

**Files:** Create `internal/cli/questions.go`, `internal/cli/answer.go`; modify `internal/cli/cli.go`; create `internal/cli/questions_test.go`, `internal/cli/answer_test.go`.

**Interfaces:**
- `questionsCmd` — `flotilla questions [--json] [--watch]`; human lines `agent  id  age  text`; `--json` an array of `PendingQuestion`; `--watch` polls and prints new ones (mirror `followLog`'s loop).
- `answerCmd` — `flotilla answer <agent> [--id <id>] "text"`; prints `Answered <agent>` (or `--json`).

- [ ] **Step 1:** Write failing tests: `answer` human + error (no pending) paths; `questions` human + `--json` shape. Reuse the `cli` test helpers that build a `*fleet.Fleet` with a registered agent + session dir; seed a `requests/<id>.json` of `type:"question"`.
- [ ] **Step 2:** Run `go test ./internal/cli/ -run 'Questions|Answer' -v` → FAIL.
- [ ] **Step 3:** Implement both commands; add `questionsCmd(f), answerCmd(f)` to `BuildRoot`'s `AddCommand`.
- [ ] **Step 4:** `go test ./internal/cli/ -v` → PASS.
- [ ] **Step 5:** Commit `feat(cli): flotilla questions + flotilla answer (daemon-independent)`.

---

### Task 5: `flotilla-ask` shim at spawn + ask-awareness prompt preamble

**Files:** Create `internal/fleet/askshim.go`; create `internal/fleet/askshim_test.go`; modify `internal/agent/wrapup.go` (+ test); modify `internal/fleet/fleet.go` (Spawn: install shim + compose hint).

**Interfaces:**
- `const askShimPath = "/usr/local/bin/flotilla-ask"`; `const askShim string` (POSIX-sh, spec §7 — writes `requests/<id>.json` atomically, blocks **indefinitely** on `responses/<id>.json`, then `sed`s the answer to stdout); `func installAskShim(ctx, be, id) error` (mirror `installFetchShim`).
- `const agent.AskHint string`; `func agent.PromptWithAskHint(prompt string) string` — appends a `[Flotilla ask-the-operator]` block (spec §7 wording). Spawn composes `PromptWithAskHint(PromptWithFetchHint(PromptWithWrapUp(...)))`.

- [ ] **Step 1:** Write failing tests: `installAskShim` copies to `askShimPath` + chmods (assert via `fake.CopyCalls`/`ExecCalls`); `askShim` references `containerSessionDir` (drift guard); `Spawn` installs the ask shim; `PromptWithAskHint` appends the block and keeps the user prompt; update the disabled-wrap-up spawn test to also assert `flotilla-ask` presence.
- [ ] **Step 2:** Run `go test ./internal/fleet/ ./internal/agent/ -run 'Ask|AskHint' -v` → FAIL.
- [ ] **Step 3:** Implement shim + hint; wire into `Spawn` after `installFetchShim`.
- [ ] **Step 4:** `go test ./internal/fleet/ ./internal/agent/ -v` → PASS.
- [ ] **Step 5:** Commit `feat(fleet): inject flotilla-ask shim + ask-awareness prompt preamble`.

---

### Task 6: `flotilla-ask` shim script-level test

**Files:** Create `internal/fleet/askshim_script_test.go`.

Run the `askShim` constant through `sh -c` with `sess=/flotilla/session` rewritten to a temp dir (mirror `fetchshim_script_test.go`'s `runShim`). A responder goroutine mirrors each appearing request into a `{"status":"ok","data":{"answer":…}}` response.

- [ ] **Step 1:** Tests: argument-required guard (exit 2, no request written); atomic request write observed; blocks until a response appears then prints exactly the answer text on stdout; a quote/backslash answer round-trips correctly through the greedy `sed`.
- [ ] **Step 2:** `go test ./internal/fleet/ -run AskShim -v` → PASS.
- [ ] **Step 3:** Commit `test(fleet): script-level flotilla-ask shim guard/block/answer paths`.

---

### Task 7: Live Docker integration test (self-skipping)

**Files:** Add to the existing self-skipping Docker path (mirror the fetch live test's skip guard).

- [ ] **Step 1:** Body: spawn a real agent; `docker exec` the agent's `flotilla-ask "Q"` in the background; the host runs `flotilla answer <agent> "A"` (or `Fleet.Answer`); assert the backgrounded `flotilla-ask` exits 0 and printed `A`. Gate behind the same Docker-availability check; `t.Skip` when unavailable.
- [ ] **Step 2:** `go test ./...` (skips locally) / `go test -race ./...` in CI → PASS/SKIP.
- [ ] **Step 3:** Commit `test: live Docker round-trip — ask blocks, answer unblocks the agent`.

---

### Task 8: Docs — backlog, spec status, README

**Files:** Modify `docs/backlog.md`, `docs/specs/2026-06-23-flotilla-agent-question-channel-design.md`, `README.md`.

- [ ] **Step 1:** Backlog: strike item 1 (agent question channel) → Done with links; renumber the remaining items.
- [ ] **Step 2:** Spec: `Status: Draft for review` → `Status: Implemented (2026-06-24)`; link this plan.
- [ ] **Step 3:** README: add `flotilla questions`, `flotilla answer`, the `flotilla-ask` shim and the `blocked` status to the command list / `## Status`.
- [ ] **Step 4:** Commit `docs: agent question/answer channel (flotilla-ask + answer/questions) shipped`.

---

## Final verification

- [ ] `go build ./...` — clean.
- [ ] `go test ./...` — all green (live Docker test self-skips).
- [ ] `golangci-lint run ./...` and `golangci-lint fmt --diff` — clean.
- [ ] `hk run check` — full pre-commit suite green.
- [ ] Manual smoke (if Docker available): spawn, `flotilla-ask "X"` inside the container (blocks), `flotilla questions` shows it, agent is `blocked` in `flotilla list`, `flotilla answer <agent> "Y"` unblocks it and the container prints `Y`; `flotilla inbox` shows `question` then `question_answered`.

## Self-review notes (spec coverage)

- §4 + §4.1 deferred seam → Task 1; `question` handler + blocked + inbox → Task 2. §5 `flotilla answer` → Tasks 3–4. §6 `flotilla questions` + blocked overlay → Tasks 3–4. §7 shim + ask preamble → Task 5. §8 inbox events → Task 2. §9 trust boundary — preserved: handler does a fixed typed thing; `Answer` writes one response scoped to one agent's session dir; no exec. §10 error handling — Tasks 3 (no pending / `--id` / unknown agent), 6 (arg guard), seam (daemon-down still works). §11 testing → Tasks 1–7. §12 sequencing — prerequisites (logs mounts, daemon, fetch) shipped.
