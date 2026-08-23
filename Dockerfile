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

# ---------- Stage 3: runtime ----------
# The container runs generated user apps, so it needs node + npm + pnpm,
# plus git and bash. Only these apk/corepack steps run under QEMU when
# cross-building; both build stages above run natively on $BUILDPLATFORM.
#
# Rootless podman is installed so the v1 agent's run_container tool can
# build and run containers for the user. Rootless podman needs:
#   slirp4netns    -> rootless networking
#   fuse-overlayfs -> rootless storage driver (with fuse3 for fusermount3)
#   shadow         -> newuidmap/newgidmap for user-namespace mapping
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
# layers, and cache apt/pip downloads across rebuilds.
RUN --mount=type=cache,target=/var/cache/apt \
    --mount=type=cache,target=/var/lib/apt/lists \
    apt-get update && apt-get install -y --no-install-recommends \
        git bash ca-certificates wget ripgrep fd-find \
    && rm -rf /var/lib/apt/lists/* \
    && ln -s "$(command -v fdfind)" /usr/local/bin/fd

# Chromium for the screenshot tool (large, changes rarely).
RUN --mount=type=cache,target=/var/cache/apt \
    --mount=type=cache,target=/var/lib/apt/lists \
    apt-get update && apt-get install -y --no-install-recommends chromium \
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
RUN --mount=type=cache,target=/var/cache/apt \
    --mount=type=cache,target=/var/lib/apt/lists \
    apt-get update && apt-get install -y --no-install-recommends \
        podman slirp4netns fuse-overlayfs python3 python3-pip \
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

# Run as the non-root `node` user (uid 1000, shipped with the base image).
RUN mkdir -p /data && chown node:node /data

COPY --from=build /out/v1 /usr/local/bin/v1

ENV V1_DATA_DIR=/data \
    V1_PORT=8080

EXPOSE 8080
VOLUME ["/data"]

USER node

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${V1_PORT:-8080}/api/healthz" || exit 1

ENTRYPOINT ["/usr/local/bin/v1"]
