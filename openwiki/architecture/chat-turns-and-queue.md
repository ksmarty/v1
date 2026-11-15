---
type: "Reference"
title: "Chat turns, queue, steer and the live stream"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-2fcc0ac15ad0af123578c81f
    resource: repo://internal/server/handlers_chat.go
  - id: openwiki-source-28aea8e51962a1087b393fba
    resource: repo://internal/server/handlers_sessions.go
  - id: openwiki-source-9a80d30692bcf63390c8c7d7
    resource: repo://internal/server/harness_chat.go
  - id: openwiki-source-7d4ac4eaf17b216fdc5fd692
    resource: repo://internal/server/server.go
  - id: openwiki-source-7be0e406371d03d1733b7d8f
    resource: repo://internal/server/sse.go
  - id: openwiki-source-3976b33293280ed661476d0a
    resource: repo://internal/server/turns.go
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Chat turns, queue, steer and the live stream

A v1 chat is a sequence of turns against one session. Each turn is one run of
the agent loop, detached from the HTTP connection that started it, streamed as
Server-Sent Events, and persisted round by round so the transcript survives a
disconnect, a crash, or the app being backgrounded. Messages sent while a run is
active queue up server-side and can be steered into the run, held, edited,
reordered or deleted.

The tool surface the loop calls is in
[Agent tools, tool results and events](./agent-tools-and-events.md); the sidecar
that actually executes the loop is
[The pi-durable harness sidecar](./harness-sidecar.md); the rows this page writes
belong to [Store, sessions and settings](./store-sessions-and-settings.md).

## One run per session

`turnManager` (`internal/server/turns.go:379`) holds at most one active run per
`(project, session)` pair, keyed by `runKey` (`internal/server/turns.go:248`). A
run is a `runState` (`internal/server/turns.go:370`) bundling its `turnQueue`,
its `runHub` and the owning `userID`.

`beginOrQueue` (`internal/server/turns.go:461`) makes the start atomic. Either a
run is already active and the message is appended to its queue
(`started=false`), or the caller takes ownership of a fresh run
(`started=true`) and executes it. Doing both under one lock is what stops a
message from being lost against a run that ended a moment earlier — the
alternative, "check then start", has a window in which the run ends and the
message is appended to a queue nobody will ever drain.

Two guards ride along:

- A message with no text but with attachments still queues, because sending a
  screenshot with no comment is a complete turn.
- `errTooManyRuns` (`internal/server/turns.go:452`) refuses a new run once the
  user has `maxRuns` active ones, so one account cannot saturate the host.
  `maxRuns <= 0` disables the cap.

`cancelRun` (`internal/server/turns.go:585`) and `end` (`internal/server/turns.go:601`)
release the slot; `running` (`internal/server/turns.go:547`) answers the status
endpoint.

## The queue

`turnQueue` (`internal/server/turns.go:36`) keeps two ordered slices:

- `pending` — follow-up turns, drained in order once the current turn finishes.
- `steers` — messages explicitly steered into the running turn, consumed at the
  next round boundary.

Each entry is a `queuedMsg` (`internal/server/turns.go:21`) with an id, text,
`Held` flag, timestamp and its `Attachments` — a queued or steered follow-up can
carry files exactly like a fresh turn, so attachments must survive a reorder and
the drain.

`persist` writes the whole queue through to the store after **every** mutation
(`persistLocked`, `internal/server/turns.go:49`), so a provider failure, a
restart or a long absence cannot drop a message the reader already sent. The hook
is nil for a queue with no store backing (tests), which degrades to memory-only.

| Operation | Method | Effect |
| --- | --- | --- |
| add | `add` (`:61`) | appends to `pending`, returns the id |
| list | `list` / `listSteers` (`:71`, `:80`) | follow-ups / steers in order |
| remove | `remove` (`:90`) | drops one entry, `errNotQueued` when absent |
| reorder | `reorder` (`:105`) | reorders by an id list, copying whole messages |
| edit | `edit` (`:133`) | replaces the text of one entry |
| hold | `hold` (`:149`) | a held message is skipped by the drain |
| steer | `steer` (`:187`) | moves one message from `pending` into `steers` |
| drain | `drain` (`:221`) | returns the next runnable follow-up |
| steerDrain | `steerDrain` (`:203`) | returns everything waiting to be injected |

`remaining` (`:179`) is what the `done` event's `more` flag is computed from, and
`heldCount` (`:163`) keeps a held message from being counted as pending work.

The HTTP surface is one endpoint per operation: `GET /api/projects/{id}/chat/queue`
plus `POST …/chat/queue/{reorder,steer,edit,hold,delete,resume}`
(`internal/server/server.go:230-245`).

`queueFor` (`internal/server/turns.go:498`) is what makes the queue survive more
than one run: when nothing is active it hydrates a detached queue from the store,
so messages left behind by a failed run can still be listed and edited — and
those edits persist.

## The turn stream

`handleChat` (`internal/server/handlers_chat.go:301`) decodes the request and
handles uploads: the body carries `uploadIds` rather than bytes, and the uploaded
files are turned into attachments before the turn starts. A turn needs text *or*
an attachment. The body is capped by `maxChatBody = 256 << 20`
(`internal/server/handlers_chat.go:38`).

`streamChatTurn` (`internal/server/handlers_chat.go:566`) owns the stream. It:

- opens the SSE response (`text/event-stream`, `no-cache`, `X-Accel-Buffering: no`)
  and `defer`s `s.turns.end`, so the run slot is released even on a panic;
- **detaches the run from the client**. Navigating away, backgrounding the app
  or closing the tab must not kill a running turn, and there is no per-turn
  timeout — long generations run to completion. The cancel function is registered
  with the manager so the stop endpoint can still abort it explicitly;
- tags every event with the run's `TurnID` and publishes it to the hub before
  writing it, so watchers see the event at the same instant the caller does;
- treats one failed write as death: a `writeDead` flag stops all further frames
  for that stream while the run itself keeps going.

Every frame is written under `sseWriteDeadline = 10 * time.Second`
(`internal/server/sse.go:25`) and a `: ping` comment is sent every
`sseHeartbeat = 15 * time.Second` (`internal/server/sse.go:14`), so a client that
hears nothing knows the connection died rather than the model being slow.

### Event vocabulary

| Event | Meaning |
| --- | --- |
| `delta` | streamed assistant text |
| `reasoning` | streamed thinking text |
| `tool_start` / `tool_end` | a tool call begins / returns (with the display payload) |
| `injected_message` | text inserted mid-turn: a steer, a background result, a screenshot caption |
| `notice` / `info` | a user-facing explanation (compaction, auto-retry) |
| `context` | updated context-window usage |
| `error` | the turn failed |
| `done` | the turn finished; `more: true` means follow-up messages are still queued |
| `run_end` | the sidecar finished the generation (harness path only) |

`done` is emitted from `internal/server/handlers_chat.go:845` as
`{Type: "done", Usage: turn.Usage, More: more}` where
`more := q.remaining() > 0` (`internal/server/handlers_chat.go:839`). The client
uses `more` to decide whether to drain the next queued turn immediately.

## Reattaching: `/chat/watch`

A client that comes back to a running chat must see the rest of the run without
re-reading the whole transcript. `handleChatWatch`
(`internal/server/handlers_chat.go:898`) subscribes to the run hub, which keeps a
512-event ring buffer of recent events plus the accumulated `text` and
`reasoning` so far (`runHub`, `internal/server/turns.go:255`).

`subscribe` (`internal/server/turns.go:292`) replays that ring first and returns
how many replayed frames are already buffered; the handler then writes a
`{"type":"replay_count","count":N}` marker ahead of them. The count is what lets
the client converge on the store as soon as the replay ends instead of waiting
for the run to finish — without it, watching a turn from the middle re-reads the
run's history and keeps duplicate rows until the end.

The ring buffer deliberately never clears `text` on a new round
(`record`, `internal/server/turns.go:274`): a tool call does not erase what the
assistant already said, and the model may keep appending after the tool result.

## Steering and the injection invariants

Steering injects a message into a run that is already going. It happens at a
round boundary, and three invariants keep the transcript honest.

**A mid-round injection lands after the streamed text.** `flushRound`
(`internal/server/harness_chat.go:1065`) persists the text and reasoning the round
has streamed so far as their own assistant row *before* the steer is written,
because store ids are the transcript's order and the client sorts by them:

```go
pendingInjections[reqID] = true
flushRound()
msgID, addErr := s.st.AddInjectedMessage(… agent.SteerToolJSON …)
emit(agent.ChatEvent{Type: "injected_message", …, ToolJSON: agent.MarkerName(agent.SteerToolJSON)})
```

The same rule applies to the built-in loop and to background results
(`pollBackground`, `internal/server/harness_chat.go:1180`).

**A steer that cannot be delivered goes back on the queue.** The message was
already drained, so a failed `deliver` calls `params.Requeue(msg)`
(`internal/server/harness_chat.go:1140-1150`) and it runs as a follow-up turn
instead. A message the reader sent never vanishes.

**An injected message keeps `run_end` from ending the turn.** The run that just
ended is not always the turn's last work — a steer delivered mid-round runs as
its own generation afterwards. The loop therefore only breaks on
`sawRunEnd && len(pendingInjections) == 0`
(`internal/server/harness_chat.go:1229`). Ending on `run_end` alone orphaned the
answer: it streamed to no consumer, never reached the transcript, and the steered
message read as ignored forever — and because the store's last id then matched
the reader's stale cache, the missing answer survived every reload too.

`sawRunEnd` with injections still outstanding after
`harnessSteerGrace = 90 * time.Second` (`internal/server/harness_chat.go:998`) of
silence does end the turn, logging how many went unanswered
(`internal/server/harness_chat.go:1232`), so a sidecar that will never answer
cannot hang the run.

**A turn v1 abandons must be aborted in the sidecar.** `giveUp`
(`internal/server/harness_chat.go:1204`) persists the partial, then calls
`bridge.Abort` with a 10-second timeout taken from
`context.WithoutCancel(ctx)` — the abandoning context is already cancelled, so a
plain child context would be dead on arrival. A turn v1 abandons but the sidecar
keeps running is worse than the stall that caused it: the model's every later
tool call reaches a turn that no longer exists, reads "no active turn for tool",
and the chat shows it failing the same way over and over.

The `submission` event closes the loop: a pending injection is retired when the
sidecar reports it `done` (answered) or `unanswered` (terminal)
(`internal/server/harness_chat.go:1349-1353`).

## Persistence, partials and continuation

Nothing is buffered until the end. `persist` (`internal/server/harness_chat.go:1032`)
writes one assistant row per round — text, tool calls, reasoning and that round's
usage — then resets the accumulators. The usage matters: the context meter reads
the newest assistant row's recorded context, and without it the meter falls back
to counting stored rows, which on the harness path never shrinks when older
entries are folded into a summary.

Flushed text is tracked separately (`flushed`, `flushedReasoning`) so the
`message_end` reconciliation (`internal/server/harness_chat.go:1356-1362`) only
writes the part no flush has written yet. The committed entry carries the whole
round; re-streaming its flushed prefix would put the same words in two rows.

A turn that produced neither text nor a tool call is an error, not a silent
`done` (`internal/server/harness_chat.go:1507`): a reasoning model that spent its
whole output window thinking used to commit a reasoning-only entry, so the reader
saw a thinking block and then nothing.

`continuedPartial` (`internal/server/handlers_chat.go:533`) finds the assistant
row to continue from after a failure: the transcript must end in an `error` row,
the row before it must be a non-empty assistant message written after the user's
message. A retry is `POST …/chat/retry` (`handleChatRetry`,
`internal/server/handlers_chat.go:462`).

## Stopping

`handleChatStop` (`internal/server/handlers_chat.go:986`) cancels the run and, in
the same call, cancels the detached commands that session started
(`background.CancelSession`) — a stop should not leave an orphaned build or dev
server behind. Stop works even though the run is detached from its connection;
that is exactly why the endpoint exists instead of relying on the client aborting
its stream.

## Forking and renaming

`handleForkSession` (`internal/server/handlers_sessions.go:59`) copies a session's
transcript into a new session and forks the model's durable conversation in the
sidecar, recording the new conversation id on the fork — otherwise the fork's
first turn would reach the model with no history. A fork while a turn is running
is refused with `run_active` (`409`), because the copy would catch the sidecar
mid-write. A failed durable fork only logs: the v1 transcript is still a correct
fork. Rename is `handleRenameSession` (`internal/server/handlers_sessions.go:103`).

## Status

`handleChatStatus` (`internal/server/handlers_chat.go:967`) reports
`{"running": bool, "lastMessageId": n}`. `lastMessageId` is what lets the client
skip a transcript reload when nothing was written while the app was in the
background — the needless reload was what made a quick app switch flash the whole
chat.

## Invariants

| Invariant | Where it is enforced |
| --- | --- |
| One active run per session; a message either starts a run or joins one queue, atomically | `beginOrQueue`, `internal/server/turns.go:461` |
| The queue is written through to the store on every mutation | `persistLocked`, `internal/server/turns.go:49` |
| A run survives the client connection closing; only an explicit stop ends it | `streamChatTurn`, `internal/server/handlers_chat.go:566` |
| A reattaching client converges on the store at the end of the replay | `replay_count` marker, `internal/server/handlers_chat.go:933` |
| A mid-round injection lands after the streamed text | `flushRound` before `AddInjectedMessage`, `internal/server/harness_chat.go:1146-1166` |
| A steer that cannot be delivered returns to the queue | `params.Requeue`, `internal/server/harness_chat.go:1140-1149` |
| Injected messages keep `run_end` from ending the turn | `sawRunEnd && len(pendingInjections) == 0`, `internal/server/harness_chat.go:1229` |
| A turn v1 abandons is aborted in the sidecar | `giveUp`, `internal/server/harness_chat.go:1204` |
| Each round is persisted with its own usage; flushed text is never written twice | `persist`, `internal/server/harness_chat.go:1032` |
