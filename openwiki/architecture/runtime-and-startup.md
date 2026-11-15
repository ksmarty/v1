---
type: "Reference"
title: "Runtime and startup wiring"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-8944315688730e7dac923731
    resource: repo://cmd/v1/main.go
  - id: openwiki-source-b79fbbd921df689b4bbdc82f
    resource: repo://docker-compose.yml
  - id: openwiki-source-bb1ebe868e35e9e500714501
    resource: repo://Dockerfile
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
  - id: openwiki-source-7d4ac4eaf17b216fdc5fd692
    resource: repo://internal/server/server.go
  - id: openwiki-source-4a81fcd95533ed8ba5a77739
    resource: repo://internal/store/store.go
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Runtime and startup wiring

v1 is one Go binary. It serves the SPA, the HTTP API, preview proxies and
terminal websockets, and it spawns exactly one child process of its own — the
pi-durable sidecar — which it owns for the life of the process. Everything else
(preview dev servers, shells, headless Chrome) is created lazily per project and
destroyed by a manager.

`cmd/v1/main.go` is the whole of `main`: read config, register secrets, open the
store, build the server, start background jobs, start the sidecar, serve, wait for
SIGINT/SIGTERM, drain in order.

## Configuration: environment read once

`config.Load(version, commit)` (`internal/config/config.go:69`) fills a `Config`
struct from the environment exactly once, at startup. There is no config file and
no hot reload — the settings that *are* runtime-tunable live in the database and
are read per request from `GetUserSetting`.

Defaults (`internal/config/config.go:70-88`):

| Field | Env var | Default |
|---|---|---|
| `Port` | `V1_PORT` | `8080` |
| `DataDir` | `V1_DATA_DIR` | `./data` |
| `AuthDisabled` | `V1_AUTH_DISABLED` | off (`true`/`1`/`yes` enable) |
| `Password` | `V1_PASSWORD` | empty |
| `AllowSignup` | `V1_ALLOW_SIGNUP` | off |
| `OpenAIKey` | `OPENAI_API_KEY` | empty |
| `OpenAIBase` | `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| `Model` | `V1_MODEL` | `gpt-4o` |
| `GitHubToken` | `V1_GITHUB_TOKEN`, else `GITHUB_TOKEN` | empty |
| `MaxPreviews` | `V1_MAX_PREVIEWS` | `3` |
| `ContextBudget` | `V1_CONTEXT_BUDGET` | `12000` |
| `ContextThreshold` | `V1_CONTEXT_THRESHOLD` | `0.80` |
| `MaxConcurrentRunsPerUser` | `V1_MAX_CONCURRENT_RUNS_PER_USER` | `2` |
| `TurnSoftTimeout` | `V1_TURN_SOFT_TIMEOUT` | `5m` |
| `TurnHardTimeout` | `V1_TURN_HARD_TIMEOUT` | `10m` |
| `VerifyEnabled` | — | `true` |
| `AutoPlan` | `V1_AUTO_PLAN` | `true` |
| `SystemPrompt` | `V1_SYSTEM_PROMPT` | empty |
| `HarnessMode` | `V1_HARNESS` | `pi` |
| `SidecarCmd` | `V1_SIDECAR_CMD` | `node` |
| `SidecarScript` | `V1_SIDECAR_SCRIPT` | resolved next to the binary |
| `SidecarSocket` | `V1_SIDECAR_SOCKET` | `$V1_DATA_DIR/harness.sock` |
| `HarnessDB` | `V1_HARNESS_DB` | `$V1_DATA_DIR/harness.sqlite` |
| `MaxSidecarRestarts` | `V1_SIDECAR_MAX_RESTARTS` | `3` |
| `HarnessDebug` | `V1_HARNESS_DEBUG` | off |

Also read but not in that block: `V1_AUTH_OIDC_ENABLED`, `V1_OIDC_ISSUER`,
`V1_OIDC_CLIENT_ID`, `V1_OIDC_CLIENT_SECRET`, `V1_OIDC_REDIRECT_URI`,
`V1_OIDC_ALLOWED_EMAILS` / `V1_OIDC_ADMIN_EMAILS` (comma lists), the GitHub OAuth
pair `V1_GITHUB_OAUTH_CLIENT_ID`/`_SECRET`, and the Vercel credentials
`V1_VERCEL_TOKEN`/`_CLIENT_ID`/`_CLIENT_SECRET`/`_REFRESH_TOKEN`/`_REDIRECT_URI`
(`internal/config/config.go:108-160`).

**Every numeric variable is validated and ignored when invalid** rather than
clamped: `V1_PORT` must parse and land in `1..65535`, `V1_CONTEXT_THRESHOLD` must
be in `(0,1]`, timeouts must parse as positive durations. A typo therefore leaves
the documented default in place instead of putting the process in a state nobody
tested (`internal/config/config.go:89-186`).

`V1_HARNESS` accepts only the two known modes (`pi`, `go`) — anything else is
silently ignored and the default stays, so `V1_HARNESS=go` is the documented
escape hatch to the built-in Go agent loop
(`internal/config/config.go:196-217`).

## Step by step

### 1. Secrets first

`main` calls `sanitize.SetSecrets(...)` with the eight credentials v1 itself holds
— password, GitHub token and OAuth secret, Vercel token/secret/refresh token,
OIDC client secret, OpenAI key — before anything else
(`cmd/v1/main.go:49-59`). The comment states why this is the first thing done:

> Register v1's own credentials so they are redacted from anything on its way to a
> provider. An agent can read a secret from anywhere — a project's .env,
> /proc/1/environ — and whatever it prints is persisted and sent to the model;
> matching on the value is what reliably stops that.

Pattern matching cannot catch an arbitrary secret; knowing the literal can.

### 2. Store open and migrate

`store.Open(cfg.DataDir)` (`internal/store/store.go:36`) creates
`<dataDir>/projects`, opens `<dataDir>/v1.db`, and applies two pragmas before
anything else: `journal_mode=WAL` and `busy_timeout=5000`
(`internal/store/store.go:44-53`). `db.SetMaxOpenConns(1)` (`:44`) serializes all
database access through one connection — v1's write load is trivial, and one
connection removes the "database is locked" class of SQLite failure entirely
rather than tuning around it.

`migrate(db)` (`internal/store/store.go:62`) is a single idempotent script of
`CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS` statements — 24 of
them — followed by additive column checks against `PRAGMA table_info`
(`internal/store/store.go:472`). There is no migration table and no version
counter: the schema is the script, every start re-runs it, and new columns are
added when absent. That is why an older binary cannot corrupt a newer database's
data silently — it simply ignores columns it does not know.

A failure at this stage is fatal (`cmd/v1/main.go:62-65`); there is no running
without a store.

### 3. Server construction

`server.New(cfg, st)` (`internal/server/server.go:94-133`) builds every
long-lived collaborator in one place: the auth manager, the preview manager
(capped at `cfg.MaxPreviews`), the terminal manager, the turn manager, the
background job manager, the OAuth/OIDC/Vercel flow maps, the permission and ask
registries and the MCP manager.

Two things happen here that are *not* constructors but boot-time repair:

- `s.auth.BootstrapAdmin()` (`internal/server/server.go:112`) creates the admin
  account from `V1_PASSWORD` only when the user table is empty.
- `s.ensureBuiltinExtensions()`, `s.ensureBuiltinSkills()` and
  `s.refreshBuiltinSkills()` (`:121-126`) write the bundled extension and skill
  files onto the data volume, so a fresh install has them and an existing install
  picks up a shipped `SKILL.md` change.

Finally the routes are registered and the mux is wrapped exactly once:
`s.handler = s.auth.Middleware(mux)` (`internal/server/server.go:130`). One
wrapper is the invariant: a route cannot accidentally be public, because
publicness is decided by path in the middleware, not by how a handler was
registered (see [HTTP API and authentication](./http-api-and-auth.md)).

### 4. HTTP server, signals, background jobs

`httpSrv` is a plain `http.Server` on `:cfg.Port` with `srv.Handler()`
(`cmd/v1/main.go:67-71`). Then a signal-aware context is created for
`os.Interrupt` and `SIGTERM` (`:73-74`), and two startup jobs are kicked off
inside it:

- `srv.StartEphemeralSweeper(ctx)` (`:78`) — the hourly archive of idle ephemeral
  chats (see [Preview, terminal, screenshots, git and deploy](./preview-terminal-git.md)).
- `srv.LoadGitHubScopes(ctx)` (`:82`) — fetches GitHub's OAuth scope catalog from
  its docs so the scope picker is populated without a hardcoded list.

Both are best-effort and both stop when the context is cancelled.

### 5. The sidecar starts before the listener

`startHarness(ctx, cfg)` (`cmd/v1/main.go:146`) returns `nil, nil` when
`cfg.HarnessEnabled()` is false, i.e. `V1_HARNESS=go`, and logs that the built-in
loop is in use. Otherwise it spawns the sidecar with the socket, the harness
database, the extensions directory, `ClientVersion: "v1/<version>"` and
`MaxRestarts: cfg.MaxSidecarRestarts` (`:152-165`).

The ordering is the point, and the comment in `main` says so:

> The pi-durable sidecar is the default chat harness. It starts before the server
> accepts traffic so a sidecar that cannot start, handshake or agree on the
> protocol fails startup loudly instead of failing the user's first chat turn.
> (`cmd/v1/main.go:84-87`)

A failure here is fatal, and the message names both fixes — `make sidecar-deps`,
or `V1_HARNESS=go` (`:90-94`). A silent fall back to the Go loop was rejected
because it would hide the fact that the requested harness is not running.

`sidecarScript(cfg)` (`cmd/v1/main.go:170-186`) resolves the entrypoint in a fixed
order: `V1_SIDECAR_SCRIPT`, then `<dir of the v1 binary>/sidecar/{dist,src}/host.js`,
then the repo checkout's `sidecar/{dist,src}/host.js`. That order is what makes
the same binary work in a container (sidecar copied beside it) and in
`make dev-backend` (repo checkout), and `dist/host.js` is preferred over
`src/host.js` when a bundled copy exists.

The listener only starts after the sidecar is up and the bridge is installed with
`srv.SetHarness(harness.NewBridge(sup, log.Printf))` (`cmd/v1/main.go:97-101`), in
a goroutine (`:103-108`). See
[The pi-durable harness sidecar](./harness-sidecar.md) for what the bridge does.

### 6. Shutdown is ordered, not parallel

On signal (`cmd/v1/main.go:110-142`):

1. **Wait for running turns**, up to 10 minutes
   (`if n := srv.WaitForTurns(10 * time.Minute); n > 0` `:118-119`). A turn is not
   cut off mid-generation: "the user would come back to a truncated answer and an
   error row" (`:112-117`). Docker is the real deadline — it SIGKILLs after its
   stop timeout unless compose raises `stop_grace_period`, and the checked-in
   compose file sets `stop_grace_period: 10m`.
2. **`srv.Shutdown()`** (`:121`) stops previews, terminals, background jobs and
   MCP servers (`internal/server/server.go:138-146`).
3. **Drain HTTP** with a 10-second timeout (`:123-127`).
4. **`sup.Close()`** (`:132-136`) — the sidecar stops *before* the store closes,
   because it holds the durable transcript open.
5. **`st.Close()`** (`:137-139`).

The order is the whole contract: turns before processes, processes before the
listener, listener before the database.

## The embedded SPA

The frontend is compiled into the binary: `//go:embed all:dist` into `distFS`
(`internal/server/server.go:37-38`), served by `handleSPA`
(`internal/server/server.go:1126`) through `fs.Sub(distFS, "dist")`. The
consequence is a build-order requirement, not just a packaging choice — the
Dockerfile copies the built assets into `internal/server/dist` *before*
`go build`, and says so: "The backend embeds the frontend via //go:embed at
internal/server/dist, so the built assets must be in place BEFORE `go build`"
(`Dockerfile:26-29`).

An unknown non-API `GET` falls back to `index.html` so client-side routes survive
a reload; a binary built without the assets answers "frontend not built" instead
of a confusing 404.

## Health

`GET /api/healthz` (`internal/server/server.go:1065-1067`) returns
`{"ok": true, "version": …}`. It is one of the two paths the auth middleware lets
through unauthenticated (the other is `/api/auth/`), which is what makes it usable
as a container healthcheck.

## Invariants

- Configuration is read once from the environment; invalid values fall back to
  defaults rather than being clamped.
- v1's own credentials are registered with `sanitize.SetSecrets` before anything
  can make a provider call.
- Exactly one auth middleware wraps the mux.
- The store opens with `SetMaxOpenConns(1)`, WAL and a busy timeout; migrations are
  idempotent and additive.
- The sidecar starts before the listener, and its failure is fatal.
- Shutdown order: running turns → previews/terminals/jobs/MCP → HTTP → sidecar →
  store.
- The SPA is embedded, so the frontend must be built before the Go binary.

## Related

- [Quickstart](../quickstart.md) — getting a process running.
- [HTTP API and authentication](./http-api-and-auth.md) — the mux and middleware
  this page constructs.
- [The pi-durable harness sidecar](./harness-sidecar.md) — the one child process.
