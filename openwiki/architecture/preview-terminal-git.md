---
type: "Reference"
title: "Preview, terminal, screenshots, git and deploy"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-bb1ebe868e35e9e500714501
    resource: repo://Dockerfile
  - id: openwiki-source-b12cba1b5936a1da8739d39c
    resource: repo://internal/auth/auth.go
  - id: openwiki-source-c7892bbdfc3002a62a48afad
    resource: repo://internal/gitops/gitops.go
  - id: openwiki-source-3482bf7b6e9faaad4cf782ec
    resource: repo://internal/preview/preview.go
  - id: openwiki-source-6a3a0486045833720e676926
    resource: repo://internal/screenshot/screenshot.go
  - id: openwiki-source-2fcc0ac15ad0af123578c81f
    resource: repo://internal/server/handlers_chat.go
  - id: openwiki-source-c1241311ff1ed46b365baa6b
    resource: repo://internal/server/handlers_github.go
  - id: openwiki-source-8bf2d8024479bd3b5f464d4b
    resource: repo://internal/server/handlers_projects.go
  - id: openwiki-source-7d4ac4eaf17b216fdc5fd692
    resource: repo://internal/server/server.go
  - id: openwiki-source-1b4ce084026da8d319f11e73
    resource: repo://internal/terminal/terminal.go
  - id: openwiki-source-a3a01632fab86b0569f0dbb4
    resource: repo://internal/vercel/vercel.go
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Preview, terminal, screenshots, git and deploy

v1 runs other people's development servers, other people's shells, a browser and a
git client. Each of those is a long-lived process or a foreign API, so each sits
behind a small manager with an explicit lifetime rather than being started inline
by a handler:

| Area | Package | Lifetime |
|---|---|---|
| preview dev server / static root | `internal/preview` | per project, capped, evicted, idle-swept |
| shell | `internal/terminal` | per websocket, killed on close |
| headless browser | `internal/screenshot` | one process, shared and serialized |
| git | `internal/gitops` | no process (go-git), no `git` binary |
| GitHub / Vercel | `internal/gitops`, `internal/vercel` | per-request HTTP |

## Previews: a manager, not a handler

`preview.Manager` (`internal/preview/preview.go:115-122`) owns `items
map[string]*Preview`, a concurrency cap and an idle timeout, with a separate
`startMu` so two simultaneous starts cannot both scan for a free port. Each
`Preview` (`:67-82`) carries the mode (`"static"` or `"node"`), the port, whether
it is a Vite app, the command, the process-group id, a ring buffer of logs and a
`done` channel.

The cap is small on purpose — `if max <= 0 { max = 3 }`
(`internal/preview/preview.go:127-129`) — because each node preview is a real
`npm`/`pnpm` process tree holding several hundred megabytes. Three is what a
single-user instance can keep warm; more is what the eviction and idle sweeps
exist to avoid.

`Start` (`:195`) is deliberately boring about reuse: an existing preview that is
still running is touched and returned (`:199-205`), so the caller never has to
know whether it was already up.

- **Static projects start no process at all.** With no `package.json` the preview
  is a record with `Mode: "static"`, `running: true` and no port
  (`:210-219`) — "Static projects need no subprocess at all." The proxy serves the
  directory off disk.
- **The cap is enforced before the expensive work** (`:227-230`), so a start that
  must evict does not first install a dependency tree and then throw it away.
- **Ports come from a small fixed range**, `portMin = 4100`, `portMax = 4199`
  (`internal/preview/preview.go:18-21`), scanned by `freePort` (`:438`); the range
  is narrow because these ports are never published, only proxied on loopback.
- **Dependencies are installed only when `node_modules` is missing**
  (`:258-263`), with a 10-minute timeout (`:488`) and a pnpm→npm fallback
  (`:471-476`). The install disables pnpm's strict-dep-builds guard, because
  modern packages ship platform binaries through optionalDependencies and work
  without their postinstall (`:482-485`).
- **The dev command differs per framework**, and each difference is a real
  failure that was fixed once: `next dev` takes `--hostname` because `--host` was
  dropped in Next 15+ (`:275-279`), Vite needs `--base /preview/<id>/` to be
  proxyable under a subpath (`:280-283`), npm needs `--` before script args while
  pnpm forwards them verbatim (`:285-297`).
- **The child gets its own process group** — `syscall.SysProcAttr{Setpgid: true}`
  (`:303`) — because `kill(p.pgid)` negates the pid to signal the whole group
  (`:393`): killing only the `sh -c` wrapper would leave `vite` holding the port.
- **Readiness is a real gate**: the port must accept connections within 60s
  (`:318-333`), and failure returns the tail of the log (`:322`, `:330`) rather
  than a bare timeout, because "did not start" without the compiler error is
  useless.

Lifetime ends three ways: `Stop`/`StopAll` (`:340-359`), LRU eviction when the cap
is reached (`evictLocked`, `:402-426`), and the background idle sweep
(`sweepIdle`, `:140-156`) which stops previews idle for
`previewIdleTimeout = 15 * time.Minute` (`:136`) "so abandoned dev servers don't
linger". `TouchRevision`/`Revision` (`:161-168`) is a separate, tiny mechanism:
every file write bumps a per-project counter so the preview iframe reloads when
the agent changes the workspace.

The HTTP surface is three routes (`internal/server/server.go:265-267`) and a
shutdown hook that stops everything (`internal/server/server.go:139`).

## The preview proxy, and why screenshots bypass it

`handlePreviewProxy` (`internal/server/server.go:1071`) resolves the project,
enforces ownership, then branches:

- **Static**: the request path is stripped of `/preview/<id>/` and handed to
  `http.FileServer(http.Dir(p.Path))` (`:1092-1099`). No process, no port.
- **Node**: an `httputil.ReverseProxy` to `127.0.0.1:<port>` (`:1101-1121`). Vite
  apps get their path forwarded unchanged because they were started with `--base`
  and expect the proxy prefix (`:1106-1107`); everything else has the prefix
  stripped (`:1109`). A dead upstream becomes a 502
  `preview unavailable: …` (`:1117-1119`) instead of a hang.

The proxy is registered for `GET/POST/PUT/PATCH/DELETE/OPTIONS` explicitly
(`server.go:323-328`) — the comment says why: the patterns must be enumerated so
they do not conflict with the `GET /` SPA catch-all registered immediately after
(`:330`).

**Why the proxy needs a session, and what `DirectURL` is for.** The whole mux is
wrapped once by `auth.Manager.Middleware` (`internal/server/server.go:128-131`),
and that middleware deliberately lets the SPA shell and bundled assets through
unauthenticated so the router can send a signed-out user to `/login`
(`internal/auth/auth.go:233-243`) — but `/api/` **and `/preview/`** are the auth
boundary (`:246-252`), so a preview response is only served to a request carrying
a session cookie. That is what makes a preview shareable-safe: `/preview/<id>/` is
a path on the v1 origin, so without the check anyone with the URL could read
another user's app.

A headless Chrome cannot present that cookie, so screenshots take the other door.
`Manager.DirectURL` (`internal/preview/preview.go:174-192`) returns the
dev-server's own loopback URL — `http://127.0.0.1:<port>/`, or
`http://127.0.0.1:<port>/preview/<id>/` for Vite — and `""` for a static preview
that has no port. `capturePreview` (`internal/server/handlers_chat.go:1153-1168`)
states the tradeoff in its doc comment:

> Node previews are reached directly on their dev-server port (the v1 proxy
> requires a session when auth is on); static previews go through the proxy,
> which needs auth disabled.

So a static project is only screenshot-able when auth is off, while a node project
works either way. The loopback URL is not an exposure: the dev server binds inside
the container, and the port is never mapped.

## Terminal: one websocket, one shell

`terminal.Manager` (`internal/terminal/terminal.go:18-21`) keeps
`sessions map[string]map[*session]struct{}` — per project, because deleting a
project must be able to kill its shells (`KillProject`, `:173-180`) and shutdown
kills all of them (`KillAll`, `:183-192`, called at
`internal/server/server.go:140`).

`ServeWS` (`:49`) is a bridge in both directions: bytes from the pty go out as
`{"type":"output","data":…}`, `{"type":"input"}` and `{"type":"resize"}` come back
(`:129`, `:141-158`). Design points that are load-bearing:

- **Origin is not checked.** "Same-origin is enforced by cookie auth; behind
  reverse proxies the Origin header may not match the Host, so do not reject on
  Origin" (`:35-37`). The session cookie *is* the same-origin proof, and the
  upgrade request cannot set it from another origin.
- **Twenty-five-second pings.** "Reverse proxies (Cloudflare, Traefik) drop
  WebSockets that sit idle — an open terminal with no output looks exactly like
  that" (`:56-58`); the read deadline is 60s (`:60`), refreshed by any pong.
- **A PTY failure is explained.** Without an explicit close frame the browser sees
  code 1006 and an empty terminal; instead the socket closes with
  `failed to start shell: %v` (`:97-104`).
- **The shell is rooted at the project directory** (`cmd.Dir = dir`, `:88`) with a
  minimal environment (`TERM=xterm-256color`, `PS1`, `HOME`; `:91-95`) and an
  initial 24×80 pty (`:96`).
- **Teardown is two-way**: closing the socket kills the process, and the process
  exiting closes the socket (`:106-121`), with `sync.Once` on the kill
  (`:161-170`) so both paths are safe.

The handler (`internal/server/handlers_chat.go:1280-1286`) does no more than
`projectOr404` and hand the project directory to `ServeWS`, route
`GET /api/projects/{id}/terminal` (`internal/server/server.go:269`).

## Screenshots: one browser, one capture at a time

`screenshot.Capture` (`internal/screenshot/screenshot.go:50`) reuses a single
headless Chrome started lazily on first use (`ensureBrowser`, `:26-46`) — the
launch cost is paid once. Captures are serialized by a mutex held for the whole
capture (`:51-52`) because the browser is shared and two tabs driving one Chrome
under load is where flaky screenshots come from.

`V1_CHROME_PATH` overrides the binary (`:39-41`) and the failure message says so
(`:55`), because "no Chrome" otherwise surfaces as an obscure protocol error.
Each capture opens a fresh tab at 1280×800, waits for `body`, then sleeps 1.5s
(`:62-69`) — "SPA dev servers render asynchronously — give the app a moment to
mount and run its first effects before shooting". The per-tab timeout is 30s
(`:59`). `RenderText` (`:80`) reuses the same browser to return rendered HTML for
JS-only pages.

Screenshots are gated on **model capability, not a setting**: the tool is only
installed when `params.Vision` is true (`internal/server/handlers_chat.go:771-775`),
and `Vision` comes from `modelSupportsImages` (`:1186`), whose rule is the
interesting part:

> An unknown id is treated as capable and the provider decides. (`:1181`)

The rationale is quoted in the doc comment: a provider's picker list publishes no
modalities, the composer accepts a hand-typed id, and attachments never consulted
this flag — so hiding the tool for an unrecognised model would tell a model it
cannot see a screenshot while it is already being shown one.

## git: go-git, so the image needs no git binary

`gitops` implements both the GitHub REST calls and the local git operations on top
of go-git (`internal/gitops/gitops.go:1-2`). That is a build constraint made
concrete: the binary is produced with `CGO_ENABLED=0` (`Dockerfile:31`), and a
pure-Go git client means the container does not need `git` installed to commit or
push.

- **Errors keep the status and a snippet.** Every API failure is
  `GitHub API error (HTTP %d): %s` with the body truncated to 300 characters
  (`:99`, `:138`, `:163`, `truncate` `:76-81`), because the useful part of a
  GitHub error is the message field, and the full body can be kilobytes.
- **Auth is a basic-auth password.** `basicAuth` (`:177-182`) returns
  `{Username: "git", Password: token}` — GitHub accepts a token in the password
  slot — and `nil` when there is no token, so a public clone stays anonymous
  rather than sending an empty credential.
- **Responses are capped** at 4 MiB (`io.LimitReader(resp.Body, 4<<20)`, `:65`);
  an unexpected HTML error page from a proxy cannot exhaust memory.
- **Commit and push are separable.** `Commit` (`:513`), `Push` (`:594`) and
  `CommitAndPush` (`:464`) all exist, and each returns a summary string so the UI
  can say what actually happened ("nothing to commit" is not an error).
- **A path must be a repo.** `validate` (`:968`) guards every operation, and
  `CommitIfRepo` (`:982`) is the "commit if this is a repository, otherwise do
  nothing" form used by callers that must not fail a turn over a non-repo project.

`handleGitHubPush` (`internal/server/handlers_github.go:91`) is the flow behind the
push button: it resolves the GitHub client, asks GitHub who the user is so the
commit is attributed to their login, then calls `CommitAndPush` and returns
`{committed, pushed, summary}`. `handleGitHubLink` (`:118`) clones a repo into the
project directory via `LinkRepo` (`internal/gitops/gitops.go:215`), which clones
into a temp directory and renames, so a failed link cannot leave a half-populated
project.

## Vercel

`vercel.Client` (`internal/vercel/vercel.go:51-57`) is a bearer-token client that
can hold a refresh token; a 401 triggers exactly one token refresh inside `do`
(`:112`) rather than a retry loop, so a revoked token fails fast instead of
hammering the API. Deploys are a two-step protocol —
`Deploy` (`:300`) uploads files and returns a deployment id, then `poll` (`:342`)
polls `/v13/deployments/<id>` every 2s with a 10-minute deadline (`:343-376`) —
and the loop only returns early on a terminal ready-state (`READY`, `ERROR*`,
`CANCELED`). A timeout returns the last deployment *and* an error, so the UI can
show the URL of a build that is still going.

## The ephemeral sweep

Ephemeral (scratch) chats are archived, not deleted, on a timer:
`StartEphemeralSweeper` (`internal/server/server.go:841-857`) ticks hourly and
`ephemeralArchiveDays` (`:830-840`) resolves each user's window — default 1 day,
and `0` disables auto-archive for that user. `sweepEphemeralProjects` (`:859-882`)
applies every user's own window to their own projects, and with auth disabled
sweeps once globally. The same expiry also runs when a user loads the project list
(`internal/server/handlers_projects.go:150`), so opening the dashboard never shows
a chat that should already have moved to the archive; the sweeper exists so the
archive happens even when nobody opens the dashboard for a week.

## Invariants

- A preview child always gets its own process group, and it is killed by group —
  never by pid alone.
- The preview proxy's auth gate covers `/preview/` exactly like `/api/`; a
  headless tool that cannot present a cookie must use `DirectURL`, and static
  previews are therefore unscreenshotable with auth on.
- Preview ports are never published; they are loopback-only and reached through
  the proxy or `DirectURL`.
- The terminal is rooted at the project directory, and closing the socket kills
  the shell.
- One shared Chrome, one capture at a time.
- git goes through go-git: no `git` binary dependency, and the build stays
  `CGO_ENABLED=0`.
- Deleting a project kills its preview and its terminals
  (`internal/server/handlers_projects.go:314`).

## Related

- [HTTP API and authentication](./http-api-and-auth.md) — the middleware that
  draws the `/api/` and `/preview/` boundary.
- [Agent tools, tool results and events](./agent-tools-and-events.md) — the
  `screenshot_app`, `run_command` and `git` tools that call into these managers.
