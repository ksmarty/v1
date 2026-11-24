# Chat audit handoff

## Objective and repository rules

The user asked for the chat section to be audited, issues documented, and the issues fixed. The issue inventory is in `docs/chat-audit.md`. Keep changes scoped to confirmed correctness defects; do not treat this as a redesign. Do not touch `data/`, install dependencies, or rewrite git history. After successful checks, rebuild/restart locally with `V1_AUTH_DISABLED=true`, report the stamped version/listener/auth state, then commit and push `main` (pre-authorized by `AGENTS.md`).

## Implemented and verified

All 20 issues in `docs/chat-audit.md` are implemented and covered by unit/integration tests:

- `internal/server/handlers_chat.go`: edit checks active run before mutation; cross-session edit is rejected; truncate rejects active runs; held queues are drained again after release; persisted error/final done events carry durable `messageId`.
- `internal/server/handlers_queue.go`: failed durable deletion returns 500; resume does not enqueue a second copy after losing the start race.
- `internal/server/handlers_ask.go`: ask responses validate `(projectID, sessionID, requestID)` before resolving or clearing pending durable asks.
- `internal/server/harness_chat.go` and `internal/harness/bridge.go`: steering reuses normal attachment content parts; `harnessKeepUserTurns` excludes screenshot-injected `agent.AgentToolJSON` rows so retries rewind accurately.
- `internal/store/store.go`: added `GetMessageInSession(projectID, sessionID string, id int64)`.
- `sidecar/src/delegate_answer.js` and `sidecar/src/host.js`: delegate answer reading follows every transcript cursor page; `turn.compact` awaits `harness.waitForTask` and fails on non-completed outcomes; `harness.resume()` is deferred until `harness.ready` after the Go bridge peer attaches.
- `sidecar/src/tools.js`: `buildContinuationHook()` clears `continuationAttempts` when a turn produces a finished visible answer so subsequent turns get their full retry budget.
- `web/src/api.ts`: `queueChat` returns either a 202 queue result or the original SSE `Response` as `{stream}`, and `streamChatResponse` consumes that response directly.
- `web/src/components/ChatPane.tsx`: detached-run busy/stop handling; stream ownership generation for the queue fallback; serialized queue holds and duplicate-save suppression; single status polling chain; durable notification ids; attachment metadata on optimistic steering.
- `web/src/chatUtils.ts`: full transcript fingerprinting and readable `innerText` tool errors.
- Tests: `internal/server/chat_mutation_test.go`, `internal/server/turns_test.go`, `internal/server/harness_chat_test.go`, `sidecar/test/delegate-answer.test.mjs`, and `web/scripts/chat-utils.test.mjs`.

## Validation and finish

```sh
gofmt -w internal/harness/bridge.go internal/server/handlers_chat.go \
  internal/server/handlers_queue.go internal/server/harness_chat.go \
  internal/server/turns_test.go internal/server/chat_mutation_test.go \
  internal/store/store.go
go test ./internal/server ./internal/harness
cd web && npm test && npm run build && cd ..
go test ./...
cd sidecar && npm run check && npm test && cd ..
```

`go test ./...` had unrelated `internal/usage` cache failures at the clean baseline; preserve the full output and distinguish any still-identical failures from regressions. The baseline frontend build passed. Run `go build ./...` and `make build` after source changes. Exercise the running chat UI for normal send, queued send/edit, stop of a detached run, and a same-length refreshed edit. Do not claim visual verification without actually opening the app.

Update `docs/chat-audit.md` statuses to reflect only verified fixes, keep this handoff current if work is interrupted, then rebuild/restart auth-disabled, report `v1 <version> (<commit>) listening on :8080` plus auth state, and commit/push only after checks pass.
