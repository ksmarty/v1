# v1

A self-hosted [v0](https://v0.dev) clone: chat with an AI and it builds real web apps for you — with live preview, a file browser, a built-in terminal, and GitHub integration. Everything ships as **one self-contained Docker container**.

## What it does

- **Chat → app** — describe what you want; the AI scaffolds and iterates on a real web app.
- **Live preview** — every generated app runs its own dev server, streamed straight into the browser.
- **File browser** — inspect and edit the generated source.
- **Terminal** — a real shell inside the container, from the browser.
- **GitHub integration** — import existing repos, or create and push new ones.
- **Your models** — provider selector for any OpenAI-compatible endpoint (models.dev catalog, same data opencode/pi use).
- **Your look** — 17 [Happy Hues](https://www.happyhues.co) themes plus a chat-left/chat-right layout toggle (Settings → Appearance; per-device).

## Quickstart

```bash
docker run -d \
  --name v1 \
  -p 8080:8080 \
  -v v1-data:/data \
  -e OPENAI_API_KEY=sk-... \
  ghcr.io/<owner>/v1:latest
```

Open http://localhost:8080 — on first run you get a setup screen to create your login.

Replace `<owner>` with the GitHub user/org that publishes the image (e.g. your fork).

### docker compose

A [`docker-compose.yml`](docker-compose.yml) is included for convenience:

```bash
# first edit the file: replace <owner> in the image name (or use `build: .`)
docker compose up -d
```

### Build from source

```bash
docker build -t v1:local .
docker run -d -p 8080:8080 -v v1-data:/data v1:local
```

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
| --- | --- | --- |
| `V1_PORT` | `8080` | Port the HTTP server listens on. |
| `V1_DATA_DIR` | `/data` | Data directory (projects, previews, database). Mount a volume here. |
| `V1_AUTH_DISABLED` | `false` | `true` disables auth entirely — localhost/trusted networks only. |
| `V1_PASSWORD` | — | Static login password (skips the first-run setup screen). |
| `OPENAI_API_KEY` | — | API key for the model provider. |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Any OpenAI-compatible endpoint. |
| `V1_MODEL` | `gpt-4o` | Model used to generate apps. |
| `V1_GITHUB_TOKEN` | — | GitHub personal access token (repo import/push). |
| `V1_GITHUB_OAUTH_CLIENT_ID` | — | Client ID of a GitHub OAuth App for device-flow login (see below). |
| `V1_AUTH_OIDC_ENABLED` | — | `true` forces OIDC login on (also auto-enables when all `V1_OIDC_*` are set). |
| `V1_OIDC_ISSUER` | — | OIDC issuer URL. For Authentik: `https://auth.example.com/application/o/<slug>/`. |
| `V1_OIDC_CLIENT_ID` | — | OIDC client ID registered with the provider. |
| `V1_OIDC_CLIENT_SECRET` | — | OIDC client secret. |
| `V1_OIDC_REDIRECT_URI` | — | Callback URI, e.g. `https://v1.example.com/api/auth/oidc/callback`. Auto-derived if unset. |
| `V1_OIDC_ALLOWED_EMAILS` | — | Comma list of emails allowed to log in (empty = any authenticated user). |
| `V1_OIDC_ADMIN_EMAILS` | — | Comma list of emails granted the admin role on OIDC sign-in (case-insensitive; applied on first login and promotions). |
| `V1_MAX_PREVIEWS` | `3` | Max preview dev servers running at once. |
| `V1_ALLOW_SIGNUP` | `false` | `true` lets anyone register a (non-admin) account from the login page. |
| `V1_VERCEL_TOKEN` | — | Vercel personal access token (see Vercel integration below). |
| `V1_VERCEL_CLIENT_ID` / `V1_VERCEL_CLIENT_SECRET` | — | Vercel OAuth app credentials. |
| `V1_VERCEL_REDIRECT_URI` | auto-derived | Pins the Vercel OAuth callback URI. |
| `V1_CONTEXT_BUDGET` | `12000` | Token budget the context indicator and auto-compaction are computed against. |
| `V1_CONTEXT_THRESHOLD` | `0.80` | Fraction of the context budget that triggers auto-compaction. |
| `V1_SYSTEM_PROMPT` | — | Extra global system-prompt text (fallback for the Settings value). |
| `V1_CHROME_PATH` | auto-detect | Chrome/Chromium binary for the `screenshot_app` tool. |

### LLM providers

The Settings page has a **provider selector** powered by a [models.dev](https://models.dev) catalog (the same provider data opencode/pi use): pick a provider (OpenAI, OpenRouter, opencode zen, Google, Groq, DeepSeek, Mistral, xAI, Moonshot, Zhipu, Together, Fireworks, Cerebras, Ollama, LM Studio, …), and the base URL and model list fill in automatically — just paste your key and pick a model. Not in the list? Use **Browse all models.dev providers…** to search the full catalog, or **Custom endpoint** for anything else OpenAI-compatible. A refresh button pulls the latest model lists from models.dev.

Env vars work too (and act as fallbacks for UI settings). Examples for `OPENAI_BASE_URL`:

- **OpenAI**: `https://api.openai.com/v1`
- **OpenRouter**: `https://openrouter.ai/api/v1`
- **Ollama** (running on the Docker host): `http://host.docker.internal:11434/v1`

If you already use a provider through its OpenAI-compatible endpoint (the way opencode users point at OpenRouter, Together, etc.), point v1 at the same endpoint with the same key and set `V1_MODEL` accordingly.

## Auth

v1 is multi-user: each person gets their own account, settings and projects.

1. **First-run setup** (default) — the first browser to hit a fresh instance
   creates the **admin** account (username + password).
2. **`V1_PASSWORD`** — sets up the admin account (`admin` / this password)
   automatically on first start.
3. **`V1_AUTH_DISABLED=true`** — no auth at all. Only for localhost or
   networks you fully trust.

### Accounts

- The first account is the admin. Admins manage accounts from
  **Settings → Users**: create users (optionally admins) and delete accounts
  (deleting a user removes their projects).
- **Open registration** — set `V1_ALLOW_SIGNUP=true` to let anyone create an
  account from the login page (non-admin).
- **Isolation** — every user sees only their own projects, even admins.
  Projects, chat history, memories and todos are per-owner; previews,
  terminals and agent runs are scoped to the project.
- **Settings are per-user** — LLM providers and keys, GitHub/Vercel tokens,
  the global system prompt, thinking defaults and permission modes are
  personal. Anything a user hasn't set falls back to the shared/global value
  (or the environment). Instance-level configuration (OAuth app credentials,
  MCP servers, skills) is shared.
- **OIDC sign-in** auto-provisions an account on first login (keyed by the
  verified email).

### Authentik / OIDC login

Sign in through your [Authentik](https://goauthentik.io) instance (or any
other OIDC provider) as an alternative to — or instead of — the password:

1. In Authentik, create an OAuth2/OpenID Connect **Provider**, then an
   **Application** bound to it. For the provider, set the **Redirect URI** —
   the **callback URI** v1 shows you (see below); by default it is
   `https://your-v1-host/api/auth/oidc/callback` (the exact origin v1 is
   served at, plus that path).
2. Configure v1 with the minimal fields — **Issuer**, **Client ID** and
   **Client secret** — either in Settings → Auth → **OIDC login** (admin
   only, applied without a restart) or via the env vars:
   - `V1_OIDC_ISSUER` — the provider's **Issuer URL** (found on the Authentik
     provider page; shaped `https://auth.example.com/application/o/<slug>/`).
   - `V1_OIDC_CLIENT_ID` and `V1_OIDC_CLIENT_SECRET` — from the provider page.
   - `V1_OIDC_REDIRECT_URI` — optional; defaults to
     `https://<host>/api/auth/oidc/callback`, derived from
     `X-Forwarded-Proto`/`Host` (fine behind a proxy that forwards those
     headers). The Settings page shows the effective URI.
   - `V1_OIDC_ALLOWED_EMAILS` — optional comma list to restrict sign-in
     (blank = any authenticated user).
   - `V1_OIDC_ADMIN_EMAILS` — optional comma list of emails granted the
     **admin** role (case-insensitive). Applied when an account is first
     auto-provisioned, and on later sign-ins it promotes an existing matching
     user. Omit to make every OIDC user a regular (non-admin) account.
3. Restart (env vars only). The login screen shows **Sign in with Authentik**
   alongside the password field. The sign-out button still ends the local
   session.

Settings saved in the UI override the env vars; the OIDC client is rebuilt
immediately on save. An empty callback URI in the settings keeps the
per-request default.

OIDC auto-enables when `V1_AUTH_OIDC_ENABLED=true`, or when all of issuer /
client ID / client secret / redirect URI are set. When OIDC is configured the
first-run password setup is skipped, so it can be the only authentication
method. The flow uses Authorization Code + PKCE and validates the ID token
signature, issuer, audience and nonce.

> Keep the Issuer URL verbatim, including the trailing slash — Authentik
> publishes it with one (e.g. `https://auth.example.com/application/o/v1/`)
> and v1 passes it through to the provider's discovery document unchanged.

Users who sign in via OIDC have no password to change, so the **Auth**
(change password) settings are hidden from them. Admins who use OIDC keep the
Auth page so they can still reach the OIDC configuration, but the password
form is replaced with a note pointing at the identity provider.

## Reverse proxy

v1 uses WebSockets (terminal, preview dev-server HMR) and SSE (chat streaming), so the proxy must pass both.

### nginx

```nginx
server {
    listen 80;
    server_name v1.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        # WebSockets (terminal, preview dev-server HMR)
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        # SSE (chat streaming) — do not buffer
        proxy_buffering off;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Long-lived streams
        proxy_read_timeout 1h;
    }
}
```

### Caddy

```caddy
v1.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Caddy handles WebSocket upgrades and SSE flushing automatically — no extra config needed.

### Traefik (v2 / v3)

Traefik passes WebSocket upgrades and SSE responses through by default — no buffering middleware is required. It also sets `X-Forwarded-Proto` automatically, which v1 needs to decide between Secure/plain cookies and to derive the OIDC redirect URI.

Example with labels on the v1 container (docker-compose):

```yaml
services:
  v1:
    image: ghcr.io/<you>/v1:latest
    volumes:
      - v1-data:/data
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.v1.rule=Host(`v1.example.com`)"
      - "traefik.http.routers.v1.entrypoints=websecure"
      - "traefik.http.routers.v1.tls.certresolver=letsencrypt"
      - "traefik.http.services.v1.loadbalancer.server.port=8080"
```

Notes:

- The port must match `V1_PORT` (default `8080`).
- No `traefik.http.middlewares.*` entries are needed for SSE or WebSockets — Traefik neither buffers SSE nor blocks upgrade requests.
- For the Docker provider to see the labels, mount the Docker socket into Traefik with `providers.docker=true` and, if the container is on a separate network, add `traefik.docker.network=<network>`.
- v1's container healthcheck calls `wget` on `/api/healthz`; you can mirror it with `traefik.http.services.v1.loadbalancer.healthcheck.path=/api/healthz` if you want Traefik to also health-check the backend.
- If you use Authentik's forward-auth (Traefik middleware) in front of v1, note that v1 has its own login — either let v1 handle auth (skip forward-auth) or gate `/` with forward-auth and leave `/api` to v1. Do not stack both, or you'll get a double login.

## GitHub integration

Two ways to connect GitHub — either one enables **importing existing repos** and **creating + pushing new repos** for generated apps.

### OAuth (recommended)

Uses GitHub's device flow — no redirect URLs, works behind any reverse proxy:

1. Create an OAuth App at <https://github.com/settings/developers> → **New OAuth App**. Homepage and callback URLs don't matter (device flow doesn't use them), but you must enable **Device Flow** in the app's settings.
2. Paste the app's **Client ID** into Settings → GitHub in v1 (or set `V1_GITHUB_OAUTH_CLIENT_ID`). No client secret is needed or stored.
3. Click **Connect with GitHub**, enter the shown code at github.com/login/device, done. v1 requests the `repo read:user read:packages` scopes (`read:packages` is needed to list your ghcr.io container images in the GitHub tab).

### Personal access token

Paste a PAT into Settings → GitHub (or set `V1_GITHUB_TOKEN`). Required scopes:

- **Classic PAT** — the **`repo`** scope (full repository access). This covers clone, push, and creating repos.
- **Fine-grained PAT** — Repository access: your repos; Permissions: **Contents: Read & write** (Metadata: Read is automatic). Note: fine-grained tokens **cannot create new repositories** via the API, so the "create repo & push" flow needs a classic PAT or OAuth.

## Vercel integration

Deploy any v1 project to Vercel from the project page — the ⚡ button next to GitHub in the header. Two ways to connect (either enables **deploy preview** and **deploy to production** for any project):

### OAuth (recommended)

1. Create an OAuth app at <https://console.vercel.co> → **Settings → OAuth** (or <https://vercel.com/account/settings/oauth>). Add the callback URL
   `<your-origin>/api/auth/vercel/oauth/callback` (see the exact value on the Settings → Vercel page; it's derived from the request host and honors `X-Forwarded-Proto`, so it works behind a reverse proxy — set `V1_VERCEL_REDIRECT_URI` if you'd rather pin it).
2. Paste the **Client ID** and **Client Secret** into Settings → Vercel in v1 (or set `V1_VERCEL_CLIENT_ID` / `V1_VERCEL_CLIENT_SECRET`) and save.
3. Click **Connect with Vercel** and authorize. Access tokens expire after an hour; v1 refreshes them automatically with the refresh token.

### Personal access token

Paste a token from <https://vercel.com/account/tokens> into Settings → Vercel (or set `V1_VERCEL_TOKEN`). A manual token needs the **`deployment`** and **`user`** scopes.

Deploys are made from the project's current files on disk (`.git`, `node_modules`, `.vercel`, `.next`, `.cache` are excluded) — the framework is auto-detected by Vercel, exactly like v0. Direct uploads are capped at ~35 MB of files; larger projects should be pushed to GitHub and deployed from there.

## Local development

Requires Go 1.26+ and Node 22+.

```bash
make dev      # backend (:8080, auth disabled, ./data) + frontend dev server
              # …or two terminals: `make dev-backend` and `make dev-frontend`
make build    # production build → bin/v1 (frontend built and embedded)
make docker   # local docker build (tags v1:local)
```

## Contributing

Bug reports and PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for setup,
local checks, and the conventional-commit flow that drives releases. Agent
instructions are in [AGENTS.md](AGENTS.md).

## Security

v1 is a dev tool that executes AI-generated code — **the Docker container is
the isolation boundary**. Treat it as such:

- The agent's shell, terminal, and preview dev servers run with your user's
  privileges. Outside Docker there is no sandbox: a project can read anything
  your user can read.
- `npm install` / `pnpm install` run package lifecycle scripts at first
  preview. Only open projects you trust, or run v1 in the container.
- Agent file tools reject paths that escape the project directory, including
  via symlinks, and `fetch_url` refuses loopback/link-local addresses
  (server-side fetch). The remaining surface — arbitrary shell — is the
  product, not a bug.
- The bundled image runs the server as an unprivileged user against `/data`;
  mount a named volume there and keep secrets (API keys, tokens) out of
  project files.

## Releases

Fully automated, zero manual steps:

1. **Push to `main`** → the Release workflow computes and pushes the next semver tag (`mathieudutour/github-tag-action`). Conventional commits drive the bump: `feat:` → minor, `fix:` → patch, `BREAKING CHANGE` → major (default: patch).
2. **Image** — a multi-arch (`linux/amd64` + `linux/arm64`) image is built and pushed to `ghcr.io/<owner>/v1`, tagged with the new version **and** `latest`.
3. **GitHub Release** — created for the tag with auto-generated release notes.

Manual release: **Actions → Release → Run workflow** and pick the bump (`patch` / `minor` / `major`).

## Memory usage

- The Go core is light: roughly **~50 MB** RSS idle.
- Each running preview is a real Node dev server — budget a few hundred MB **per active preview**.
- `V1_MAX_PREVIEWS` (default `3`) caps how many previews run at once; the least-recently-used ones are stopped beyond the cap.
