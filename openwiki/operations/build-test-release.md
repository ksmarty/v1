---
type: "Reference"
title: "Build, test and release"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-164e2da859b5277df81c7d94
    resource: repo://.github/workflows/ci.yml
  - id: openwiki-source-6d4b4e707b8d60b6ccfa3425
    resource: repo://.github/workflows/openwiki-update.yml
  - id: openwiki-source-4d1d392666be6dfdd7a91a2e
    resource: repo://.github/workflows/release.yml
  - id: openwiki-source-8037e2358a2c4f9b2c722a11
    resource: repo://AGENTS.md
  - id: openwiki-source-f317ee207e1653d2033c81a4
    resource: repo://CONTRIBUTING.md
  - id: openwiki-source-b79fbbd921df689b4bbdc82f
    resource: repo://docker-compose.yml
  - id: openwiki-source-bb1ebe868e35e9e500714501
    resource: repo://Dockerfile
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
  - id: openwiki-source-14e56945b7c632a3b335dcec
    resource: repo://web/package.json
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Build, test and release

v1 is one Go binary with a React SPA embedded inside it and a Node sidecar
shipped beside it. That shape decides everything here: the frontend must be built
*before* `go build` runs (because of `//go:embed`), the sidecar needs
`npm ci` before v1 will start, and the release pipeline produces one
multi-arch container image rather than a set of language-specific artifacts.

Two facts explain most of the odd builder steps:

- **`internal/server/dist` is embedded at compile time.** `go build` and even
  `go vet` refuse to compile a checkout where that directory holds only
  `.gitkeep`, so CI and `CONTRIBUTING.md` both drop in a placeholder
  `index.html` (`CONTRIBUTING.md`, `.github/workflows/ci.yml:62-67`), and
  `make build` fills it with the real frontend first (`Makefile:32-37`).
- **`CGO_ENABLED=0` is not optional.** The image is built from Alpine stages and
  the memory encoder is pure Go precisely because there is no C toolchain (see
  [Memory, embeddings and sanitisation](../architecture/memory-search-and-sanitize.md)).

How the binary wires itself up at runtime is
[Runtime and startup](../architecture/runtime-and-startup.md); first-run usage is
[Quickstart](../quickstart.md).

## Local development

```bash
make dev          # backend on :8080 (auth disabled, ./data) + Vite dev server
```

`make dev` (`Makefile:10-12`) backgrounds
`V1_AUTH_DISABLED=true V1_DATA_DIR=./data go run -ldflags "$(LDFLAGS)" ./cmd/v1`
with `&` and runs `cd web && npm run dev` in the foreground, so Ctrl-C stops
both. Two terminals are better for a long session: `make dev-backend`
(`Makefile:14-15`) and `make dev-frontend` (`:17-18`).

Auth is disabled in dev so the SPA is reachable without a session; ownership
checks and the `/preview/` auth gate are skipped in that mode, which is why the
preview iframe works locally without a login (see
[HTTP API and auth](../architecture/http-api-and-auth.md)).

### Sidecar dependencies are a prerequisite

`sidecar-deps` (`Makefile:23-24`) is `cd sidecar && npm ci`. `dev`,
`dev-backend` and `build` all depend on it, and the comment on the target says
it plainly: "v1 will not start without it" — the default harness is the
pi-durable sidecar, not the built-in Go loop. `make sidecar-test`
(`Makefile:28-29`) runs `npm run check && npm test`: a syntax check over every
source file, then the bridge suite that spawns the real sidecar and speaks
JSON-RPC to it (see [Harness sidecar](../architecture/harness-sidecar.md)).

To run without it, set `V1_HARNESS=go` to select the built-in agent loop.

## Version stamping

```make
BUILD_ID := $(shell git rev-parse --short=6 HEAD)-$(shell date +%H%M%S)
LDFLAGS  := -X main.version=$(BUILD_ID) -X main.commit=$(shell git rev-parse --short HEAD)
```

(`Makefile:1-2`). A local build therefore carries a timestamped identifier such
as `abc123-143002`, which is what `/api/healthz` and the startup log line report
(`v1 <version> (<commit>) listening on :8080`) — the string
`CONTRIBUTING.md` asks for in a bug report. Release builds stamp the semver tag
instead (`Dockerfile:31-33`).

## The exact checks a change must pass

`CONTRIBUTING.md` calls these "exactly what CI runs":

```bash
go vet ./...
go test ./...
CGO_ENABLED=0 go build ./...
cd web && npm ci && npm run build      # tsc --noEmit + vite build
docker build -t v1:local .             # optional, matches the CI docker job
```

The Go commands need the placeholder `internal/server/dist/index.html` in a
fresh checkout:

```bash
mkdir -p internal/server/dist
[ -f internal/server/dist/index.html ] || \
  echo '<!doctype html><title>ci placeholder</title>' > internal/server/dist/index.html
```

`npm run build` is `tsc --noEmit && vite build` (`web/package.json:6-10`), so a
type error fails the frontend job — there is no separate `typecheck` script.

The sidecar has its own pair, which CI runs as its own job because a broken
sidecar leaves the Go tests green while every chat turn fails:

```bash
cd sidecar && npm ci
npm run check && npm test
```

### What CI gates, job by job

`.github/workflows/ci.yml` runs on `pull_request` targeting `main` with
`concurrency: cancel-in-progress: true`, in four independent jobs:

| Job | Steps | Why it exists |
| --- | --- | --- |
| `frontend` | `npm ci`, `npm run build` (cwd `web`) | the SPA compiles and typechecks |
| `sidecar` | `npm ci`, `npm run check`, `npm test` (cwd `sidecar`) | the ESM host parses and its bridge suite passes |
| `go` | placeholder dist, `go vet ./...`, `go test ./...`, `CGO_ENABLED=0 go build ./...` | the backend vets, tests and cross-compiles |
| `docker` | buildx, `platforms: linux/amd64`, `push: false`, args `VERSION=ci COMMIT=${{ github.sha }}`, `cache-from/to: type=gha` | the Dockerfile still builds |

The docker job builds only `linux/amd64` and never pushes: it is a build check,
not a publish. The gha layer cache is shared with the release workflow, so the
first release after a PR reuses the layers that PR already built.

### Toolchain versions

Go **1.26+**, Node **22+** (`CONTRIBUTING.md`). CI pins `go-version: "1.26"`
(`.github/workflows/ci.yml:57`) and `node-version: 22` (`:23`, `:41`); the
Dockerfile pins `golang:1.26-alpine` and `node:22-alpine` / `node:22-slim`. The
sidecar's `package.json` requires Node `>=22.19`.

## The container image

`Dockerfile` (172 lines) is four stages, and each split has a reason worth
keeping:

1. **`web`** — `node:22-alpine`, `npm ci` then `npm run build`, with npm and
   BuildKit caches mounted so a source change does not re-download the tree.
2. **`build`** — `golang:1.26-alpine`, `go mod download` first, then
   `COPY --from=web /app/web/dist ./internal/server/dist` **before** `go build`,
   with the comment that spells out why: the backend embeds the frontend via
   `//go:embed`, so the assets must be in place first. Compiled with
   `CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}` and
   `-ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"`, both
   stages pinned to `--platform=$BUILDPLATFORM` so cross-builds compile natively
   and only the final stage runs under QEMU.
3. **`sidecar`** — `node:22-slim`, `npm ci --omit=dev`, sources copied to
   `/usr/local/lib/v1/sidecar`. Deliberately **not** `$BUILDPLATFORM`: some
   transitive dependencies ship platform-specific binaries (esbuild), so
   installing under the build platform would produce a `node_modules` the other
   architecture cannot run.
4. **`final`** — `node:22-slim` (Debian/glibc, because semble's prebuilt wheels
   are glibc-only and Alpine/musl cannot load them). Installs, in small separate
   layers so a tweak does not invalidate the heavy ones: `git bash ca-certificates
   wget ripgrep fd-find` (with `fd` symlinked from `fdfind`), the GitHub CLI from
   its apt repo, `chromium` for the screenshot tool, and rootless podman with
   `slirp4netns`, `fuse-overlayfs`/`fuse3` and `uidmap` so the `run_container`
   tool can build containers for the user.

The runtime stage then points **every global toolchain at the persisted
`/data` volume** — `NPM_CONFIG_PREFIX=/data/npm`, `PYTHONUSERBASE=/data/python`,
`PIPX_HOME`/`PIPX_BIN_DIR`, `UV_TOOL_DIR`/`UV_TOOL_BIN_DIR`,
`CARGO_HOME=/data/cargo`, `GOPATH=/data/go`, `BUN_INSTALL=/data/bun`, with those
`bin` directories prepended to `PATH`. `/usr/local` is wiped by an image update
and `/data` is not, so the plain `npm install -g` / `pip install --user`
commands the agent already knows keep working across upgrades.

Environment: `V1_DATA_DIR=/data`, `V1_PORT=8080`,
`V1_SIDECAR_SCRIPT=/usr/local/lib/v1/sidecar/src/host.js`. `EXPOSE 8080`,
`VOLUME ["/data"]`, `USER node`, and
`HEALTHCHECK … wget -q -O /dev/null http://127.0.0.1:${V1_PORT:-8080}/api/healthz`.

### Running it

`docker-compose.yml` is documented as "convenience only — the same container runs
with plain `docker run`". It pulls `ghcr.io/<owner>/v1:latest`, publishes
`8080:8080`, mounts the `v1-data` volume at `/data`, and sets two things that are
not defaults by accident:

- `privileged: true` — the image bundles rootless podman, so docker-in-docker
  needs the *outer* container to allow nested containers. The comment offers the
  narrower alternative: `CAP_SYS_ADMIN` + user namespaces + `seccomp=unconfined`.
- `stop_grace_period: 10m` — v1 waits for running turns on SIGTERM, and Docker's
  10-second default would cut a long answer short. `docker run` takes the same
  value as `--stop-timeout 600`.

Optional environment (commented in the file): `V1_PASSWORD`, `OPENAI_API_KEY`,
`OPENAI_BASE_URL`, `V1_MODEL`, `V1_GITHUB_TOKEN`, `V1_AUTH_DISABLED`. The full
list is in the configuration reference of
[Runtime and startup](../architecture/runtime-and-startup.md).

## The release workflow

`.github/workflows/release.yml` (162 lines) triggers on **every push to `main`**
and on `workflow_dispatch` with one input:

| Input | Type | Default | Options |
| --- | --- | --- | --- |
| `bump` | `choice` | `patch` | `patch`, `minor`, `major` |

Manual runs use the chosen bump; automatic runs pass `auto` and let the commits
decide. Concurrency is `release-${{ github.ref }}` with
`cancel-in-progress: false` — "simultaneous pushes to main must not race a
release: serialize them" (`:19-21`).

### Job `tag` — compute and push the next semver

Permissions `contents: write`; `fetch-depth: 0` so the conventional commits can be
analysed. The bump is computed natively, with the comment recording why: "no
third-party action (the old one ran on the deprecated Node 20 runtime and its
token usage failed on re-runs)" (`:36-45`).

1. `LAST=$(git describe --tags --abbrev=0 … || echo v0.0.0)`, split into
   major/minor/patch.
2. No commits since `LAST` → "nothing to release", empty `tag` output, exit 0.
3. `auto` mode: `^feat!|BREAKING CHANGE` → major, `^feat` → minor, otherwise
   patch.
4. Apply the bump; if the resulting tag already exists, emit an empty output and
   exit 0.
5. Tag as `github-actions[bot]` and push, retrying three times with 10s and 20s
   sleeps — "a tag push can fail on a GitHub-side glitch — `fatal error in
   commit_refs_rules`, seen with no ruleset in play at all" (`:84-87`). Three
   failures fail the job.

### Job `release` — publish

Runs only when `needs.tag.outputs.tag != ''`. Permissions `contents: write` (the
Release) and `packages: write` (GHCR). Steps: QEMU + Buildx, log in to
`ghcr.io` with the automatic `GITHUB_TOKEN`, lowercase the repository name (GHCR
requires lowercase), then `docker/metadata-action` tags the image twice —
`type=raw,value=<tag>` and `type=raw,value=latest` — and
`docker/build-push-action` builds `linux/amd64,linux/arm64` with
`VERSION=<tag> COMMIT=${{ github.sha }}` and pushes. Finally
`softprops/action-gh-release` creates a GitHub Release for the tag with
`generate_release_notes: true`.

So a merge to `main` with a `feat:` subject publishes `vX.Y+1.0` and `latest`
for two architectures, plus a Release whose notes are generated from the merged
PRs. Nothing else is published: no binaries, no npm package, no signed artifacts.

### The OpenWiki workflow

`.github/workflows/openwiki-update.yml` is a third workflow, added by OpenWiki
itself: `workflow_dispatch` plus a daily `0 8 * * *` schedule, `fetch-depth: 0`
(so `openwiki code --update` can diff HEAD against the commit it last
documented), Node 22, `npm install --global openwiki@0.9.0` and then
`openwiki code --update --print` with `OPENWIKI_PROVIDER=openai`. It exits
non-zero without an `OPENAI_API_KEY` secret, which is the only thing it needs.

## Contributor workflow and boundaries

From `CONTRIBUTING.md`:

1. Branch from `main`; one concern per PR.
2. Conventional commit subjects, because they drive the automated bump:
   `feat:` → minor, `fix:` (or anything else) → patch, `BREAKING CHANGE:` in the
   body or `feat!` → major. The release workflow's regexes are the authority, and
   they are the reason a subject line matters.
3. CI must be green — frontend build (which typechecks), Go vet/test/build, the
   sidecar suite and the Docker build.
4. Conventions documented in `AGENTS.md` or the README (commands, layout, config
   keys) change in the **same PR** as the code.

Bug reports carry the version and commit from the startup log line, and for
agent/chat problems a diagnostics export (chat `+` menu → **Export diagnostics**,
which scrubs API keys before download).

`AGENTS.md` is the authoritative architecture document — `## Project overview`
(`:5`), `## Layout` (`:11`), `## Key commands` (`:121`), `## Conventions`
(`:135`), `## Boundaries` (`:534`) and `## OpenWiki` (`:553`). Its conventions
section is where the invariants live that the architecture pages here cite (the
authority rules for user messages, the fold rule, the appearance mirror, the
composer cap); its boundaries section is what a change should not cross.

## Invariants worth keeping

1. **The frontend is built before `go build`, never after.** `//go:embed` fails
   the build otherwise, which is why every pipeline has a placeholder or a
   `COPY --from=web`.
2. **`CGO_ENABLED=0` everywhere.** A dependency that needs cgo breaks the build
   and the image, so pure-Go alternatives are chosen even when they are more
   work.
3. **Every toolchain that installs globally points at `/data`.** Anything written
   to `/usr/local` disappears on the next image update.
4. **Version bumps come from commit subjects.** A `fix:` merged with a `feat:`
   in the same window releases a minor, so the subject is part of the release
   contract.
5. **A release never races another release.** `cancel-in-progress: false` on a
   per-ref group; a cancelled tag job would leave a tag pushed with no image.
