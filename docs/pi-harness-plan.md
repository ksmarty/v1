# v1 → pi-durable: replace the backend harness

Scope: replace v1's Go chat harness (`internal/agent/agent.go` + tool loop + compaction
+ queue/steer) with a bundled pi-durable sidecar process, while keeping the Go HTTP API,
auth, store, preview/terminal/gitops and the React SPA.

## 1. Decisions locked (from Q&A)

| # | Decision |
|---|---|
| D1 | pi moves in as a **sidecar** process; Go stays the server/API/UI owner. |
| D2 | Sidecar runs on **pi-durable** (`@earendil-works/pi-durable` v1.0.4), not bare pi-agent-core. |
| D3 | **pi-durable owns the transcript.** One pi conversation per v1 chat session; the session→conversation id mapping lives in v1's store. v1's `messages` table becomes a derived read model (or is deleted). |
| D4 | v1-specific tools are exposed via a **host-tool API**: Go exposes an internal tool endpoint; the sidecar registers pi-durable `defineTool` wrappers that call back into Go. |
| D5 | **v1's SSE `ChatEvent` contract is preserved**; Go translates pi-durable's `watchEvents`/`viewState` into it. Frontend untouched. |
| D6 | **pi owns inference.** A per-session pi custom provider is registered from v1's stored `llm_base_url`/`llm_api_key`/`model`; `internal/llm/llm.go` is deleted; `internal/llm/providers.go` (models.dev catalog + thinking discovery + picker) survives for the UI only. |
| D7 | Rolling out as a **hard cutover behind a temporary env flag** (`V1_HARNESS=pi|go`, default pi); old Go loop deleted when the flag goes. |
| D8 | Sidecar runs on **Node >=22.19** and uses pi-durable's shipped `openNodeSqliteStorage()`. **No Bun anywhere.** Image stays Go + `node:22-slim` (two runtimes, one already present). |
| D9 | **All agent tools stay Go-implemented**, registered in the sidecar via `defineTool` → bridge → Go (see F3). Recommendation standing unless overridden. |
| D10 | **Skills are lazy-loaded, matching pi.** The sidecar's skills `section()` carries name/description/location only; `SKILL.md` bodies are fetched on demand through the `read_file` host tool. Deliberate behaviour change from v1's full-body injection (accepted, not a bug to fix later). |

## 2. Findings that revise the decisions

**F1 — pi-durable has no skills or MCP layer.** `grep -rin 'skill|mcp' dist/*.d.ts` on the
pi-durable 1.0.4 tarball returns nothing. Skills/MCP live in *pi-coding-agent* (the product
package). pi-durable only offers extensions, `section()` prompt sections, tools, `hook()`s,
`wrapTool`/`wrapSection`, custom tasks and `memo` (no skills/MCP section in its README either).
→ Realisation of D7's intent: **skills** become a prompt `section()` in the sidecar
(`loadSkills()` + `formatSkillsForPrompt()` are pure functions exported by pi-coding-agent and
can be reused, or Go keeps composing the string and passes it as a section over the bridge);
**MCP** stays in Go and its tools are registered as host tools through the bridge, exactly like
D4 (v1 already surfaces MCP tools as dynamically-added tools with a namespace prefix, and
pi-durable has `control: { addTools?: string[] }` for exactly this dynamic case).

**D10 (decided): skills are lazy-loaded.** v1 injects each enabled `SKILL.md`'s **full body**
into the system prompt; the sidecar instead emits only name/description/location and lets the
model read a body on demand via `read_file`.
Concretely: Go keeps discovering skills (`internal/skills/skills.go`) and passes
`skills: [{name, description, location}]` in `conversation.ensure`/`configure` (already a field in
§4); the sidecar formats them with pi's `formatSkillsForPrompt` shape. Two consequences to handle,
not to "fix":
- **Skill bodies now arrive as tool results**, so `read_file`'s path guard must permit the skills
dirs (`<project>/.v1/skills`, `.pi/skills`, `.opencode/skills`, and the global config skills dir) —
today the guard is workspace-relative and would reject the global dir.
- **Skill text is no longer provenanced in the prompt**, so a skill a user enabled may never be
read in a given run; the UI's "enabled skills" list stays the source of truth for expectations.
Token cost drops substantially for projects with many/large skills (the reason for the choice).

**F2 — RESOLVED (D8): Node >=22.19.** `dist/storage/sqlite/node.js` hard-imports `DatabaseSync`
from `node:sqlite`, which is the surface Bun cannot be relied on for; the package is ESM,
`"engines": {"node": ">=22.19.0"}`, no `bun:` imports. So the sidecar runs on Node and uses
`openNodeSqliteStorage(path)` as shipped — the image is already `node:22-slim`. (Rejected: a
`bun:sqlite` facade over `SqliteStorage.open(db)`; also rejected `storage/jsonl/node`, which is
Bun-clean but turns the transcript into append-only JSONL and weakens the mirror/rebuild story.)
The portable `storage/sqlite` core has *no* Node imports, so the facade route stays available if
this ever needs revisiting. One process owns a storage at a time — **no cross-process locking**.

**F3 — pi-durable ships no image/vision tool.** "Reading images is not supported yet."
v1's `screenshot_app` is vision-gated and injects a PNG as a user message with an image
attachment (`injected_message`), because tool results are text-only on many OpenAI-compatible
APIs. That stays Go-side; pi-durable can carry the image as a `pi.user` entry via
`submit({ type: "write", entry })`.

**F4 — ids change type.** pi-durable entry ids are opaque (`EntryId`, string); v1's
`messages.id` is `int64` and the frontend/REST depend on it (`GET /api/projects/{id}/messages`,
`/messages/{msgId}/attachments/{idx}`, `/messages/truncate`, `ChatEvent.MessageID int64`).
Keeping a **derived read model** in Go (mirror rows written from committed `entry_appended` /
`message_end` events, with pi-durable entry id stored alongside) preserves every existing
endpoint and the whole frontend. The read model is rebuildable from pi-durable at any time, so
pi-durable stays authoritative (D3 holds).

**F5 — `HarnessOptions.models` is harness-wide, `ModelRef` is per-conversation.**
`HarnessOptions.models: Models` (pi-ai), `AgentState.model?: ModelRef = {provider, modelId}`.
Since every v1 user has their own `llm_base_url`/`llm_api_key`, register providers namespaced by
user (`<userId>:<providerId>`) in one `MutableModels`/`ModelRuntime` and select per conversation
via `Conversation.configure({ model })`. Runtime registration needs no config file:
`pi-ai`: `createModels()` → `setProvider(createProvider({id, baseUrl, auth, models, api}))`;
`ModelRuntime.registerProvider(id, cfg)` / `setRuntimeApiKey(providerId, key)` in pi-coding-agent.
Thinking levels are exactly `off|minimal|low|medium|high|xhigh|max`, mapped from v1's
`reasoning`/thinking options; `thinkingLevelMap` maps a level to a provider-specific string.

## 3. Target architecture

```
            ┌──────────────────────────── Go (unchanged boundary) ───────────────────────────┐
Browser ──▶ │ HTTP API + auth + SSE hub (internal/server)                                    │
            │ turnManager single-flight per (project, session) + turnQueue (hold/reorder/    │
            │   edit/steer) — kept: it is the UI-facing queue in AGENTS.md                    │
            │ store (sqlite): users/projects/sessions/settings/memories/todos/pending_asks    │
            │   + mapping {session_id → conversation_id}  + derived messages read model       │
            │ preview · terminal · gitops · screenshot · mcp client · skills dir              │
            └───────────────┬──────────────────────────────────────────────┬─────────────────┘
                            │ JSON-RPC over Unix socket (newline-delimited) │
                            │  requests →, events ←                         │ host-tool callbacks
            ┌───────────────▼──────────────────────────────────────────────▼─────────────────┐
            │ pi-durable sidecar (long-lived, one process, all users)                        │
            │  Harness.open(openNodeSqliteStorage(/data/harness.sqlite), {models, registry,  │
            │    settings, env?}, context)                                                   │
            │  registry: host tools (defineTool → bridge) + MCP tools + prompt sections      │
            │    (base system prompt, memories, plan, skills, cwd/tool guidance)             │
            │  hook(ToolTask, { beforeTool }) → bridge authorize() → v1 permission_mode      │
            │  one Conversation per v1 chat session; watchEvents per subscription            │
            └───────────────────────────────────────────────────────────────────────────────┘
```

Storage: `/data/harness.sqlite`, WAL, `synchronous=NORMAL` (commits survive process crashes).
`/data/v1.db` (store) stays as-is.

## 4. Bridge protocol (JSON-RPC 2.0, newline-delimited, Unix socket)

Go → sidecar:
- `harness.ready` → `{schemaVersion}` (startup handshake; fail fast if the socket never appears)
- `conversation.ensure` `{v1SessionId, cwd, systemPrompt, memories, plan, skills, provider:{baseUrl,apiKey}, model, thinkingLevel, tools:[names], vision, toon, agentKind}` → `{conversationId}`
- `conversation.configure` `{conversationId, …same fields}` (settings/model/tool changes between turns)
- `turn.submit` `{conversationId, requestId, text, attachments, whenBusy:"reject"}` → `{submissionId}`
  (Go's `turnQueue`/`turnManager` already guarantee single-flight, so the harness never sees a
  busy reject in the normal path)
- `turn.steer` `{conversationId, requestId, text}` → `{submissionId}` (`whenBusy:"steer"`)
- `turn.followUp` `{conversationId, requestId, text}` → `{submissionId}` (`whenBusy:"followUp"`)
- `turn.abort` `{conversationId}` · `turn.compact` `{conversationId, instructions}` · `turn.reset`
- `conversation.entries` `{conversationId, cursor, limit}` (rebuild/mirror repair)
- `conversation.usage` `{conversationId}` (context-usage endpoint)
- `watch.start`/`watch.stop` `{conversationId, subscriptionId}`
- `host.result` (reply) / `host.event` (unsolicited tool-side notifications)

Sidecar → Go:
- `event` `{subscriptionId, events:[AgentEvent…]}` batched one batch per commit
  (`snapshot`, `run_start/end`, `turn_start/end`, `message_start`, `message_update{usage,changes}`,
  `message_end{entry}`, `tool_execution_start/update/end`, `inbox_update`, `submission`,
  `auto_retry_start/end`, `deferred_poll`, `entry_appended`, `agent_changed`, `usage_changed`,
  `task_failed`, `compaction_start/end`)
- `host.call` `{callId, tool, args, conversationId}` — a host tool needs Go (see §6)

Every request carries a `requestId`; pi-durable is idempotent per `requestId` (`submission()`
reacquires after a restart), so Go can safely retry a submit after the sidecar dies.

## 5. SSE translation table (D5)

| v1 `ChatEvent` | pi-durable source |
|---|---|
| `delta` | `message_update.changes[].text_delta` (accumulated per `contentIndex`) |
| `reasoning` | `message_update.changes[].thinking_delta` |
| `tool_start` | `tool_execution_start {toolCallId, toolName, args}` → Go renders the same `detail` string it renders today |
| `tool_end` | `tool_execution_end {entry}` (+ `isError`/diagnostics) |
| `info` | Go-generated (run/compaction notices) + `compaction_start/end`, `auto_retry_start/end`, `deferred_poll` |
| `injected_message` | steer placement (`inbox_update`/`run_start.inputs` resolution) and background-job results; `tool:"background"` for the latter |
| `todos` / `memories` | Go emits after the `set_todos` / `remember` / `forget` host tool runs (Go owns those stores) |
| `permission_request` | `beforeTool` hook → bridge → Go `permRegistry` (same as today) |
| `question_request` | `ask_user` host tool → Go `askRegistry` + `pending_asks` (same as today) |
| `project_renamed` / `session_renamed` / `background_started` | Go emits when the corresponding host tool runs |
| `done` | `run_end` + usage from `usage_changed`/`pi.usage`; map to `Usage{input,output,model,cost,context}` (`context` = final round prompt size, as today) |
| `error` | `task_failed`, a settled submission with `status:"unanswered"`, or exhausted `auto_retry` |
| `file` / `plain` | Go-generated as today |

## 6. Tool ownership (D4 + F3)

Recommendation: **all agent tools stay Go-implemented** and are registered in the sidecar's
registry as `defineTool` wrappers that proxy to Go over the bridge. Rationale: AGENTS.md forbids
weakening the path-escape/SSRF guards in the agent tools, and v1's tool output shape is
rendered by the UI (`tool_start`/`tool_end` detail, TOON re-encoding for the model, tool-result
JSON in the store). pi-durable's native `CodingTools` (`read/write/edit/bash`) execute through
`NodeExecutionEnv` with no such guards, so adopting them would either regress security or require
a guarding `ExecutionEnv` wrapper — extra work for no user-visible gain.

Host tools (unchanged implementations, new transport): `read_file`, `write_file`, `edit_file`,
`delete_file`, `move_file`, `list_files`, `search_files`, `run_command`,
`run_command_background`, `install`, `typecheck`, `preview`, `restart_preview`,
`verify_project`, `screenshot_app`, `run_container`, `git`, `fetch_url`, `make_plan`,
`update_plan`, `set_todos`, `remember`, `forget`, `ask_user`, `set_project_name`,
`set_session_name`, plus dynamically-added MCP tools (namespaced).

Tool-result TOON encoding moves from `RunChat`'s history builder into the host-tool reply
(a `wrapTool`/`afterTool` in the sidecar, or Go encoding before returning) — semantics unchanged.

## 7. Go-side work list

Delete: `internal/agent/agent.go`'s `RunChat` loop, `internal/agent/compaction.go`,
`internal/agent/planning.go`'s LLM caller, `internal/llm/llm.go`.
Keep + rewire: `internal/agent/tools.go` (`Executor`) to serve the bridge instead of the loop;
`internal/agent/toon.go`; `internal/agent/background.go`; `internal/mcp/mcp.go`;
`internal/skills/skills.go` (as a section source); `internal/agent/perms.go` behind the hook.
New: `internal/harness/` — sidecar process supervisor (`os/exec`, restart with backoff, socket
readiness probe), the JSON-RPC client/server, the event translator, the tool proxy, the
session→conversation mapping, the derived message read model.
New: `internal/server/handlers_*` wiring for `GET /chat/status`, `/chat/watch`, the queue
endpoints and `/messages` to the new path; `turnManager`/`turnQueue` stay.
Docs: README + AGENTS.md sections (`internal/agent/` description, the run/queue semantics
paragraph, the tool list, the new `internal/harness/` entry, the Docker/dev instructions).

## 8. Sidecar contents

`sidecar/` (new, TypeScript, ESM, no bundler needed — `tsc` to `dist/`; Node >=22.19):
`host.ts` (bridge server, JSON-RPC framing, watch subscriptions), `harness.ts`
(`Harness.open` + settings), `providers.ts` (per-user provider registration + thinking levels),
`tools.ts` (`defineTool` wrappers for host tools + MCP), `hooks.ts` (`beforeTool` → authorize),
`sections.ts` (base prompt, memories, plan, skills, cwd/tool guidance), `entries.ts`
(v1 message ↔ pi entry mapping for the mirror), `env.ts` (only if any native tool survives).
Build: new Dockerfile stage (`node:22-slim`, assert `node >= 22.19` at build time) installs
`pi-durable` + `pi-ai`, compiles TS, copies `dist/` + `node_modules/` into the final stage
(`/app/sidecar`).
Dev: `make dev-backend` also starts `node sidecar/dist/host.js`, socket in the data dir.
Config: `V1_HARNESS=pi|go`, `V1_SIDECAR_CMD`, `V1_SIDECAR_SOCKET`.

## 9. Phases

0. **Spike — DONE, all mechanisms verified** (see §9.1). F2/F5 confirmed before any product
   code moves.
1. Bridge + supervisor + handshake; `V1_HARNESS=go` remains the default.
2. Host tools + provider registration + prompt sections; parity harness comparing
   `V1_HARNESS=go` vs `pi` output on a fixed script of turns. Skills land here as the D10
   lazy-load section (skills-dir path guard included).
3. Event translator + mirror read model; SSE parity for every `ChatEvent` in §5.
4. Queue/steer/hold/abort/compact/context endpoints on the new path; crash-recovery tests
   (kill -9 the sidecar mid-turn, resume, assert no duplicate submission).
5. Migration: back-fill existing sessions as conversation entries; flip the default; delete the
   Go loop; update README/AGENTS.md/CHANGELOG.

### 9.1 Phase-0 spike results (verified on Node v26.10.0, pi-durable/pi-ai/chord 1.0.4, `/tmp/spike/`)

Ran a real end-to-end turn against a live OpenAI-compatible endpoint
(`opencode-go` / `deepseek-v4-flash`) with a **Go-shaped Unix-socket JSON-RPC host**
(`spike.mjs`), then reopened the store in a second process (`spike.mjs --verify`).

Confirmed working, in one run:

| Mechanism | Result |
| --- | --- |
| `Harness.open(openNodeSqliteStorage(path), {models, registry}, ctx)` | ✅ store created, `root()` created conversation id=1 |
| Per-process provider registration w/ static api key | ✅ usage keyed `opencode-go/deepseek-v4-flash`, cost + reasoning tokens + `cacheRead` populated |
| Host tool reached over a **real Unix socket**, NDJSON JSON-RPC | ✅ `host rpc calls=1`, `read_file` returned 43 bytes |
| `beforeTool` approval round-trip to host | ✅ `authorizations=2 denials=1`; allowed call ran, denied call blocked |
| Blocked call semantics | ✅ still committed as a `pi.tool-result` entry whose text is `<harness>\n[error] Tool call blocked: <block string>`; the run **continues** to a final answer; **no** `tool_execution_start` is emitted for a blocked call (only `tool_execution_end`) |
| Event stream for SSE translation | ✅ `message_start`, `message_update` (`thinking_delta`/`text_delta`/`toolcall_start`), `message_end`, `tool_execution_start`/`_end`, `turn_start`/`_end`, `run_start`/`_end`, `submission`, `usage_changed`, `snapshot` |
| Persistence + restart | ✅ 7 entries survive; `root()` reacquires the reserved root conversation; agent snapshot (model, thinkingLevel, **extensions by name**, tools, sections, instructions, cwd) restored |
| Crash recovery / idempotency | ✅ `harness.submission(id)` reacquired with durable status `done`; resubmitting the **same `requestId` returned the same submission id — no double run** |

Two provider-registration gotchas (both cost real debugging time; encode them in the sidecar):

1. `createProvider` does **not** stamp the provider id onto its models. Each model object needs an
   explicit `provider: "<id>"` or every lookup fails with `Unknown provider: undefined`.
2. Each model object also needs an explicit **`baseUrl`**: `getCompat()` calls `detectCompat(model)`
   unconditionally (even when `model.compat` is set) and `detectCompat` dereferences
   `model.baseUrl` → `TypeError: Cannot read properties of undefined (reading 'includes')`
   surfaced only as `stopReason: "error"` on an empty assistant entry.

One further finding, important for **both** the provider story and the event translator:

3. Provider-level `headers` are **not** merged on the harness call path, and upstream endpoints may
   require per-session routing headers (opencode-go returns
   `400 {"type":"MissingSessionID","message":"Request is missing x-opencode-session ..."}`).
   The working recipe is to wrap the registered streams and inject the header from the
   `options.sessionId` the harness does supply (stable per submission, e.g.
   `01a11293-4867-778f-85f9-f19c6a1adc0b`). v1 will need this wrapper per provider config.
4. A provider failure does **not** always arrive as an `error` event: the run settled `unanswered`,
   usage stayed zero, and the only evidence was `stopReason: "error"` +
   `errorMessage: "<status>: <body>"` on the committed assistant entry. The translator must read
   **entry error fields**, not just the event stream, or provider errors become silent failures.

Note: `Harness.open` requires `registry` even for a read-only reopen, because resumed agent
snapshots resolve extensions **by name** — the sidecar must install the identical
registry (extension, tool, and section keys) before `resume()`, or rehydrated conversations break.

## 10. Risks / open items

- pi-durable is labelled Experimental (API changes between releases) — pin the exact version and
  keep the bridge as the seam so upgrades are contained.
- `node:sqlite` availability depends on the exact Node build (needs ≥22.19) — assert it at build
  time and in the sidecar's startup handshake; fail loudly rather than at first commit.
- No cross-process locking for the harness store → one sidecar process only (already the design);
  a second instance must be refused loudly.
- `HarnessOptions.env` is called per tool call and pi-durable ships no skills/MCP (F1) — skills
  and MCP parity is on us.
- Usage shape differs (`pi.usage` is `{models, tools}` keyed by `"provider/modelId"`); `Context`
  (final-round prompt size) must be derived, not read.
- Vision/screenshot path (F3) and attachments (`ChatAttachmentMeta` ↔ `UserMessage` content
  parts) need explicit mapping.
- Existing `messages` rows carry `usage`/`reasoning`/`model` per assistant message; the mirror
  must keep those columns populated from `message_end`/`usage_changed`.
