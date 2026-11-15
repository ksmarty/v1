---
type: "Reference"
title: "The pi-durable harness sidecar"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-8944315688730e7dac923731
    resource: repo://cmd/v1/main.go
  - id: openwiki-source-34c7c33a2aa03da6a5cc9305
    resource: repo://internal/harness/bridge.go
  - id: openwiki-source-de1c20948153871b6fb3e7fd
    resource: repo://internal/harness/rpc.go
  - id: openwiki-source-da3b3ac4fbe181ddaa4d7f63
    resource: repo://internal/harness/supervisor.go
  - id: openwiki-source-9a80d30692bcf63390c8c7d7
    resource: repo://internal/server/harness_chat.go
  - id: openwiki-source-c7740d98d388f2bd766d41b0
    resource: repo://sidecar/src/host.js
  - id: openwiki-source-916510df2e066a7deb88b4aa
    resource: repo://sidecar/src/tools.js
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# The pi-durable harness sidecar

v1 keeps ownership of the HTTP API, auth, the store and the tools in Go. Chat
turns, however, run in a **separate Node process** — the pi-durable sidecar —
because the durable agent loop, its transcript and its generation semantics are
that library's, not something to reimplement. The two processes talk over a Unix
socket in newline-delimited JSON-RPC 2.0.

The package comment states the split plainly: *"Go stays the owner of the HTTP
API, auth, the store and the tools: it starts the sidecar, keeps it alive, and
answers the sidecar's tool and approval calls"*
(`internal/harness/rpc.go:1-6`).

| Concern | Owner |
| --- | --- |
| HTTP API, auth, sessions, settings, SQLite store | Go |
| Model calls, generation loop, durable transcript | sidecar (pi-durable) |
| Tool execution and approvals | Go, invoked by the sidecar over `host.call` |
| SSE to the browser | Go, translated from sidecar events |

```
browser ──SSE──▶ Go server ──JSON-RPC──▶ sidecar (node)
                    ▲   │                   │
                    │   └──tool.call────────┤
                    └──────ToolResult───────┘
```

## Protocol

The transport is in `internal/harness/rpc.go`, the lifecycle in
`supervisor.go`, and the typed calls in `bridge.go` — *"the transport and the
process lifecycle are deliberately separate: this file only knows the
protocol"* (`internal/harness/bridge.go:1-7`).

- One JSON object per line over a Unix socket; `maxLine = 32 << 20`
  (`internal/harness/rpc.go:29`) caps a single line, which is generous because
  tool results and event batches are the largest payloads.
- `ProtocolVersion = 1` (`internal/harness/rpc.go:24`) is reported by both sides
  in the handshake. A mismatch is a startup failure, which is safe because the
  server and the sidecar ship in the same image — they can only disagree after a
  partial upgrade.
- Writes are serialized behind a mutex; one reader goroutine dispatches
  responses to waiting callers and inbound messages to the `Handler`
  (`internal/harness/rpc.go:324`, `:345`).
- The `Handler` contract carries an important constraint: requests
  (`host.call`, approval prompts) may block on the user, but notifications
  (event batches) arrive in order on the read loop, so **a slow notification
  handler stalls every later message** (`internal/harness/rpc.go:68-72`). That is
  why event pushes go into a queue rather than being processed inline.

### Methods Go calls on the sidecar

| Method | Go client | Purpose |
| --- | --- | --- |
| `harness.ready` | `Supervisor.handshake`, `internal/harness/supervisor.go:278` | handshake + version check |
| `ping` | `Bridge.Ping`, `internal/harness/bridge.go:416` | liveness only; answers even mid generation |
| `conversation.ensure` | `Bridge.Ensure`, `internal/harness/bridge.go:406` | register or reconfigure one chat session's conversation |
| `conversation.entries` | `Bridge.Entries`, `:437` | durable transcript pages |
| `conversation.rewind` / `conversation.fork` | `:499`, `:513` | rewind to a user turn; fork into a new conversation |
| `watch.start` / `watch.stop` | `Bridge.Watch`, `:452` | subscribe to an event stream |
| `turn.submit` / `turn.steer` / `turn.followUp` | `Bridge.Submit`, `:480`; `Steer`, `:535` | start a turn, inject mid-turn, or attach a follow-up |
| `turn.abort` / `turn.compact` | `:550`, `:557` | stop generating; compact the transcript |
| `extensions.list` / `extensions.reload` | `:569`, `:584` | the loader described in [Extensions and skills](./extensions-and-skills.md) |
| `harness.shutdown` | `Bridge.Shutdown`, `:562` | graceful stop |

The sidecar routes the same set through one `dispatch` switch
(`sidecar/src/host.js:953`), and an unknown method is a JSON-RPC
`-32601`, not a crash.

### Methods the sidecar calls on Go

`Bridge.Handle` (`internal/harness/bridge.go:264`) answers four:

- **`event`** — an event batch for a subscription. It is pushed into that
  subscription's `EventQueue` and returns immediately, because it runs on the
  read loop.
- **`tool.call`** — the sidecar asks Go to run a tool. The call is routed through
  `Bridge.runner(conversationID)` (`:352`) to the `ToolRunner` that registered for
  that turn. With no runner the answer is a `ToolResult` with
  `"no active turn for tool <name>"` and `IsError: true`, plus a log line: from
  the model's side an abandoned turn looks exactly like a tool that mysteriously
  fails, and every call it makes reads the same.
- **`tool.authorize`** — the approval prompt. A refusal is returned as
  `{"block": reason}` rather than a bridge error, because the sidecar records the
  intent durably and the model must see a blocked call, not a transport failure.
- **`delegate.attach`** — a delegated child runs in a pi conversation of its own,
  and pi hands a tool the conversation it is running in, so the child's tool
  calls carry an id no runner was registered under. The sidecar asks for the alias
  explicitly instead of relying on having rewritten the id, and
  `Bridge.Attach` (`:366`) records child → parent so a call resolves whichever id
  it arrives with. Aliases are dropped when the parent turn's
  `Register` cleanup function runs (`:386`), since they only mean anything while
  that turn is running.

## Process lifecycle

`cmd/v1/main.go:88-103` starts the sidecar **before the server accepts
traffic**, and a failure is fatal with a fix in the message:

```
The pi-durable sidecar is the default chat harness. Install its
dependencies with `make sidecar-deps`, or set V1_HARNESS=go to run the
built-in agent loop instead.
```

Silently falling back to the Go loop would hide that the requested harness is not
running, so it is not an option. `startHarness` (`cmd/v1/main.go:146`) passes the
socket, the pi-durable database path, the extensions directory under the data
volume, the client version and the restart budget.

`Supervisor.Start` (`internal/harness/supervisor.go:142`) then:

1. creates the socket directory and calls `refuseIfRunning` (`:167`). Only one
   process may own the durable store, so a second one is refused with
   `ErrAlreadyRunning` (`:18`) rather than allowed to corrupt it. An orphan whose
   parent died closes its own stdin and exits, so a short probe window (250 ms
   steps, `probeTimeout = 3s`) distinguishes "orphan still shutting down" from
   "another server owns this socket".
2. removes a stale socket file left by a crash — otherwise the spawn would dial a
   dead socket.
3. `spawn` (`:194`): `exec.Command` with an environment built as a fresh slice
   rather than appended to the caller's (`append` would reuse the backing array
   and silently overwrite whatever followed it), carrying
   `V1_SIDECAR_SOCKET`, `V1_HARNESS_DB` and `V1_EXTENSIONS_DIR`.
4. opens a **stdin pipe it never writes to**. The child watches its stdin, so when
   v1 dies the pipe closes and the sidecar exits instead of lingering with the
   store open (`sidecar/src/host.js:1157-1163` handles `end`, `close`, `SIGTERM`,
   `SIGINT`).
5. waits for the socket, dials, handshakes, and records the connection.
6. starts `watch`.

### Restart

`watch` (`internal/harness/supervisor.go:319`) waits on the child, and restarts
with exponential backoff: `backoff = RestartBackoff << (consecutive - 1)` capped
at `maxRestartBackoff = 30s`, up to `MaxRestarts` consecutive attempts. Zero
means a single attempt and no restarts. Two details make the budget mean what it
says:

- a sidecar that stayed up for `RestartHealthyAfter` (60 s) before dying resets
  the counter, so an isolated crash does not count against a crash loop;
- a *deliberate* restart resets it too — "a deliberate restart is not a crash:
  hand it a full budget so it cannot trip the give-up path"
  (`internal/harness/supervisor.go:431`, under `restartMu` (`:432`) so two turns hitting
  the same problem at once cannot kill each other's fresh sidecar).

When the budget is exhausted the supervisor logs `sidecar crashed %d times in a
row; giving up (chat turns will fail)` and closes its stopped channel.

### Self-healing restart for a poisoned session

A session poisoned by a failed storage commit answers *every* later call with
`"Session is poisoned by a failed commit after storage admission; reopen it"`,
and the storage object is permanently unusable — reopening means a fresh process.
`Supervisor.Call` (`:398`) therefore recognises that specific failure and
restarts once, then retries:

```
harness: <method> hit a poisoned session; restarting the sidecar
```

`isPoisonedSession` (`:425`) matches the message because the JSON-RPC code is a
generic `-32000`; it is matched loosely, and it only ever triggers
restart-and-retry, so an upstream wording change costs a retry rather than
correctness. `Restart` drops `s.conn` *before* killing the process — the read loop
only notices the dead socket asynchronously, so waiting for "a connection exists"
would hand back the dying one and the retry would race the kill — and then waits
for the watch loop to bring up a new process instead of spawning a second one.

### Shutdown

`cmd/v1/main.go:113-142` waits for running turns (`srv.WaitForTurns(10*time.Minute)`)
before tearing anything down, because the sidecar owns the turn and cutting it off
would leave a truncated answer and an error row. Then previews and terminals stop,
HTTP drains, and the sidecar is closed before the store, since it holds the
durable transcript open.

## The two generation hooks

Both live in `sidecar/src/tools.js` and both exist because pi-durable classifies a
finished generation by its stop reason, which providers do not always set the way
the loop needs.

### Continuation: a round with no visible text is not a finished turn

`continuationMessage(content, stopReason)` (`sidecar/src/tools.js:113`) returns
null for a finished answer and a prompt otherwise. The rule:

- a round containing a `toolCall` part is never continued;
- a round whose stop reason is not `length` **and** which produced visible text
  is done;
- `stopReason === "length"` gets *"You reached the output limit while thinking.
  Do not reason further — answer the user's last message directly and concisely
  now."*
- otherwise a thinking-only round gets *"Continue from where you left off. Do not
  repeat what is already written above."*, and a round with nothing at all gets
  *"You stopped without answering. Answer the user's last message directly and
  concisely now."*

Without it, a reasoning model that fills its output window with thinking, or that
returns nothing right after a tool call such as `remember`, reads to the user as
"the chat stopped midway for no reason".

`buildContinuationHook` (`sidecar/src/tools.js:165`) installs it on
`onYield`: `{ continue: message }` appends a user message and hands the run to a
successor generation, which is how the continuation stays durable — the same shape
the built-in loop's truncation resume uses. It is **bounded**, because pi-durable's
per-task memo resets when a continuation hands the run to a successor and so cannot
count these: a process-local map (`continuationAttempts`, `sidecar/src/tools.js:104`)
allows `MAX_CONTINUATIONS = 3` within a `CONTINUATION_WINDOW_MS = 5 * 60 * 1000`
window, then logs `continuation: still unfinished after several retries; keeping
the partial reply` and lets the turn end. The map is swept past 500 entries so a
long-lived process does not leak per-conversation state.

### Promotion: a tool call the provider did not mark as a tool round

pi-durable starts a tool round only for exactly `stopReason === "toolUse"`. A
provider that ends a tool-calling round with `"length"` (it ran out of output
while writing the call) or `"stop"` leaves the library nothing to run: the call is
committed and then never executed, and the turn ends with a tool call and no
result — which the model itself reads as "the tool keeps getting interrupted".

`promoteToolRound(message)` (`sidecar/src/tools.js:142`) rewrites such a message's
stop reason to `"toolUse"` and returns true. It runs in `afterResponse`
(`sidecar/src/tools.js:169`), i.e. **before** pi-durable classifies the message,
and logs `tool round: running a tool call the provider did not mark as toolUse`.
An `"error"` stop reason is deliberately left alone: that path has its own
retry and compaction handling.

## Stall and recovery guards

Two independent failures are guarded on the Go side of the stream.

**A user stop must reach the sidecar.** When the turn's context is cancelled and
the turn was not already aborted, the loop calls `bridge.Abort(sidecarID)` on a
fresh 10 s context (`context.WithoutCancel`), because the turn's own context is
already dead (`internal/server/harness_chat.go:1250`). Without that call the
sidecar keeps generating into a transcript nobody is watching, burning tokens and
durable entries. The partial output is persisted and the turn returns
`context.Canceled`.

**A sidecar that stops answering must not pin the session.** `Drain` is bounded at
250 ms so steering can be polled mid-turn; a drain timeout is not silence, though —
a tool running in Go (a build, an install) and a provider that has produced no
token yet both keep the sidecar quiet for minutes. So the watchdog asks the
runtime instead (`internal/server/harness_chat.go:1285`):

| Constant | Value | Meaning |
| --- | --- | --- |
| `harnessStallTimeout` | 60 s | quiet for this long with nothing in flight is suspicious |
| `harnessPingTimeout` | 5 s | a runtime that cannot answer `ping` is gone |
| `harnessStallCeiling` | 10 min | alive but silent this long ends the turn anyway |

`stallWatch` (`internal/server/harness_chat.go:956`) tracks `lastActivity` (reset by
anything, including a Go-side tool), `quietSince` (reset only by the runtime
speaking) and `announced`. A ping failure gives up with "the agent stopped
responding (no activity for …)"; a live runtime past the ceiling gives up with
"the agent produced nothing for …"; between the two the loop logs once that the
runtime is alive but has said nothing, which is the difference between a wedged
runtime and a slow model.

`harnessPingTimeout` and the per-user soft/hard turn timeouts are separate knobs:
the soft/hard pair (5 and 10 minutes by default, `internal/config/config.go:81-82`,
overridable per user in `internal/server/server.go:772`) bounds the built-in Go
agent loop, not the sidecar.

## Invariants

| Invariant | Where it is enforced |
| --- | --- |
| Only one process owns the durable store | `refuseIfRunning`, `internal/harness/supervisor.go:167` |
| A protocol mismatch fails startup, never a first turn | `handshake`, `internal/harness/supervisor.go:278` |
| The sidecar dies when v1 dies | stdin pipe, `internal/harness/supervisor.go:222` + `sidecar/src/host.js:1157` |
| An event notification never blocks the read loop | queue push in `Bridge.Handle`, `internal/harness/bridge.go:283` |
| A tool call with no runner is an error result, never a hang | `Bridge.Handle`, `internal/harness/bridge.go:322` |
| A deliberate restart never counts against the crash budget | `Restart`, `internal/harness/supervisor.go:431` |
| A poisoned session is reopened rather than left dead | `Supervisor.Call`, `internal/harness/supervisor.go:398` |
| A turn that ends with no visible text is continued, bounded to 3 | `buildContinuationHook`, `sidecar/src/tools.js:165` |
| A tool call the provider left as `stop`/`length` still runs | `promoteToolRound`, `sidecar/src/tools.js:142` |
| A stopped turn is aborted in the sidecar | `internal/server/harness_chat.go:1250` |
| A silent sidecar cannot hold a session forever | `stallWatch` + `ping`, `internal/server/harness_chat.go:934-988` |
