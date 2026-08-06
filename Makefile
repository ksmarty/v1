VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: dev dev-backend dev-frontend build docker

# Dev mode: backend on :8080 (auth disabled, data in ./data) + Vite dev server.
# Prefer two terminals — `make dev-backend` and `make dev-frontend` —
# or use `make dev`, which backgrounds the backend with `&` and runs the
# frontend dev server in the foreground (Ctrl-C stops both).
dev:
	@V1_AUTH_DISABLED=true V1_DATA_DIR=./data go run ./cmd/v1 & \
	cd web && npm run dev

dev-backend:
	V1_AUTH_DISABLED=true V1_DATA_DIR=./data go run ./cmd/v1

dev-frontend:
	cd web && npm run dev

# Production build: frontend -> internal/server/dist (embedded) -> bin/v1
build:
	cd web && npm ci && npm run build
	mkdir -p internal/server/dist
	cp -R web/dist/. internal/server/dist/
	go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(shell git rev-parse --short HEAD)" -o bin/v1 ./cmd/v1

# Local docker build (multi-arch + push is handled by the release workflow)
docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(shell git rev-parse --short HEAD) -t v1:local .
