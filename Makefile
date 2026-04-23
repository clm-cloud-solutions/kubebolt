.PHONY: dev dev-api dev-web agent-image agent-deploy agent-logs agent-dev agent-undeploy install build build-api build-web build-binary build-all test clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# ─── Development ──────────────────────────────────────────────

## Run both API and Web in a single terminal
## Auto-loads .env if present (for KUBEBOLT_AI_* and other dev env vars).
## .env is sourced via POSIX shell, so quoted values and comments work correctly.
dev: install
	@echo "Starting KubeBolt..."
	@echo "  API  → http://localhost:8080"
	@echo "  Web  → http://localhost:5173"
	@if [ -f .env ]; then echo "  Loading .env..."; fi
	@echo ""
	@trap 'kill 0' EXIT; \
		( \
			set -a; \
			[ -f .env ] && . ./.env; \
			set +a; \
			cd apps/api && go run cmd/server/main.go --kubeconfig ~/.kube/config \
		) & \
		cd apps/web && npx vite --host & \
		wait

## Run only the API
dev-api:
	@( \
		set -a; \
		[ -f .env ] && . ./.env; \
		set +a; \
		cd apps/api && go run cmd/server/main.go --kubeconfig ~/.kube/config \
	)

## Run only the Web
dev-web:
	cd apps/web && npx vite --host

# ─── Agent (runs as DaemonSet inside Kubernetes) ────────────────────────────
#
# Unlike dev/dev-api/dev-web which run on the host, the agent is designed to
# live inside the cluster. These targets build the image locally, deploy it
# as a DaemonSet on whatever context kubectl currently points at, and tail
# logs. Assumes docker-desktop Kubernetes (or any local cluster with access
# to the host's Docker daemon image cache).
#
# Prerequisite: `make dev` (or `make dev-api`) must be running on the host so
# the agent has a backend to reach at host.docker.internal:9090.

## Build the agent container image locally (tag: kubebolt-agent:dev).
agent-image:
	docker build -f packages/agent/Dockerfile -t kubebolt-agent:dev .

## Apply the dev DaemonSet manifest and force a rollout to pick up the
## latest locally-built image (imagePullPolicy: Never means K8s reuses
## whatever :dev tag is already in the local Docker daemon).
agent-deploy:
	kubectl apply -f deploy/agent/kubebolt-agent-dev.yaml
	@kubectl rollout restart ds/kubebolt-agent -n kubebolt-system 2>/dev/null || true
	@kubectl rollout status ds/kubebolt-agent -n kubebolt-system --timeout=60s

## Follow logs from all agent pods.
agent-logs:
	kubectl logs -n kubebolt-system -l app=kubebolt-agent -f --tail=50

## Inner loop: build image, deploy, follow logs.
agent-dev: agent-image agent-deploy agent-logs

## Tear down the dev DaemonSet (keeps the namespace).
agent-undeploy:
	kubectl delete -f deploy/agent/kubebolt-agent-dev.yaml --ignore-not-found

# ─── Setup ────────────────────────────────────────────────────

## Install frontend dependencies
install:
	@if [ ! -d apps/web/node_modules ]; then \
		echo "Installing frontend dependencies..."; \
		cd apps/web && npm install; \
	fi

# ─── Build ────────────────────────────────────────────────────

## Build everything (separate API binary + frontend bundle)
build: build-api build-web

## Build the Go API binary (API-only, no embedded frontend)
build-api:
	cd apps/api && go build -o kubebolt cmd/server/main.go

## Build the frontend for production
build-web:
	cd apps/web && npm run build

## Build single binary with embedded frontend for current platform
build-binary: install build-web
	@echo "Embedding frontend into Go binary..."
	@find apps/api/cmd/server/web/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
	@mkdir -p apps/api/cmd/server/web/dist
	@cp -r apps/web/dist/. apps/api/cmd/server/web/dist/
	cd apps/api && CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o kubebolt ./cmd/server/
	@echo ""
	@echo "Binary built: apps/api/kubebolt ($$(du -h apps/api/kubebolt | cut -f1))"
	@echo "Run: ./apps/api/kubebolt --kubeconfig ~/.kube/config"

## Build binaries for all platforms (5 targets) into dist/
## Produces: kubebolt-linux-amd64, kubebolt-linux-arm64, kubebolt-darwin-amd64,
##           kubebolt-darwin-arm64, kubebolt-windows-amd64.exe + CHECKSUMS.txt
build-all: install build-web
	@echo "Embedding frontend and cross-compiling for 5 platforms..."
	@find apps/api/cmd/server/web/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
	@mkdir -p apps/api/cmd/server/web/dist dist
	@cp -r apps/web/dist/. apps/api/cmd/server/web/dist/
	@rm -f dist/kubebolt-*
	@for target in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64; do \
		GOOS=$${target%-*}; \
		GOARCH=$${target#*-}; \
		EXT=""; \
		[ "$$GOOS" = "windows" ] && EXT=".exe"; \
		OUT="dist/kubebolt-$${target}$${EXT}"; \
		echo "  → $$OUT"; \
		cd apps/api && CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH \
			go build -ldflags="-s -w -X main.version=$(VERSION)" \
			-o ../../$$OUT ./cmd/server/ && cd ../..; \
	done
	@echo ""
	@echo "Generating checksums..."
	@cd dist && shasum -a 256 kubebolt-* > CHECKSUMS.txt
	@echo ""
	@ls -lh dist/

# ─── Test ─────────────────────────────────────────────────────

## Run all tests
test:
	cd apps/api && go test ./...

# ─── Clean ────────────────────────────────────────────────────

## Remove build artifacts
clean:
	rm -f apps/api/kubebolt
	rm -rf apps/web/dist
	rm -rf apps/api/cmd/server/web/dist
	rm -rf dist/
