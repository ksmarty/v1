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
backend and frontend changes respectively.

## Conventions

- Go: stdlib only for new dependencies unless unavoidable; `net/http` (Go 1.22+
  method-pattern routes), no framework. Errors returned upward, handled at the
  boundary. `writeJSON`/`writeError`/`decodeJSON` helpers live in
  `internal/server/server.go`.
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
- No comments unless they add real value; match surrounding style.
