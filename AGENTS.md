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
- `internal/auth/` — password sessions + auth middleware.
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
- Chat runs are single-flight per project (`turnManager` in
  `internal/server/turns.go`): a second `POST /chat` while a run is active is
  queued onto the run's `turnQueue` atomically (`beginOrQueue`, 202 reply).
  The agent drains the queue between rounds (steer — persisted and emitted as
  `injected_message`); leftovers become follow-up turns on the same SSE
  stream (queue). Edits and retries reject with `run_active` (409) while a
  run is active.
- Agent tools beyond file/command work: `remember`/`forget` manage
  project-scoped memories (sqlite `memories` table, injected into the system
  prompt each turn, browsable in the Memories tab); `ask_user` blocks the
  turn on a user question via the `askRegistry` (mirrors the permission
  pattern) and renders as an in-chat answer card; `screenshot_app` is
  vision-gated (above); `fetch_url` fetches web pages for the model
  (HTML → text via `golang.org/x/net/html`).
- Tool results are stored as JSON (the UI parses them) but re-encoded as
  TOON (spec: toon-format/spec) when fed to the model — encoder in
  `internal/agent/toon.go`, applied in `RunChat` history building. The model
  is told about the format in the system prompt.
- SQLite is accessed only through `internal/store`. Settings are key/value
  strings; env vars are fallbacks, sqlite overrides env. Prefix settings keys
  with a domain (`keyLLM...`, `keyGitHub...`).
- LLM providers come from the models.dev catalog (`internal/llm/providers.go`).
  Provider IDs/baseURLs are pinned in `providers.json` (the source of truth for
  base URLs); name/keyHint/doc/models are refreshed from models.dev. New
  providers added at runtime are persisted separately (not in the cache).
- Frontend: React function components + hooks, Tailwind, no CSS-in-JS. Shared
  UI primitives live in `web/src/components/ui.tsx`; types in `web/src/types.ts`;
  API client in `web/src/api.ts`.
- Frontend/backend JSON fields are camelCase. Backend keys use the `key*`
  constants in `internal/server/server.go`.
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
  the last known-good height. Keep the inline script and the hook in sync.
  The viewport meta locks zoom (`maximum-scale=1, user-scalable=no`) so pinch
  zoom can't distort the metrics again, and the inline script resets
  `scrollRestoration`/`scrollTo(0,0)` — iOS restores PWA scroll offsets
  across launches. The preview iframe sits absolutely positioned inside an
  `overflow-hidden` stage (PreviewPane) because iOS expands in-flow iframes
  to their content height, which stretches the whole page.
- No comments unless they add real value; match surrounding style.
