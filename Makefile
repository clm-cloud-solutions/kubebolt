.PHONY: dev dev-api dev-web install build build-api build-web test clean

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

# ─── Setup ────────────────────────────────────────────────────

## Install frontend dependencies
install:
	@if [ ! -d apps/web/node_modules ]; then \
		echo "Installing frontend dependencies..."; \
		cd apps/web && npm install; \
	fi

# ─── Build ────────────────────────────────────────────────────

## Build everything
build: build-api build-web

## Build the Go API binary
build-api:
	cd apps/api && go build -o kubebolt cmd/server/main.go

## Build the frontend for production
build-web:
	cd apps/web && npm run build

# ─── Test ─────────────────────────────────────────────────────

## Run all tests
test:
	cd apps/api && go test ./...

# ─── Clean ────────────────────────────────────────────────────

## Remove build artifacts
clean:
	rm -f apps/api/kubebolt
	rm -rf apps/web/dist
