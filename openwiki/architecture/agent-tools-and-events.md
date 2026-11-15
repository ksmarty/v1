---
type: "Reference"
title: "Agent tools, tool results and events"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-a5516410306ad363ccb348a3
    resource: repo://internal/agent/agent.go
  - id: openwiki-source-5bb9f6441b51246e35b50516
    resource: repo://internal/agent/attachments.go
  - id: openwiki-source-4c708286eedd9cd61c97a578
    resource: repo://internal/agent/background.go
  - id: openwiki-source-f37313ed2d1728abb1bf11a7
    resource: repo://internal/agent/image.go
  - id: openwiki-source-1d3a5eba2e9274efa7e31523
    resource: repo://internal/agent/markers.go
  - id: openwiki-source-8ef78abf8bdec0ce0bf604d2
    resource: repo://internal/agent/perms.go
  - id: openwiki-source-dc8ff05d99ba4de7877a87e7
    resource: repo://internal/agent/tools.go
  - id: openwiki-source-0cd0dfc7937ecf4edb41365d
    resource: repo://internal/agent/toon.go
  - id: openwiki-source-9a80d30692bcf63390c8c7d7
    resource: repo://internal/server/harness_chat.go
  - id: openwiki-source-bdac1941a583a38762a06fdb
    resource: repo://web/src/components/ChatPane.tsx
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Agent tools, tool results and events

A v1 turn is a model call that may ask to run tools. This page covers the tool
surface offered to the model, what a tool returns, how that result is reshaped
for the model (TOON) and for the transcript (display payloads and the
`tool_start` / `tool_end` events), and the side channels a tool can use:
attachments, `ask_user`, the permission resolver, and background commands.

The turn itself — queueing, streaming, persistence, steering — is
[Chat turns, queue, steer and the live stream](./chat-turns-and-queue.md). How
the sidecar runs the loop that calls these tools is
[The pi-durable harness sidecar](./harness-sidecar.md).

## The tool surface

Tool schemas are hand-written `llm.Tool` values in `internal/agent/agent.go`,
assembled by `func (p ChatParams) ToolSet() []llm.Tool` (`internal/agent/agent.go:133`).
Each entry carries a JSON-Schema `parameters` object and a description written
for the model, so the tool list is the model's contract with the runtime.

| Group | Tools | Notes |
| --- | --- | --- |
| Files | `list_files`, `search_files`, `read_file`, `write_file`, `edit_file`, `delete_file`, `move_file` | Paths resolve through `resolve(rel)` (`internal/agent/tools.go:1354`), which refuses anything escaping the project workspace. `skippedDirs` (`internal/agent/tools.go:1394`) hides `node_modules`, `.git` and `dist` from listings and searches. |
| Commands | `run_command`, `run_command_background`, `run_container`, `git` | Working directory is the project. A detached `run_command_background` returns a job id and reports completion later (see below). |
| Preview | `restart_preview`, `screenshot_app` | Both need the preview manager; `screenshot_app` is only offered to vision-capable models. |
| Plan and todos | `make_plan`, `update_plan`, `set_todos` | Writes the active plan / todo list shown in the UI. |
| Session | `set_project_name`, `set_session_name`, `ask_user` | `ask_user` blocks on the user (see below). |
| Memory | `remember`, `forget`, `search_memories` | Project-scoped, capped at `maxMemoriesPerProject = 2000` (`internal/agent/tools.go:504`). |
| Web | `fetch_url`, `web_search` | `validateFetchURL` (`internal/agent/tools.go:1859`) is the SSRF guard; `web_search` is only offered when a search key is configured. |
| Extensions | `create_extension`, `create_skill`, `list_extensions` | Module source over `inlineSourceLimit = 8192` bytes must be passed by path. See [Extensions and skills](./extensions-and-skills.md). |
| Verification | `verify_project` | Output is capped at `maxVerifyOutput = 32 << 10` (`internal/agent/tools.go:44`). |
| Delegation / MCP | `delegate` (a builtin extension) and any configured `mcp__*` tools | `mcpCall` (`internal/agent/tools.go:467`) dispatches MCP tools. |

Two filters narrow that list before it is sent:

- **Plan mode.** `planSafeTools` (`internal/agent/tools.go:174`) drops every tool
  in `planBlockedTools` (`internal/agent/tools.go:153`) — all writes, command
  execution, git, containers, preview restarts, todos, renames, memory writes and
  extension/skill installs. The model can still investigate.
- **Capabilities.** Vision, web search and MCP entries are only added when the
  model, the settings and the configured servers allow them.

## Running a call

`Executor.ExecuteWithDisplay(ctx, name, argsJSON) (string, json.RawMessage, error)`
(`internal/agent/tools.go:197`) is the single entry point: it switches on the
tool name in `execute` (`internal/agent/tools.go:365`), returns the
model-visible result string plus an optional display payload, and is wrapped by
`Execute` (`internal/agent/tools.go:188`) for callers that do not care about
presentation.

Failures are structured, not plain strings. `toolFail(t, msg string, recoverable bool, suggestion string)`
(`internal/agent/tools.go:963`) builds a `ToolError` (`internal/agent/tools.go:959`)
that carries whether the model can retry and a suggested next step, and
`formatToolError` (`internal/agent/agent.go:1529`) renders it for the tool
message so a failure teaches the model what to do differently instead of just
ending the call.

## What the model sees: TOON

Tool results are stored as JSON because the browser renders them, but the copy
placed in front of the model is re-encoded as TOON (Token-Oriented Object
Notation). `toonJSON` (`internal/agent/toon.go:99`) returns its input unchanged
when it is not valid JSON, and `toonEncode` (`internal/agent/toon.go:106`) writes
two-space indentation with a comma delimiter. Objects decode through `tField`
(`internal/agent/toon.go:17`) so key order survives — Go map iteration would
otherwise scramble the field order TOON depends on. Encoding only: there is no
decoder, because nothing ever parses TOON back.

The encode happens at both points where a result enters history — the live call
(`internal/agent/agent.go:802`) and the replay of stored history
(`internal/agent/agent.go:453`):

```go
content := result
if p.ToonEnabled {
    content = toonJSON(result)
}
```

`ToonEnabled` is a setting rather than a constant, so it can be turned off for
a model that behaves worse with the compact form.

Two guards keep a long conversation from exhausting the context:

- `elideHistoricalToolResult` (`internal/agent/agent.go:854`) replaces a
  replayed result larger than `maxHistoricalToolResult = 256` bytes
  (`internal/agent/agent.go:252`) with `[<tool> result omitted — N bytes; re-read
  the file if you need it again]`. The model already acted on it and the
  filesystem is the live source of truth.
- `stripBrokenToolCalls` (`internal/agent/agent.go:871`) drops tool calls whose
  arguments did not finish streaming (truncation leaves invalid JSON, which
  upstream rejects with a hard 400 on every later request) and returns their
  names so the app can say why a call never ran.

## What the browser sees: display payloads

The raw result is often unusable as UI: `ask_user` returns answers, `list_files`
returns an array, `create_extension` returns a paragraph. `toolDisplay(name, out)`
(`internal/agent/tools.go:208`) builds a small `{kind, …}` JSON object for the
tools that deserve a card, and returns `nil` for the rest:

| `kind` | Produced for | Contents |
| --- | --- | --- |
| `answers` | `ask_user` | per-question answer, options, free text, optional global note |
| `files` | `list_files` | count, the file list, `truncated` |
| `page` | `fetch_url` | title/url/summary so a large fetch does not double its weight |
| `results` | `web_search` | the hit list |
| `extension` / `skill` | `create_extension`, `create_skill` | version, changelog and the source diff, lifted out of the install summary |

The payload is stored on the tool row next to the result
(`internal/agent/agent.go:795-802`, `tj["display"] = display`) and delivered live
on `tool_end` as `Display`. Extension tools add their own presentation from the
`display` object in their module (`web/src/extensionDisplay.tsx`), used by
`chipLabel` (`web/src/components/ChatPane.tsx:1370`) to fill a `{field}` summary
template.

## Events

A turn's progress reaches the browser as `agent.ChatEvent`
(`internal/agent/agent.go:65`) values over the SSE stream. The fields that matter
for tools are `Type`, `Name`, `Detail`, `OK`, `Error`, `Display` and `ToolJSON`.

| Event | Emitted when |
| --- | --- |
| `tool_start` | a call begins; `Name` plus a short `Detail` (the command, path or question — see `toolDetail`, `internal/agent/agent.go:885`) |
| `tool_end` | the call returns; `OK` reflects success, `Detail` is the result text, `Display` is the payload above |
| `injected_message` | text a tool inserted into the transcript (`screenshot_app` caption, a background result, a steered message) |
| `notice`, `info` | compaction, retry, or a user-facing explanation |
| `delta`, `reasoning` | streamed model text / thinking |
| `context` | updated context-window usage |

`ChatEvent.ToolJSON` carries the **bare** marker name rather than the stored
JSON string, because the client compares it against plain strings
(`agent.MarkerName`, `internal/agent/markers.go:26`). That is what lets a row the
app wrote be styled as such while the turn is running: `""` is the reader,
`"steer"` a message steered into a running turn, `"agent"` text injected on the
agent's behalf, `"background"` a finished background command.

## Why the live card and the reloaded card must match

A tool result exists twice: as a `tool_start` / `tool_end` pair in the live
stream, and as a persisted `tool` row read back on reload. Both must render
identically, or the transcript rewrites itself when the page refreshes.

`isNoiseResult` (`web/src/components/ChatPane.tsx:1350`) enforces that on the
client. A result that is a bare success acknowledgement — a JSON object with
`ok: true` and none of `error`, `content`, `output` or `results` — is dropped
from the expanded body, because the call chip already says what happened and
rendering `{"ok":true}` under a nice preview is the "split screen" users see.
Failures and output-bearing results are kept. The same predicate is applied to
the live detail (`web/src/components/ChatPane.tsx:3853`), to reloaded rows
(`web/src/components/ChatPane.tsx:4926`) and to the fold summary
(`web/src/components/ChatPane.tsx:8375`) — three call sites on purpose, since a
rule applied in only one of them produces a body that appears live and vanishes
on reload.

The stored side relies on the marker column for the same reason: author markers
are persisted, never inferred, so a row is styled by who wrote it even after an
app restart.

## Attachments and images

Every turn carries `[]Attachment` (`internal/agent/attachments.go:34`) — text or
images the reader attached, queued or steered along with the message. They are
turned into model parts by `userMessage` (`internal/agent/attachments.go:117`),
with text inlined up to `maxInlineTextAttachment = 51200` bytes
(`internal/agent/attachments.go:15`) and images sent as image parts
(`imagePart`, `internal/agent/attachments.go:106`).

Images are normalised once, when the turn is accepted, by
`NormalizeImageAttachments` (`internal/agent/image.go:34`): the longest edge is
capped at `maxInlineImageDim = 2000`, the encoded copy at
`maxInlineImageBytes = 4.5 MB` of base64, and any source above
`maxDecodePixels = 80_000_000` passes through untouched rather than risking an
out-of-memory decode (`internal/agent/image.go:20-28`). A JPEG source stays JPEG;
everything else is re-encoded as PNG so screenshots and transparency survive. Only
this bounded copy is stored, so the display and the model payload cannot diverge.

A model that stopped accepting images mid-conversation is handled by
`stripImageParts` (`internal/agent/attachments.go:156`), which replays the
history as text only.

## `ask_user`

`askUser` (`internal/agent/tools.go:1103`) posts a structured questionnaire and
waits. The limits are enforced at the tool boundary: `MaxAskQuestions = 4`
(`internal/agent/tools.go:1052`) and `MaxAskOptions = 4`
(`internal/agent/tools.go:1055`), with `DefaultAskTimeout = 30 * time.Minute`
(`internal/agent/tools.go:972`). One option label, `freeTextLabel = "type something"`
(`internal/agent/tools.go:1060`), is reserved: picking it means the reader is
answering in their own words, so the answer is carried out of band
(`isFreeTextLabel`, `internal/agent/tools.go:1062`) instead of being treated as
the literal label. `askResult` (`internal/agent/tools.go:1070`) assembles the
per-question answers plus an optional note about the questionnaire as a whole,
and `Labels` (`internal/agent/tools.go:1020`) normalises both plain-string and
object options.

## Permissions

Tool calls pass through a resolver before they run:

```go
// internal/agent/perms.go
type Resolver interface {
    Request(ctx context.Context, tool, detail string) (bool, error)
}
```

The server implements it against the 3-option permission policy
(allow / deny / ask). An `ask` decision surfaces as an SSE `permission_request`
that the reader answers in the chat, and a denial — policy, user, or timeout —
comes back as an error describing why, so the model can adapt rather than retry
blindly.

## Background commands

`runCommandBackground` (`internal/agent/tools.go:1211`) detaches the command from
the turn: the tool returns a job id immediately and the completion is delivered
later, mid-turn if the turn is still running or at the start of the next one.
`BackgroundJob` (`internal/agent/background.go:60`) holds the command, session
and callback; the finished text is sanitised by `sanitizeBackgroundText`
(`internal/agent/background.go:40`), which strips ANSI escapes and control
characters (OpenAI-compatible providers reject strings that fail their character
pattern) and repairs invalid UTF-8 so the text survives JSON/SSE transport.

The result is persisted as a user row carrying `BackgroundToolJSON = `"background"``
(`internal/agent/background.go:23`). That marker must be valid JSON — the chat
API hands `tool_json` to the browser verbatim as `json.RawMessage`, and a bare
word made the whole message list fail to marshal after the handler had already
written its 200 header, so the client reported a JSON syntax error instead of
showing history. `IsBackgroundRow` (`internal/agent/background.go:31`) accepts
both the current and the legacy bare encoding, so rows written by older builds
keep rendering.
