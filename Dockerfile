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
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
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
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/v1 ./cmd/v1

# ---------- Stage 3: runtime ----------
# The container runs generated user apps, so it needs node + npm + pnpm,
# plus git and bash. Only these apk/corepack steps run under QEMU when
# cross-building; both build stages above run natively on $BUILDPLATFORM.
FROM node:22-alpine AS final

RUN apk add --no-cache git bash ca-certificates \
    && corepack enable \
    && corepack prepare pnpm@latest --activate

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
