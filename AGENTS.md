# AGENTS.md

Guidance for AI agents working in this repository.

## Project overview

v1 is a self-hosted clone of v0 (v0.dev): chat with an AI and it builds real
web apps. It ships as one Docker container. Backend is Go, frontend is a
React SPA (Vite + TypeScript), both built into a single binary.

## Layout

- `cmd/v1/` — entrypoint; wires config → store → server.
- `internal/config/` — reads all env config once at startup (`Config` + `Load`).
- `internal/server/` — HTTP API routes, handlers, embedded `dist/` (built SPA).
- `internal/auth/` — multi-user auth: per-user accounts (bcrypt), sessions bound to a user, admin role, auth middleware (attaches the `*store.User` to the request context). The auth middleware protects `/api/*` and `/preview/*` (401 JSON) but serves the SPA shell + static assets publicly so unauthenticated browsers reach `/login` (password or OIDC); it still attaches the user to the context when a session exists. OIDC users carry an `oidc` flag on the `users` row (auto-set on OIDC sign-in) and can be granted admin via `V1_OIDC_ADMIN_EMAILS`; the Settings Auth page hides the password form for OIDC users (admins keep the OIDC config section). Settings are per-user (`user_settings` table) with a shared/global fallback — the server's `userSetting` helper layers them; instance-level keys (MCP, skills, providers cache, OAuth app credentials) stay global. Projects are strictly owner-scoped (`projects.owner_id`, 404 for non-owners); auth-disabled dev mode skips the ownership gate.
- `internal/llm/` — OpenAI-compatible client + models.dev provider catalog.
- `internal/agent/` — the chat agent loop (SSE) and its file/preview tools.
- `internal/store/` — SQLite (settings, sessions, projects, messages).
- `internal/preview/`, `internal/terminal/`, `internal/gitops/` — previews, terminals, GitHub.
- `internal/scaffold/` — project templates.
- `web/` — React frontend; built into `internal/server/dist` for embedding.

## Key commands

- `make dev` — backend on :8080 (auth disabled) + Vite dev server.
- `make dev-backend` / `make dev-frontend` — split terminals.
- `make build` — `npm run build` (runs `tsc --noEmit`) then copies `web/dist`
  into `internal/server/dist` and `go build ./cmd/v1`.
- `go build ./...`, `go test ./...` — backend compile + tests.

Always run `go build ./...` and `cd web && npm run build` (typecheck) after
backend and frontend changes respectively. After each round of changes, rebuild
and restart the local server with `V1_AUTH_DISABLED=true` (via `make dev-backend`
or `make dev`), then report: (1) the new stamped build version (`v1 <version>
(<commit>) listening on :8080`), and (2) confirmation that auth is disabled.

## Conventions

- Go: stdlib only for new dependencies unless unavoidable; `net/http` (Go 1.22+
  method-pattern routes), no framework. Errors returned upward, handled at the
  boundary. `writeJSON`/`writeError`/`decodeJSON` helpers live in
  `internal/server/server.go`. The one browser-automation dep is
  `github.com/chromedp/chromedp` (`internal/screenshot`) — it drives a shared
  headless Chrome for the `screenshot_app` agent tool; Chrome is auto-located,
  `V1_CHROME_PATH` overrides. The tool is only registered when the turn's
  model carries `imageInput` catalog metadata
  (`Server.modelSupportsImages`); the PNG reaches the model as an injected
  user message with an image attachment (tool results are text-only on many
  OpenAI-compatible APIs), streamed to the UI as `injected_message`.
  Node-mode previews are screenshotted via `preview.Manager.DirectURL`
  (the proxy requires a session when auth is on).
- Chat is organized into per-project sessions (`chat_sessions` table; messages
  carry a `session_id`, backfilled to the project's auto-created "Session 1").
  The chat/retry/queue/ask/compact/context endpoints all take a `sessionId`
  (falling back to the default session); runs are single-flight per
  (project, session) — `turnManager` in `internal/server/turns.go` keys on
  both, so sessions run independently.
- Chat runs are single-flight per session (`turnManager` in
  `internal/server/turns.go`): a second `POST /chat` while a run is active is
  queued onto the run's `turnQueue` atomically (`beginOrQueue`, 202 reply
  with the entry's id). Runs have no timeout (long generations run to
  completion; the stop endpoint cancels). `GET /chat/status` reports whether
  a run is active — the client polls it to attach to the run's live event
  hub (`GET /chat/watch`): a client returning to a running chat replays the
  events from now on through the normal stream handler, so the UI behaves
  exactly as if it had never left (streaming composer, live thinking and
  tool rows), and refreshes the transcript when the run finishes. A mid-response failure (token limit, network drop)
  persists the partial reply; retrying then continues from it and, on
  success, folds the partial + continuation into one message and drops the
  error (`ContinueFromID` in ChatParams, `MergeContinuedTurn` in the store). Queued messages can be reordered
  (`/chat/queue/reorder`), edited in place (`/chat/queue/edit`), or steered
  into the running turn (`/chat/queue/steer`). A message being edited is
  marked held (`/chat/queue/hold`): the follow-up drain skips it and the run
  waits for the edit to finish before sending it. The queue lives server-side (survives clients
  leaving) and processes in order as follow-up turns on the same SSE stream
  once the current turn finishes. A message can be steered explicitly via
  `POST /chat/queue/steer` — it is then injected into the running turn at the
  next round boundary (persisted and emitted as `injected_message`); unconsumed
  steers fall through to the follow-up queue. `GET /chat/queue` lists
  the queue and the pending steers (`steering` array, one entry per message
  steered but not yet injected — the UI shows them as pending rows with a
  spinner until the `injected_message` lands). The chat UI shows the queue as a pinned block above the
  composer. Sessions can be renamed (`POST /sessions/{id}/rename`). Edits and
  retries reject with `run_active` (409) while a run is active.
- Agent tools beyond file/command work: `set_project_name` renames the
  project (emits a `project_renamed` event so the UI header updates) — only
  the project's first session may rename it, later sessions are refused;
  `run_command_background` starts a command detached from the turn (shared
  `agent.BackgroundManager` on the server): the tool returns a job id, and the
  result is persisted as a user message ("[Background #id: …] finished…") when
  the command completes; the running turn's loop injects it at the next round
  (via `injected_message`), so the model can keep working and react when it
  arrives; `remember`/`forget` manage
  project-scoped memories (sqlite `memories` table, injected into the system
  prompt each turn, browsable in the Memories tab); `ask_user` blocks the
  turn on a user question via the `askRegistry` (mirrors the permission
  pattern) and renders as an in-chat answer card — the question is also
  persisted per project in the sqlite `pending_asks` table and surfaced via
  `GET /api/projects/{id}/ask/pending`, so the card survives reloads and
  reconnects (a new turn, an answer, or a timeout clears it). `ask_user` also
  accepts a `questions` array for multi-question asks — the UI renders one
  block the user steps through (back/next, editable answers, "Confirm all"),
  and the tool resolves with the full answers array once confirmed;
  `screenshot_app` is
  vision-gated (above); `fetch_url` fetches web pages for the model
  (HTML → text via `golang.org/x/net/html`).
- Tool results are stored as JSON (the UI parses them) but re-encoded as
  TOON (spec: toon-format/spec) when fed to the model — encoder in
  `internal/agent/toon.go`, applied in `RunChat` history building. The model
  is told about the format in the system prompt.
- SQLite is accessed only through `internal/store`. Settings are key/value
  strings; env vars are fallbacks, sqlite overrides env. Prefix settings keys
  with a domain (`keyLLM...`, `keyGitHub...`).
- LLM providers come from the models.dev catalog (`internal/llm/providers.go`). `providers.json` pins a curated core (id/baseURL); the refresh (`RefreshCatalog`) then appends every other OpenAI-compatible provider from models.dev dynamically, so new providers appear without a code change. `knownBaseURLs` maps ids without a models.dev `api` field; runtime-added providers are persisted separately (not in the cache).
- Frontend: React function components + hooks, Tailwind, no CSS-in-JS. Shared
  UI primitives live in `web/src/components/ui.tsx`; types in `web/src/types.ts`;
  API client in `web/src/api.ts`.
- Frontend/backend JSON fields are camelCase. Backend keys use the `key*`
  constants in `internal/server/server.go`.
- Every settings form that saves through `SaveRow` (ui.tsx) must pass `pulse`
  — a boolean set while the form differs from what's persisted — so the Save
  button glows (`v1-save-breathe`) until the user saves. Match existing call
  sites (Settings.tsx, ToolSettings.tsx, ProjectPane.tsx); don't ship a new
  form with `pulse` omitted or hardcoded.
- `Dialog` (ui.tsx) locks body scroll while open (`overflow: hidden`); dialog
  bodies that scroll use `overscroll-contain` so wheel/touch momentum doesn't
  chain to the page behind the overlay. `fixedBody` splits a fixed header from
  a scrolling body — use it for tall dialogs (tools, model picker).
  `fullScreen` dialogs are full-bleed on mobile and size to
  `max(var(--v1-app-height,0px),100dvh)` like the project shell.
- `ToolSettings` (chat tools dialog + Settings → Tools & permissions) shows one
  section at a time under a fixed `shrink-0` tab bar with a separate scroll
  container below. The tab bar is NOT `sticky` — it sits in a non-scrolling
  flex column so it never moves. On the Settings page the tools pane gets a
  fixed-height flex column (`overflow-hidden` main, no page padding) so the
  same pinning works there; other Settings pages scroll `main` as before.
  `initialPermissionMode` is passed from the chat header so the permission cards
  don't flash the default (`ask`) before the server value loads.
- Token usage shows per turn: the backend stores usage per assistant message,
  and each turn-final assistant message renders its round's counts beneath it
  (from persisted usage on reload and from the live `done` event). There is no
  cumulative session total.
- iOS PWA: the project view root uses `v1-safe-top` so content clears the
  Dynamic Island; the mobile bottom nav uses
  `pb-[calc(env(safe-area-inset-bottom)/2)]` — half the home-indicator inset,
  so the labels just clear the indicator while the bar sits as low as
  possible. `useAppHeight` pins
  `--v1-app-height` to work around stale viewport bounds in standalone mode;
  the root is `h-[max(var(--v1-app-height,0px),100dvh)]` — at cold launch iOS
  JS viewport metrics read too small while `100dvh` is nearly correct, so the
  taller of the two wins. In standalone mode the largest measured height is
  kept (never shrunk), capped at the screen size (zoomed-out/expanded states
  report huge visual viewports — a bogus 1241 once got persisted, so the keys
  are v2), and persisted per orientation in `localStorage`
  (`v1-app-height-v2:<portrait|landscape>`); the inline script in
  `web/index.html` restores it before first paint so cold launches start at
  the last known-good height. Full-screen standalone (window width matches
  the screen) additionally floors the height at the screen size, so a fresh
  install with no stored value can't float the bottom nav above a gap while
  iOS reports the viewport too short at cold launch. INVARIANT: in standalone
  the shell must never be shorter than the visible viewport — preserve the
  screen-size floor and the touch/foreground re-measure (iOS only recalibrates
  stale metrics on interaction) in both files. Keep the inline script and the
  hook in sync.
  The viewport meta locks zoom (`maximum-scale=1, user-scalable=no`) so pinch
  zoom can't distort the metrics again, and the inline script resets
  `scrollRestoration`/`scrollTo(0,0)` — iOS restores PWA scroll offsets
  across launches. The preview iframe sits absolutely positioned inside an
  `overflow-hidden` stage (PreviewPane) because iOS expands in-flow iframes
  to their content height, which stretches the whole page.
- No comments unless they add real value; match surrounding style.

## Boundaries

- Always: run the checks in "Key commands" after changes; keep edits scoped;
  update this file and the README when you change what they document.
- Ask first: git mutations (commit/push/reset/rebase), deleting files or data
  outside the task's scope, installing dependencies, touching `data/` (the dev
  database), changing auth or permissions logic.
- Never: commit secrets or `data/`, weaken path-escape/SSRF guards in the
  agent tools, or add non-stdlib Go dependencies without a stated reason.

Contributor workflow (PR flow, CI gates, releases) is in
[CONTRIBUTING.md](CONTRIBUTING.md).
