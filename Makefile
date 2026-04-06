.PHONY: dev dev-api dev-web install build build-api build-web test clean

# ─── Development ──────────────────────────────────────────────

## Run both API and Web in a single terminal
dev: install
	@echo "Starting KubeBolt..."
	@echo "  API  → http://localhost:8080"
	@echo "  Web  → http://localhost:5173"
	@echo ""
	@trap 'kill 0' EXIT; \
		cd apps/api && go run cmd/server/main.go --kubeconfig ~/.kube/config & \
		cd apps/web && npx vite --host & \
		wait

## Run only the API
dev-api:
	cd apps/api && go run cmd/server/main.go --kubeconfig ~/.kube/config

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
