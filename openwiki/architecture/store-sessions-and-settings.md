---
type: "Reference"
title: "Store: projects, sessions, messages and settings"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-1d3a5eba2e9274efa7e31523
    resource: repo://internal/agent/markers.go
  - id: openwiki-source-1cc2e4fb103d25d6ac43e821
    resource: repo://internal/server/appearance.go
  - id: openwiki-source-e3a61e665fea9fa4cdf242f1
    resource: repo://internal/server/handlers_auth.go
  - id: openwiki-source-2fcc0ac15ad0af123578c81f
    resource: repo://internal/server/handlers_chat.go
  - id: openwiki-source-7d4ac4eaf17b216fdc5fd692
    resource: repo://internal/server/server.go
  - id: openwiki-source-4a81fcd95533ed8ba5a77739
    resource: repo://internal/store/store.go
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Store: projects, sessions, messages and settings

`internal/store` is the only package that talks to SQLite. Everything above it —
the HTTP handlers, the turn loop, the harness bridge — reads and writes through
`*store.Store`, so the invariants that keep a transcript honest and a setting
predictable live here rather than in the callers.

## Opening the database

`Open(dataDir)` creates `<dataDir>/projects` (the workspace root for every
project), opens `<dataDir>/v1.db`, and applies two pragmas before anything else:

- `db.SetMaxOpenConns(1)` — one connection, so SQLite cannot fail a write with a
  lock error under the concurrent readers (chat status polling, the queue list,
  the preview iframe) v1 has. Serialising is the cheap fix; the workload is
  dominated by small reads.
- `PRAGMA journal_mode=WAL` and `PRAGMA busy_timeout=5000`.

Then it calls `migrate(db)` (`internal/store/store.go:36-60`).

## Migration is one idempotent script

There is no schema version table and no numbered migration files.
`migrate(db)` (`internal/store/store.go:62`) is a single function that:

1. Executes one script of `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT
   EXISTS` statements.
2. Calls `migrateAddColumns(db, table, map[column]ALTER)` for every column added
   after its table shipped. The helper reads `PRAGMA table_info` and runs only
   the statements whose column is missing.
3. Runs the two table rebuilds (`rebuildTableForSessions`) that could not be an
   `ALTER`: `compaction_snapshots` and `pending_asks` gained a `session_id` in
   their primary key, so the old table is copied into a `_v2` table with an
   empty session, dropped, and the new one renamed into place.
4. Runs the data repairs — `migrateLegacyUsers`, `markLegacyAgentRows`,
   `scrubStoredMessages`.

The whole thing is safe to run on every start, which is exactly what happens: a
database from any earlier release converges to the current shape at open, and a
second run does nothing.

### Tables

| Table | Holds | Key facts |
| --- | --- | --- |
| `settings` | instance-wide settings | `key` PK, `value`; the fallback layer below `user_settings` |
| `user_settings` | per-account settings | PK `(user_id, key)` |
| `users` | accounts | `username` UNIQUE, `is_admin`, `oidc` |
| `sessions` | login cookies | `token` PK, `expires_at` |
| `projects` | one workspace each | `owner_id`, `path`, `ephemeral`, `archived`, `session_seq` |
| `chat_sessions` | chat threads inside a project | `archived`, `harness_conversation_id` |
| `messages` | the transcript | `session_id`, `tool_json` markers, `model`, `usage`, `attachments` |
| `compaction_snapshots` | one compacted summary per session | PK `(project_id, session_id)` |
| `memories` | saved facts | `importance`, `last_accessed`, `tags`, `embedding` BLOB |
| `plans` | the active plan JSON | PK `project_id` |
| `pending_asks` | an `ask_user` form awaiting answers | PK `(project_id, session_id)` |
| `queued_messages` | the per-session turn queue | `steer`, `held`, `position` |
| `uploads` | staged uploads | pruned by cutoff |
| `vercel_deploys` | deployment records | `deployment_id`, `state` |
| `push_subscriptions` | browser push endpoints | `endpoint` PK (one row per device) |

Two defaults deserve a note because they encode a decision rather than a shape:

- `projects.vercel_enabled` defaults to **1**. That grandfathers projects created
  before the toggle existed — their Vercel button kept working. New projects are
  inserted with an explicit `0`, so Vercel is opt-in from then on.
- `projects.github_tab` defaults to NULL, which means *decide from the repo URL*:
  the GitHub tab is a property of the project being a GitHub repo, not a question
  to ask up front.

## The message-author invariant

`messages.role` alone cannot say who wrote a user-role row, and the transcript
renders user-role rows on the reader's side. So the store makes the author part
of the call (`internal/store/store.go:1185-1236`):

- `AddMessage(projectID, sessionID, role, content, toolJSON, model, reasoning,
  usage, attachments)` refuses `role == "user"` with
  `ErrUserRoleNeedsAuthor` (`internal/store/store.go:1192`). It is for assistant
  replies, tool results, error rows and plan rows only.
- `AddUserMessage(projectID, sessionID, content, model, attachments)` is the only
  way to store something the reader typed, and it deliberately cannot carry a
  marker — it writes an empty `tool_json`.
- `AddInjectedMessage(projectID, sessionID, content, marker, model, attachments)`
  stores a user-role row the app wrote on the reader's behalf — a `screenshot_app`
  caption, a steered message, a finished background command. A blank marker is
  refused with `ErrInjectedMessageNeedsMarker` (`internal/store/store.go:1196`);
  the marker is persisted in `tool_json`.

Before this split a call site could — and one did — store a tool caption as a
plain user message, and the transcript then showed it as the reader's own. The
markers themselves live in `internal/agent/markers.go`: `SteerToolJSON = "steer"`
(a message the user wrote that joined a turn already in flight) and
`AgentToolJSON = "agent"` (text a tool injected). `internal/agent/background.go:25`
adds `BackgroundToolJSON = "background"`. Every marker is a raw JSON string
because the chat API forwards `tool_json` to the browser as `json.RawMessage`;
`MarkerName` decodes it to the bare name for live events, and legacy bare markers
pass through unchanged so both encodings reach the client as the same name.

`RoleNotice = "notice"` (`internal/store/store.go:1204`) is a third kind of
app-written row: it explains something the transcript did to itself (a
compaction) and is **never sent to the model**. It is the reader's context, not
the conversation's.

The chat API normalises `tool_json` on read (`internal/server/handlers_chat.go`):
a valid JSON marker passes through, a legacy bare `background` marker is upgraded
to the quoted constant so old rows keep their styling, and any other non-JSON
value is dropped with a log line rather than making the whole transcript
unreadable.

### The legacy-row repairs

Both run at startup and are idempotent:

- `markLegacyAgentRows` (`internal/store/store.go:348`) updates the one row shape
  the harness path used to write wrong: a user row whose content is exactly
  `Screenshot of the current app preview (captured by the screenshot_app tool).`
  It stamps `tool_json = '"agent"'` and clears the caption. The literal is frozen
  on purpose — a migration must describe the data it was written against, not
  follow a constant that may change later. The image is kept; only the sentence
  goes.
- `scrubStoredMessages` (`internal/store/store.go:364`) walks every message and
  runs `sanitize.Scrub` on `content` and `attachments`, updating only the rows
  that changed. This is what makes chats stored before the sanitizer recoverable:
  without it, a retry would rebuild the request from the same poisoned rows.

`migrateLegacyUsers` (`internal/store/store.go:421`) is the third: it drops
pre-multi-user login sessions, and when no `users` row exists but the old
`password_hash` setting does, it creates the `admin` account from it and deletes
the setting. Ownerless projects (legacy rows, or projects created while auth was
disabled) are claimed by `ClaimOwnerlessProjects(ownerID)`, which the auth
bootstrap calls after creating the first admin.

## Projects

`Project` (`internal/store/store.go:946-974`) carries both the identity
(`ID`, `Name`, `Path`) and every per-project switch: `OwnerID`, `AutoPush`,
`PreviewDisabled`, `VercelEnabled`, `GitHubTab`, `VercelProject`, `Ephemeral`,
`Archived`.

- `CreateProject` stamps `created_at`/`updated_at` and writes empty strings as
  NULL, so "unset" is one representation and not two.
- `ListProjects`, `ListProjectsByOwner`, `GetProject`, `RenameProject`,
  `TouchProject` are the read/write surface; `DeleteProject` deletes the project
  row **last**, after its messages, chat sessions, memories, compaction snapshots
  and pending asks, so a failure part-way leaves the project discoverable rather
  than orphaned data with no owner.
- `Ephemeral` marks a scratch chat. The dashboard groups these separately and the
  sweeper archives them; `Archived` hides a project without deleting anything.
  `ArchiveExpiredEphemeralProjects(ownerID, days)`
  (`internal/store/store.go:1085`) does the sweep in one `UPDATE`, using the
  newest `user`/`assistant` message or `updated_at`, whichever is later, as "last
  activity". An empty `ownerID` sweeps every project (the auth-disabled case).

## Chat sessions

`chat_sessions` splits a project's history into threads. `ChatSession`
(`internal/store/store.go:1651-1663`) exposes `ID`, `Name`, `CreatedAt`,
`Archived` and `LastTurnAt`; `ListSessions` derives `LastTurnAt` as the newest
assistant message time, because for a session used for weeks, its creation date
says nothing about when it was last worked in, and the dashboard shows the latter.

- `CreateChatSession(projectID, name)` auto-numbers an empty name from a
  per-project counter: `UPDATE projects SET session_seq = session_seq + 1 …
  RETURNING session_seq` → `"Session N"`. The counter never decreases, so
  archiving or deleting a session does not make the next one reuse an old name.
  (The migration seeds `session_seq` from the existing session count so projects
  that predate the counter do not start below it.)
- `EnsureDefaultSession(projectID)` returns the oldest session, creating
  `"Session 1"` when there is none — and claims any pre-session messages for it
  with `UPDATE messages SET session_id = ? WHERE session_id IS NULL`. That is the
  bridge between a single-thread database and the current shape. `chatSessionID`
  in the chat handlers calls it whenever the request names no session.
- `ForkChatSession` copies a session's transcript and, when present, its
  compaction snapshot into a new session, leaving the original untouched. The
  snapshot's `covered_message_id` is remapped to the fork's own ids.
- `ArchiveChatSession` / `UnarchiveChatSession` flip the `archived` flag;
  `DeleteChatSession` deletes its messages first, then the session.
- `SetHarnessConversationID` records which pi-durable conversation backs a
  session. It has to be **durable**: the sidecar's own `v1SessionId` map lives in
  memory, so a restarted sidecar would otherwise mint a fresh conversation for an
  existing session and the model would silently lose the history.

## Messages

`Message` keeps `ToolJSON`, `Model`, `Reasoning`, `Usage` and `Attachments` as
text columns — the store never parses them. The API layer decodes `usage` and
`attachments` and forwards `tool_json` as raw JSON.

Two read paths exist for one reason:

- `ListMessages(projectID, sessionID)` returns the whole transcript oldest-first.
- `ListMessagesPage(projectID, sessionID, beforeID, limit)`
  (`internal/store/store.go:2163`) returns one page, `limit <= 0` meaning "no
  limit". It selects `ORDER BY id DESC LIMIT limit+1` — the extra row answers
  `hasMore` without a second query — drops the extra, and reverses the page to
  transcript order. A client walks backwards with `before=<id>`. The chat UI
  loads the newest page first because the reply the reader came back for is in
  the last few rows, and waiting for a long history to serialise is what made a
  long chat feel slow to open.

Two cheap aggregates support the UI without loading anything:

- `SessionLastMessageID` (`internal/store/store.go:2214`) — the chat status poll
  compares it with what the client holds to decide whether the transcript
  actually needs re-fetching. A run that ended while the app was away changes it,
  and nothing else does.
- `SessionMessageCount` — answers the one question empty-session cleanup asks.

## Settings: two layers, one resolution rule

`settings` is the instance layer; `user_settings` is the account layer.
`GetSetting`/`SetSetting`/`DeleteSetting` and
`GetUserSetting`/`SetUserSetting`/`DeleteUserSetting` are plain upserts on each.
`SeedUserSetting(key, value)` writes a value for every user that has none yet, so
a new global default reaches existing accounts without clobbering choices they
made themselves.

Resolution happens in `(*Server).userSetting` (`internal/server/server.go:539`):
the user's own value wins when it is present **and non-empty**, otherwise the
shared/global value applies. Two consequences matter:

- A `PUT /api/settings` with an empty string *deletes* the user's row, which is
  how "inherit the global value again" is expressed. It is not a way to store an
  empty setting.
- The global layer is what makes a pre-multi-user install (and an admin-set
  deployment value) keep working unchanged.

Instance-level settings are written with `setGlobal` rather than `set`. That is
deliberate for anything that describes the install rather than the account — the
GitHub and Vercel OAuth app credentials (one OAuth app per install) and the OIDC
configuration.

## Appearance: one settings object for every UI knob

`appearance` is a single per-user JSON object, not a key per knob
(`internal/server/appearance.go:13`). The knobs — theme, chat side, the project
tab bar, the composer button row, JSON pretty-printing, tool-call folding, turn
folding, thinking collapsed — are read together, saved a knob at a time, and only
the SPA interprets them. Keeping them in one row means a read is one query and a
new client can never see a half-applied set.

- `appearanceSettings` (`internal/server/appearance.go:26`) is the *resolved*
  state: every field is filled, because the API never hands the SPA a value it
  would have to default itself.
- `defaultAppearance()` matches the SPA's own defaults, so a fresh account
  renders the same before and after the settings fetch lands.
- `appearancePatch` (`internal/server/appearance.go:45`) is what a PUT may
  change: every field is a pointer, and a nil field is left alone. One control
  saving therefore cannot push the other knobs back from a stale client copy.
- `(*Server).appearance(userID)` decodes over the defaults — so a newer release
  that adds a knob reads an older row as *that knob's default*, not as a zero
  value — and an unreadable row falls back to the defaults with a log line
  instead of failing the settings request.
- `normalized()` clamps the values the SPA can render (an unknown chat side
  becomes `left`) and never leaves a nil slice to become `null` in JSON.
- `applyAppearancePatch` merges the patch and writes the whole resolved object
  back.

`chatTabsSetting{Order, Hidden}` (`internal/server/appearance.go:19`) is shared by
two rows: the project view's tab bar (`chatTabs`) and the chat composer's button
row (`composerButtons`). An **empty order means "the built-in order"**, which is
what keeps an entry added in a later release from being invisible to an existing
account. The SPA resolves that case to its own `DEFAULT_*` list, and enforces the
four-button cap when it reads the layout, not when it saves.

The object travels on the existing settings endpoint: `GET /api/settings`
returns `appearance: s.appearance(userID)`, and a `PUT /api/settings` body with
an `appearance` field is applied through `applyAppearancePatch`.

## Memories, plans, asks, queue, uploads

- **Memories.** `AddMemory(projectID, content, category, importance)` defaults an
  empty category to `fact` and a non-positive importance to 1. `Memory`
  importance decays: `EffectiveImportance` (`internal/store/store.go:1705`)
  returns the stored value unchanged for pinned memories (importance >= 2) and
  `importance - 0.1*days` otherwise. `ListMemories`
  (`internal/store/store.go:1725`) applies that decay and lazily deletes a row
  whose effective importance falls below 0.2 after 30 days unused — so decay
  needs no background job. `TouchMemory` bumps `last_accessed` and `access_count`
  on each retrieval. Vectors are packed little-endian into the `embedding` BLOB;
  a row written before an embedding provider existed has a NULL blob, which the
  ranker reads as "not comparable" rather than as a zero vector.
- **Plans.** `plans` is keyed by `project_id` alone — a plan is the project's,
  not the session's.
- **Pending asks.** `pending_asks` is keyed by `(project_id, session_id)` and
  upserts on conflict, so a second `ask_user` in the same session replaces the
  first rather than accumulating. `ClearPendingAsk` removes it when answered,
  timed out, or superseded by a new turn.
- **Queue.** `queued_messages` persists the turn queue (see
  [chat turns and queue](chat-turns-and-queue.md)); `position` orders it and
  `attachments`/`steer`/`held` are text-and-flag columns so the queue survives a
  restart.
- **Uploads.** `uploads` stages file content by id; `PruneUploads(cutoff)`
  deletes what no longer has a referencing message.

## Why the invariants live here

Each of these rules is enforced in the store rather than in a caller because a
caller can be added later and forget it:

- A user-role row cannot be stored without saying who wrote it — the transcript's
  honesty is a schema-level property, not a convention.
- Settings resolve user-then-global in one place, so no handler has to remember
  the precedence.
- Appearance is one object with pointer patches, so concurrent controls cannot
  clobber each other.
- Migrations run at open, so no code path can run against a stale schema.

## Related

- [Chat turns and queue](chat-turns-and-queue.md) — who writes the rows and when.
- [HTTP API and auth](../architecture/http-api-and-auth.md) — the endpoints that
  reach these tables, and the ownership check that scopes them.
- [Memory, search and sanitize](memory-search-and-sanitize.md) — how stored
  memories are ranked and how stored text is cleaned.
