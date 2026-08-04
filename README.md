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
| `V1_MAX_PREVIEWS` | `3` | Max preview dev servers running at once. |

### LLM providers

The Settings page has a **provider selector** powered by a [models.dev](https://models.dev) catalog (the same provider data opencode/pi use): pick a provider (OpenAI, OpenRouter, opencode zen, Google, Groq, DeepSeek, Mistral, xAI, Moonshot, Zhipu, Together, Fireworks, Cerebras, Ollama, LM Studio, …), and the base URL and model list fill in automatically — just paste your key and pick a model. "Custom endpoint" covers anything else OpenAI-compatible, and a refresh button pulls the latest model lists from models.dev.

Env vars work too (and act as fallbacks for UI settings). Examples for `OPENAI_BASE_URL`:

- **OpenAI**: `https://api.openai.com/v1`
- **OpenRouter**: `https://openrouter.ai/api/v1`
- **Ollama** (running on the Docker host): `http://host.docker.internal:11434/v1`

If you already use a provider through its OpenAI-compatible endpoint (the way opencode users point at OpenRouter, Together, etc.), point v1 at the same endpoint with the same key and set `V1_MODEL` accordingly.

## Auth

Three ways to handle authentication:

1. **First-run setup** (default) — the first browser to hit a fresh instance sets the admin password.
2. **`V1_PASSWORD`** — set a fixed password via env; the login screen asks for it.
3. **`V1_AUTH_DISABLED=true`** — no auth at all. Only for localhost or networks you fully trust.

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

## GitHub integration

Two ways to connect GitHub — either one enables **importing existing repos** and **creating + pushing new repos** for generated apps.

### OAuth (recommended)

Uses GitHub's device flow — no redirect URLs, works behind any reverse proxy:

1. Create an OAuth App at <https://github.com/settings/developers> → **New OAuth App**. Homepage and callback URLs don't matter (device flow doesn't use them), but you must enable **Device Flow** in the app's settings.
2. Paste the app's **Client ID** into Settings → GitHub in v1 (or set `V1_GITHUB_OAUTH_CLIENT_ID`). No client secret is needed or stored.
3. Click **Connect with GitHub**, enter the shown code at github.com/login/device, done. v1 requests the `repo read:user` scopes.

### Personal access token

Paste a PAT into Settings → GitHub (or set `V1_GITHUB_TOKEN`). Required scopes:

- **Classic PAT** — the **`repo`** scope (full repository access). This covers clone, push, and creating repos.
- **Fine-grained PAT** — Repository access: your repos; Permissions: **Contents: Read & write** (Metadata: Read is automatic). Note: fine-grained tokens **cannot create new repositories** via the API, so the "create repo & push" flow needs a classic PAT or OAuth.

## Local development

Requires Go 1.23+ and Node 22+.

```bash
make dev      # backend (:8080, auth disabled, ./data) + frontend dev server
              # …or two terminals: `make dev-backend` and `make dev-frontend`
make build    # production build → bin/v1 (frontend built and embedded)
make docker   # local docker build (tags v1:local)
```

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
