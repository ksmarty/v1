---
type: "Reference"
title: "Quickstart"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-8037e2358a2c4f9b2c722a11
    resource: repo://AGENTS.md
  - id: openwiki-source-8944315688730e7dac923731
    resource: repo://cmd/v1/main.go
  - id: openwiki-source-f317ee207e1653d2033c81a4
    resource: repo://CONTRIBUTING.md
  - id: openwiki-source-b79fbbd921df689b4bbdc82f
    resource: repo://docker-compose.yml
  - id: openwiki-source-bb1ebe868e35e9e500714501
    resource: repo://Dockerfile
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Quickstart

v1 is a self-hosted clone of [v0](https://v0.dev): you chat with an AI and it
builds a real web app — with a live preview, a file browser, a terminal and
GitHub integration. It ships as **one Docker container**: a Go server with the
React SPA embedded in the binary, and a Node sidecar beside it.

Two engines run chat turns, and the difference matters before you run anything:

- **the pi-durable sidecar — the default.** A Node process that owns the durable
  transcript and calls back into Go for every tool. v1 supervises it over a Unix
  socket. See [Harness sidecar](architecture/harness-sidecar.md).
- **the built-in Go loop** (`V1_HARNESS=go`). The older agent loop, still
  reachable, no longer the default.

**v1 refuses to start without the sidecar's dependencies** — the binary exits
with a message naming `make sidecar-deps` and `V1_HARNESS=go`.

## Run it

```bash
docker run -d \
  --name v1 \
  -p 8080:8080 \
  -v v1-data:/data \
  --privileged \
  -e OPENAI_API_KEY=sk-... \
  ghcr.io/<owner>/v1:latest
```

Open <http://localhost:8080>. On first run you get a setup screen and create your
login; after that the session cookie carries you (see
[HTTP API and auth](architecture/http-api-and-auth.md)). Replace `<owner>` with
the GitHub user/org that publishes the image.

`--privileged` is there because the image bundles **rootless podman** so the
agent's `run_container` tool can build containers for you. Without it the tool
fails with `no container runtime found`. The narrower alternative is
`--cap-add SYS_ADMIN --security-opt seccomp=unconfined`, and the host kernel must
allow unprivileged user namespaces.

A `docker-compose.yml` is included with `privileged: true` and a ten-minute
`stop_grace_period` (v1 waits for a running turn on SIGTERM) already set:

```bash
docker compose up -d      # edit <owner> in the image name first, or use build: .
```

Everything the app writes lives in the `/data` volume: the SQLite database, the
projects, the installed skills and extensions, and every tool the agent installs
globally (the image points npm, pip, cargo, go and bun at `/data` on purpose).
Backing up `/data` backs up the installation.

## Run it from source

Requires **Go 1.26+** and **Node 22+**.

```bash
make sidecar-deps   # cd sidecar && npm ci — v1 will not start without this
make dev            # backend on :8080 (auth disabled, ./data) + Vite dev server
```

`make dev` backgrounds the backend and runs the Vite dev server in the
foreground, so Ctrl-C stops both; `make dev-backend` and `make dev-frontend` do
the same in two terminals. Auth is disabled in this mode, so no login is needed
and the preview iframe works directly.

To produce the single binary exactly as the image does:

```bash
make build          # web build → internal/server/dist (embedded) → bin/v1
```

which builds the SPA, copies it into the embedded directory and runs
`go build`. `make docker` builds the image locally instead. Version stamping,
the CI checks and the release pipeline are in
[Build, test and release](operations/build-test-release.md).

## First steps in the UI

1. **Settings → LLM & providers** — pick a provider and paste a key. The catalog
   is models.dev data, and any OpenAI-compatible endpoint can be added without a
   code change ([LLM providers, models and plan usage](architecture/llm-providers-and-models.md)).
2. **New project** — describe the app. The first message is sent for you, and
   the preview starts as soon as the scaffold exists
   ([Preview, terminal and git](architecture/preview-terminal-git.md)).
3. **Settings → Appearance** — 17 themes, chat side, tab order and the composer
   buttons (four at a time), account-scoped so they follow you between devices
   ([The SPA architecture](frontend/spa-architecture.md)).
4. Optional: **Settings → GitHub** for repo import and push, **Skills** for
   installable instruction files, **Extensions** for tools v1 gains at runtime.

## Where to read next

| If you want to know… | Read |
| --- | --- |
| what starts, in what order, from which env var | [Runtime and startup](architecture/runtime-and-startup.md) |
| the HTTP surface, cookies, users and ownership | [HTTP API and auth](architecture/http-api-and-auth.md) |
| how a turn streams, queues and steers | [Chat turns, queue and steer](architecture/chat-turns-and-queue.md) |
| the tool surface and what the model actually receives | [Agent tools and events](architecture/agent-tools-and-events.md) |
| the sidecar process and its restart rules | [Harness sidecar](architecture/harness-sidecar.md) |
| tables, sessions and account-scoped settings | [Store, sessions and settings](architecture/store-sessions-and-settings.md) |
| providers, catalog, plan-usage rings | [LLM providers, models and plan usage](architecture/llm-providers-and-models.md) |
| extensions and skills | [Extensions and skills](architecture/extensions-and-skills.md) |
| preview servers, terminal, git and deploy | [Preview, terminal and git](architecture/preview-terminal-git.md) |
| memories, embeddings and sanitisation | [Memory, embeddings and sanitisation](architecture/memory-search-and-sanitize.md) |
| the React app | [The SPA architecture](frontend/spa-architecture.md) |
| commands, CI and releases | [Build, test and release](operations/build-test-release.md) |

`AGENTS.md` at the repository root remains the authoritative, always-current
architecture document; this wiki is generated from it and the source.
