---
type: "Reference"
title: "HTTP API, auth and per-user scope"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-b12cba1b5936a1da8739d39c
    resource: repo://internal/auth/auth.go
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
  - id: openwiki-source-e3a61e665fea9fa4cdf242f1
    resource: repo://internal/server/handlers_auth.go
  - id: openwiki-source-e5205101ceb67bc957950b88
    resource: repo://internal/server/handlers_oidc.go
  - id: openwiki-source-7d4ac4eaf17b216fdc5fd692
    resource: repo://internal/server/server.go
  - id: openwiki-source-4a81fcd95533ed8ba5a77739
    resource: repo://internal/store/store.go
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# HTTP API, auth and per-user scope

v1 is a single Go binary that serves both the JSON API and the built SPA. Every
request passes through one auth middleware, lands on one `http.ServeMux`, and is
answered by a handler that resolves the current user, the user's settings and the
project it is allowed to touch. This page is the map of that surface: the route
groups, the middleware contract, accounts and OIDC, the two-layer settings model,
and the ownership rule that keeps one user's projects invisible to another.

Turn mechanics are
[Chat turns, queue, steer and the live stream](./chat-turns-and-queue.md);
what a turn can do to the world is
[Agent tools, tool results and events](./agent-tools-and-events.md).

## The request path

```
browser ──▶ auth.Manager.Middleware ──▶ *http.ServeMux ──▶ handler
                 │                          │
                 ├─ public: /api/healthz,    ├─ /api/... → JSON handlers
                 │   /api/auth/*, SPA shell  ├─ /preview/{id}/ → proxy
                 └─ else: session or 401     └─ GET / → embedded SPA
```

`New` wires the middleware around the mux once
(`internal/server/server.go:130`), so `Handler()` (`internal/server/server.go:135`)
is the single entry point the test suite drives with `s.Handler().ServeHTTP(...)`.
The mux itself is built by `func (s *Server) routes(m *http.ServeMux)`
(`internal/server/server.go:169`) using Go 1.22 method patterns — `GET /api/projects/{id}`,
`PATCH /api/users/{id}`, `DELETE /api/...` — so a path is never matched by the wrong
verb and a handler never has to check `r.Method` itself.

## The auth middleware contract

`func (m *Manager) Middleware(next http.Handler) http.Handler`
(`internal/auth/auth.go:222`) sorts every request into exactly one of four cases,
in this order:

| Case | Test | Result |
| --- | --- | --- |
| Auth disabled | `m.disabled` (`V1_AUTH_DISABLED=true`) | Synthetic `{ID:"local", Username:"local", IsAdmin:true}` is attached; everything is allowed |
| Public API | `/api/healthz` or prefix `/api/auth/` | Passed through with no user attached |
| SPA shell and assets | path is not under `/api/` and not under `/preview/` | Served; the session user is attached **when one exists** so the app can render personalised state on first paint |
| Everything else | `/api/...` and `/preview/...` | A valid session is required, otherwise `401` with `Content-Type: application/json` and body `{"error":"unauthorized"}` |

Two consequences worth knowing when adding a route:

- A new endpoint is protected automatically. Nothing under `/api/` is public by
  accident; the public list is an explicit allowlist of two prefixes.
- The preview proxy is inside the boundary. `/preview/{id}/` is matched by the
  middleware's `HasPrefix("/preview/")` branch, and `handlePreviewProxy`
  (`internal/server/server.go:1071`) re-checks project ownership before it
  forwards to the dev server — so a preview URL is not a capability URL.

Sessions are opaque server-side tokens in an `HttpOnly` cookie named `v1_session`
(`internal/auth/auth.go:18`), created by `CreateSession`
(`internal/auth/auth.go:139`) with `SameSite=Lax`, `Secure` set from
`isSecure(r)` (`r.TLS` or `X-Forwarded-Proto: https`, so it works behind a TLS
terminating proxy), and a 30-day lifetime (`sessionTTL`,
`internal/auth/auth.go:20`). `DestroySession` (`internal/auth/auth.go:157`) clears
it with `MaxAge: -1`.

## Accounts

There is no user table seeded at install time; the first account is whatever is
configured or whoever signs up first.

- **Bootstrap.** `BootstrapAdmin` (`internal/auth/auth.go:52`) creates `admin`
  from `V1_PASSWORD` only when `UserCount() == 0`, hashes with bcrypt
  (cost default, `HashPassword`, `internal/auth/auth.go:45`), marks it admin, and
  calls `ClaimOwnerlessProjects` so projects created before multi-user existed are
  not orphaned.
- **Setup screen.** `GET /api/auth/status` (`internal/server/handlers_auth.go:30`)
  reports `setupRequired`, and `POST /api/auth/setup`
  (`internal/server/handlers_auth.go:77`) creates the first account as admin.
  `SetupRequired` (`internal/auth/auth.go:76`) is false when auth is disabled or
  OIDC is on — in both cases there is nothing to set up with a password.
- **Signup.** `POST /api/auth/signup` (`internal/server/handlers_auth.go:110`)
  answers `403 signup is disabled` unless `V1_ALLOW_SIGNUP` was set, which is
  surfaced to the UI as `signupEnabled` (`internal/server/handlers_auth.go:48`).
- **Usernames.** `usernameRe` (`internal/server/handlers_auth.go:19`) allows
  `^[a-zA-Z0-9._-]{3,32}$`; `validateUsername` rejects anything else with
  "username must be 3–32 characters: letters, digits, . _ -".
- **Admin surface.** `/api/users` (list, create, `PATCH`/`DELETE /api/users/{id}`)
  and `GET|POST /api/settings/oidc` are gated by `requireAdmin`
  (`internal/server/server.go:372`), which writes `403 admin required`.

### OIDC

OIDC is off unless `V1_AUTH_OIDC_ENABLED=true` (`internal/config/config.go:123`),
and its configuration has two layers that are deliberately asymmetric:

- Environment (`V1_OIDC_ISSUER`, `V1_OIDC_CLIENT_ID`, `V1_OIDC_CLIENT_SECRET`,
  `V1_OIDC_REDIRECT_URI`, `V1_OIDC_ALLOWED_EMAILS`, `V1_OIDC_ADMIN_EMAILS`) is the
  deployment-level source of truth.
- Admin-saved settings (`keyOIDCIssuer`, `keyOIDCClientID`, `keyOIDCClientSecret`,
  `keyOIDCCallbackURI`, `keyOIDCAllowedEmails`) fill the gaps;
  `oidcConfig()` (`internal/server/server.go` ~495) merges saved over env, and an
  empty saved value falls back to env.

`handleOIDCSettings` (`internal/server/handlers_oidc.go:54`) therefore also returns
`issuerFromEnv`, `clientIdFromEnv`, `clientSecretFromEnv`, `callbackUriFromEnv` and
`allowedEmailsFromEnv`, so the UI can render those fields read-only: editing a
value the environment already defines would just be overwritten at the next
restart. `handleOIDCSettingsSave` (`internal/server/handlers_oidc.go:80`) writes
the keys and calls `rebuildOIDC`; an empty client secret keeps the previously
saved one, so saving the form twice does not wipe the secret.

The sign-in flow is a standard authorisation-code flow with PKCE and a nonce,
kept in an in-memory map keyed by `state`:

1. `GET /api/auth/oidc/start` (`internal/server/handlers_oidc.go:124`) mints a
   32-byte `state`, a PKCE verifier and a nonce, stores the flow with
   `oidcFlowTTL`, then redirects to the issuer. A discovery failure returns `502`
   with the issuer in the message because that is the error an operator can act on.
2. `GET /api/auth/oidc/callback` (`internal/server/handlers_oidc.go:173`) looks up
   and **immediately deletes** the state (single use, expired flows dropped), then
   `VerifyCode` checks the code, verifier and nonce.
3. `Allowed(email)` enforces the allowlist; a rejection redirects to
   `/login?oidc=denied` rather than rendering an error page, so the login screen
   stays the single place a failure is explained.
4. A first-time email is auto-provisioned as a user with a random password hash and
   `OIDC = true`; emails listed in `V1_OIDC_ADMIN_EMAILS` become admins, and a
   listed email signing in later is promoted (`SetUserAdmin`).
5. A session cookie is created and the browser is redirected into the app.

`pruneOIDCFlows` (`internal/server/handlers_oidc.go:110`) drops expired entries, so
the map cannot grow without bound.

## Per-user and global settings

Every setting lives in one of two tables: `user_settings` (per account) and the
shared `settings` table (instance-level). The single helper that defines the
layering is `func (s *Server) userSetting(userID, key string) (string, bool)`
(`internal/server/server.go:539`): the user's own value wins when it is present
**and non-empty**, and otherwise the shared/global value is returned. That is what
makes an admin-set instance-wide key work as a default while still letting a user
override it, and what makes "clear this field" behave like "fall back" instead of
"blank it out".

`handlePutSettings` (`internal/server/handlers_auth.go:261`) implements the two
halves with two closures:

- `set(key, *string)` writes a user setting; an empty string calls
  `DeleteUserSetting`, which is exactly the fall-back semantics above.
- `setGlobal(key, *string)` writes the shared row, used only where one value per
  install is the correct model: the GitHub OAuth app credentials
  (`keyGitHubOAuthClientID`, `keyGitHubOAuthClientSecret`) and the Vercel OAuth
  app credentials (`keyVercelOAuthClientID`, `keyVercelClientSecret`) — one OAuth
  app per install, so a per-user copy would be meaningless.

Keys that stay global therefore form a short list: OAuth app credentials for
GitHub/Vercel, the OIDC configuration, and the provider catalogue caches
(`keyProvidersCache`, `keyProvidersCustom`). Everything a person can reasonably
want their own way — permission mode, thinking default, TOON, tool exposure,
disabled tools, timeouts, terminal font, auto-push, context threshold, appearance,
system prompt — is per-user.

`handleGetSettings` (`internal/server/handlers_auth.go:188`) is the read side and
follows one rule that matters for anything secret: it never echoes a stored
secret, only a boolean plus a hint (`apiKeySet`, `apiKeyHint(p.APIKey)`,
`github.tokenSet`, `webSearch.keySet`). The settings form can therefore show
"configured" without the browser ever holding the key.

The LLM block of that response is the shape the rest of the server depends on:
`llm{baseURL, model, defaultModel, apiKeySet, apiKeyHint, models, providers[{id,
name, baseURL, model, apiKeySet, apiKeyHint, disabled}], currency}`. On the write
path the provider merge has three rules worth remembering:

- A record without an `id` gets `store.NewID()`; an empty `apiKey` keeps the stored
  key of the existing record (or inherits the legacy single-provider key), because
  the form never shows the key back for editing.
- Changing the provider list calls `invalidateCustomModelsCache()`, so the next
  `/api/providers` request refetches instead of serving a stale model list.
- The effective single-provider keys (`keyLLMBaseURL`/`keyLLMAPIKey`/`keyLLMModel`)
  are kept in step with the saved record whose base URL matches, and on a fresh
  install mirrored from the first non-disabled provider — so a second provider can
  never silently overwrite the config that provider-less code paths use.

## Project ownership

Ownership is a column on the project row, and the rule is enforced in one helper:
`projectOr404` (`internal/server/server.go:380`) returns `404 project not found`
when auth is on and `p.OwnerID != u.ID`. A `403` would confirm that a project with
that id exists, so the API deliberately cannot be used to enumerate other users'
work. `handlePreviewProxy` repeats the same check (`internal/server/server.go:1081`)
because the preview is a byte-stream proxy, not a JSON handler. When auth is
disabled the check is skipped, which is why single-user installs behave as they
always did.

`ClaimOwnerlessProjects` (`internal/store/store.go:668`) is the migration hook:
`UPDATE projects SET owner_id = ? WHERE owner_id IS NULL OR owner_id = ''`. It runs
after the bootstrap admin is created and after the first setup account, so
pre-multi-user projects belong to whoever set the instance up.

## Route groups

The routes below are registered in `routes` (`internal/server/server.go:169-331`).
Read it as a table of capabilities, not an exhaustive list.

| Group | Example patterns | Notes |
| --- | --- | --- |
| Health | `GET /api/healthz` | Public |
| Auth | `POST /api/auth/login`, `GET /api/auth/oidc/start`, `POST /api/auth/logout` | Public prefix |
| OAuth apps | `GET /api/auth/{github,vercel}/oauth/start` | Instance credentials, per-user token |
| Users | `GET /api/users`, `PATCH /api/users/{id}` | Admin only |
| Settings | `GET|PUT /api/settings`, `POST /api/settings/test-llm` | Per-user + global layering |
| Providers | `GET /api/providers`, `GET /api/providers/usage`, `POST /api/providers/add` | Usage/thinking are provider-scoped |
| Projects | `GET /api/projects`, `POST /api/projects/import`, `DELETE /api/projects/{id}` | Ownership enforced |
| Project files | `GET|PUT|DELETE /api/projects/{id}/file` | Path-escaped by the agent's `resolve` |
| Messages | `GET /api/projects/{id}/messages`, `/messages/truncate` | Paged transcripts |
| Chat | `POST /api/projects/{id}/chat`, `/retry`, `/stop`, `/status`, `/watch` | See the turns page |
| Queue | `POST /api/projects/{id}/chat/queue` + reorder/steer/edit/hold/resume | One run per session |
| Sessions | `POST /api/projects/{id}/sessions/fork`, `/rename`, `/archive` | Archive keeps history |
| Ephemeral state | `/memories`, `/todos`, `/ask`, `/background`, `/uploads` | Per project/session |
| Preview | `GET /api/projects/{id}/preview/start`, `/preview/{id}/` proxy | Proxy is inside auth |
| Terminal | `GET /api/projects/{id}/terminal` | WebSocket upgrade |
| Git / GitHub | `GET /api/projects/{id}/git/status`, `/github/push` | Token resolved per user |
| MCP | `GET /api/mcp/status`, `/mcp/oauth/start` | Per-user servers |
| Skills / extensions | `GET /api/skills`, `POST /api/extensions/{id}/save` | See extensions page |
| SPA | `GET /` | Catch-all, last |

Order matters at one point: `GET /` is registered last
(`internal/server/server.go:330`), and the preview proxy loop enumerates all six
methods (`internal/server/server.go:327`) — a bare `"/preview/{id}/"` pattern
would conflict with the `GET /` catch-all.

## JSON helpers

Four helpers stand between a handler and the wire, and each exists because of a
specific failure:

- `writeJSON` (`internal/server/server.go:335`) marshals **before** it calls
  `WriteHeader`. Encoding directly into the response after `WriteHeader` turns an
  encoding failure into a `200` with an empty body, which clients report as a
  confusing JSON syntax error (Safari: "The string did not match the expected
  pattern") instead of the real fault. On failure it logs and substitutes a `500`
  with `{"error":"failed to encode response"}`.
- `writeError` (`internal/server/server.go:351`) shapes `{"error": msg}`.
- `decodeJSON` (`internal/server/server.go:355`) answers `400 invalid JSON body: …`
  and returns false, so handlers stay linear.
- `currentUser` (`internal/server/server.go:366`) reads the user the middleware
  attached; it is never nil for an authenticated API request.

The SPA is served from the embedded `dist` tree by `handleSPA`
(`internal/server/server.go:1126`): a real file is served with `Cache-Control:
no-cache` through `http.ServeContent`, and any unknown non-API path falls back to
`index.html` so client-side routing survives a reload. A binary built without the
frontend answers `frontend not built` in plain text rather than a blank page.

## Invariants

- **The public surface is two prefixes.** `/api/healthz` and `/api/auth/` are the
  only unauthenticated API paths; everything else under `/api/` and `/preview/`
  requires a session cookie.
- **A missing resource and a forbidden resource look the same.** Project-scoped
  handlers return `404`, never `403`, so ids cannot be probed.
- **Empty means fall back.** A settings write of `""` deletes the user row and
  re-exposes the instance value; that is why the settings form can be cleared.
- **Secrets are write-only.** No response contains a stored API key, token or
  client secret — only `*Set` booleans and hints.
- **Env beats the database for deployment-level configuration.** OIDC and OAuth app
  credentials come from env first; the UI shows those fields as read-only.
- **One mock per install.** OAuth app credentials and OIDC configuration are
  instance-level rows, never per-user copies.
- **Register specific patterns before `GET /`.** The SPA catch-all is last, and the
  preview proxy enumerates its methods.
