# syntax=docker/dockerfile:1

# ---------- Stage 1: build the React frontend ----------
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /app/web

# Install dependencies first for better layer caching.
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci

COPY web/ ./
RUN --mount=type=cache,target=/root/.npm npm run build

# ---------- Stage 2: build the Go backend (cross-compiled) ----------
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# The backend embeds the frontend via //go:embed at internal/server/dist,
# so the built assets must be in place BEFORE `go build`.
COPY --from=web /app/web/dist ./internal/server/dist

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/v1 ./cmd/v1

# ---------- Stage 2b: the pi-durable harness sidecar ----------
# Deliberately NOT $BUILDPLATFORM: some transitive dependencies ship
# platform-specific binaries (esbuild), so installing under the build platform
# would produce a node_modules that cannot run on the other architecture. The
# base image matches the runtime stage, so glibc and Node versions agree.
#
# The sidecar is plain ESM — there is no build step, so this only has to
# install dependencies and copy the sources.
FROM node:22-slim AS sidecar
WORKDIR /sidecar
COPY sidecar/package.json sidecar/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --omit=dev
COPY sidecar/src ./src

# ---------- Stage 3: runtime ----------
# The container runs generated user apps, so it needs node + npm + pnpm,
# plus git and bash. Only these apk/corepack steps run under QEMU when
# cross-building; both build stages above run natively on $BUILDPLATFORM.
#
# Rootless podman is installed so the v1 agent's run_container tool can
# build and run containers for the user. Rootless podman needs:
#   slirp4netns    -> rootless networking
#   fuse-overlayfs -> rootless storage driver (with fuse3 for fusermount3)
#   uidmap         -> newuidmap/newgidmap setuid helpers for user-namespace
#                     mapping of the subuid/subgid ranges (a setuid-root
#                     binary cannot be installed later by an unprivileged
#                     user, so it must ship in the image)
# plus a subuid/subgid range for the `node` user (see /etc/subuid below).
#
# Docker-in-docker: the OUTER container that runs v1 must allow nested
# containers, e.g. start it privileged (or with CAP_SYS_ADMIN, seccomp
# unconfined and user namespaces enabled on the host kernel). See
# docker-compose.yml.
FROM node:22-slim AS final

# Debian (glibc) base: semble's binary wheels (semble-grammars .so) are
# built for glibc only — Alpine/musl cannot run it.
# Split into small, rarely-changing layers so a tweak to one package (or the
# healthcheck needing wget) doesn't invalidate the heavy podman/chromium
# layers. Do NOT cache-mount apt's dirs here: /var/lib/apt/lists holds apt's
# lockfile, and the two platform builds (amd64 + arm64) run concurrently and
# fight over it. The gha layer cache already reuses unchanged layers.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git bash ca-certificates wget ripgrep fd-find \
    && rm -rf /var/lib/apt/lists/* \
    && ln -s "$(command -v fdfind)" /usr/local/bin/fd

# GitHub CLI, so the agent can work with issues, pull requests and releases
# instead of hand-rolling API calls. It is authenticated per command from
# V1_GITHUB_TOKEN (see internal/agent/env.go) — the token is never baked into
# the image, and only a command that actually invokes gh is given it.
RUN apt-get update && apt-get install -y --no-install-recommends gnupg \
    && mkdir -p -m 755 /etc/apt/keyrings \
    && wget -qO /etc/apt/keyrings/githubcli-archive-keyring.gpg \
        https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# Chromium for the screenshot tool (large, changes rarely).
RUN apt-get update && apt-get install -y --no-install-recommends chromium \
    && rm -rf /var/lib/apt/lists/*

# Rootless podman so the v1 agent's run_container tool can build and run
# containers. Rootless podman needs: slirp4netns (rootless networking),
# fuse-overlayfs (rootless storage driver), and a subuid/subgid range for
# the `node` user (see /etc/subuid below).
#
# Docker-in-docker: the OUTER container that runs v1 must allow nested
# containers, e.g. start it privileged (or with CAP_SYS_ADMIN, seccomp
# unconfined and user namespaces enabled on the host kernel). See
# docker-compose.yml.
RUN apt-get update && apt-get install -y --no-install-recommends \
    podman slirp4netns fuse-overlayfs uidmap python3 python3-pip \
    && rm -rf /var/lib/apt/lists/* \
    && echo "node:100000:65536" > /etc/subuid \
    && echo "node:100000:65536" > /etc/subgid

# pnpm for generated apps; pinned major so corepack doesn't re-resolve
# @latest and churn this layer. The npm cache mount keeps the tarball.
RUN --mount=type=cache,target=/root/.npm \
    corepack enable && corepack prepare pnpm@9 --activate

# semble: semantic code search over the workspace (cached pip downloads).
RUN --mount=type=cache,target=/root/.cache/pip \
    python3 -m pip install --no-cache-dir --break-system-packages semble==0.5.5

# The `node` user (uid/gid 1000) gets a subordinate id range so rootless
# podman can map container uids; without /etc/subuid + /etc/subgid entries
# podman refuses to start containers.

# Pre-create the persistent install directories so a FRESH volume inherits them
# (Docker initialises an empty named volume from the image's directory). An
# existing volume is covered by the persistent-tool-install builtin skill,
# which mkdir -p's the same paths before installing.
RUN mkdir -p /data/npm/bin /data/python/bin /data/cargo/bin /data/go/bin /data/bun/bin /data/bin \
    && chown -R node:node /data

COPY --from=build /out/v1 /usr/local/bin/v1

# The pi-durable chat harness (V1_HARNESS=pi). Installed outside /usr/local/bin
# so the binary directory stays binaries-only; V1_SIDECAR_SCRIPT points at it.
COPY --from=sidecar /sidecar /usr/local/lib/v1/sidecar

ENV V1_DATA_DIR=/data \
    V1_PORT=8080 \
    V1_SIDECAR_SCRIPT=/usr/local/lib/v1/sidecar/src/host.js

# Tools the agent installs must survive container recreation, so every toolchain
# that installs globally is pointed at the persisted /data volume — /usr/local
# is wiped by an image update, /data is not. NPM_CONFIG_PREFIX, PYTHONUSERBASE,
# CARGO_HOME, GOPATH, BUN_INSTALL, PIPX_* and UV_* redirect each toolchain
# without touching HOME, so the plain `npm install -g` / `pip install --user`
# commands the agent already knows land in the right place. The bin directories
# are prepended to the base image's PATH, with /data/bin as the shared drop-in
# for standalone binaries. See the persistent-tool-install builtin skill.
ENV NPM_CONFIG_PREFIX=/data/npm \
    PYTHONUSERBASE=/data/python \
    PIPX_HOME=/data/pipx \
    PIPX_BIN_DIR=/data/bin \
    UV_TOOL_DIR=/data/uv/tools \
    UV_TOOL_BIN_DIR=/data/bin \
    CARGO_HOME=/data/cargo \
    GOPATH=/data/go \
    BUN_INSTALL=/data/bun \
    PATH=/data/bin:/data/npm/bin:/data/python/bin:/data/cargo/bin:/data/go/bin:/data/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

EXPOSE 8080
VOLUME ["/data"]

USER node

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${V1_PORT:-8080}/api/healthz" || exit 1

ENTRYPOINT ["/usr/local/bin/v1"]
