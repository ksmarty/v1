# Contributing

Thanks for contributing to v1. This guide covers setup, local checks, and the
PR process. Agent-specific repo instructions live in [AGENTS.md](AGENTS.md).

## Setup

Requires Go 1.26+ and Node 22+.

```bash
make dev      # backend (:8080, auth disabled, ./data) + Vite dev server
              # or two terminals: `make dev-backend` / `make dev-frontend`
```

## Local checks

Run these before opening a PR — they mirror CI exactly:

```bash
go vet ./...
go test ./...
CGO_ENABLED=0 go build ./...   # needs internal/server/dist (see below)
cd web && npm ci && npm run build
docker build -t v1:local .     # optional, matches the CI docker job
```

`go build`/`go vet` embed `internal/server/dist` via `//go:embed`. In a fresh
checkout that directory only has `.gitkeep`; CI drops in a placeholder:

```bash
mkdir -p internal/server/dist
[ -f internal/server/dist/index.html ] || \
  echo '<!doctype html><title>ci placeholder</title>' > internal/server/dist/index.html
```

(`make build` fills it with the real frontend instead.)

## Pull requests

1. Branch from `main`. Keep changes scoped — one concern per PR.
2. Use [conventional commits](https://www.conventionalcommits.org): they drive
   the automated release bump on merge (`feat:` → minor, `fix:` → patch,
   `BREAKING CHANGE:` in the body → major; anything else defaults to patch).
3. CI (`.github/workflows/ci.yml`) must be green: frontend build
   (`npm run build`, which typechecks via `tsc --noEmit`), Go vet/test/build,
   and a `linux/amd64` Docker build.
4. If you change conventions documented in [AGENTS.md](AGENTS.md) or the
   [README](README.md) (commands, layout, config keys), update the docs in the
   same PR.

## Reporting issues

Use the issue templates (bug report / feature request). Include the version
and commit from the server log line (`v1 <version> (<commit>) listening on
:8080`) and, for chat/agent problems, a diagnostics export (chat `+` menu →
**Export diagnostics** — it scrubs API keys).
