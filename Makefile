BUILD_ID := $(shell git rev-parse --short=6 HEAD)-$(shell date +%H%M%S)
LDFLAGS := -X main.version=$(BUILD_ID) -X main.commit=$(shell git rev-parse --short HEAD)

.PHONY: dev dev-backend dev-frontend build docker sidecar-deps sidecar-test

# Dev mode: backend on :8080 (auth disabled, data in ./data) + Vite dev server.
# Prefer two terminals — `make dev-backend` and `make dev-frontend` —
# or use `make dev`, which backgrounds the backend with `&` and runs the
# frontend dev server in the foreground (Ctrl-C stops both).
dev: sidecar-deps
	@V1_AUTH_DISABLED=true V1_DATA_DIR=./data go run -ldflags "$(LDFLAGS)" ./cmd/v1 & \
	cd web && npm run dev

dev-backend: sidecar-deps
	V1_AUTH_DISABLED=true V1_DATA_DIR=./data go run -ldflags "$(LDFLAGS)" ./cmd/v1

dev-frontend:
	cd web && npm run dev

# Dependencies for the pi-durable chat harness sidecar (the default harness).
# Plain ESM — no build step — so this is only needed to run it or its tests.
# `dev`, `dev-backend` and `build` depend on it: v1 will not start without it.
sidecar-deps:
	cd sidecar && npm ci

# Syntax check + the bridge test suite (spawns the real sidecar and speaks the
# JSON-RPC protocol to it).
sidecar-test: sidecar-deps
	cd sidecar && npm run check && npm test

# Production build: frontend -> internal/server/dist (embedded) -> bin/v1
build: sidecar-deps
	cd web && npm ci && npm run build
	mkdir -p internal/server/dist
	cp -R web/dist/. internal/server/dist/
	go build -ldflags "$(LDFLAGS)" -o bin/v1 ./cmd/v1

# Local docker build (multi-arch + push is handled by the release workflow)
docker:
	docker build --build-arg VERSION=$(BUILD_ID) --build-arg COMMIT=$(shell git rev-parse --short HEAD) -t v1:local .
