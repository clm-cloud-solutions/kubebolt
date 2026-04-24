.PHONY: dev dev-api dev-web agent-image agent-deploy agent-logs agent-dev agent-undeploy install build build-api build-web build-binary build-all test clean kind-testbed kind-testbed-down kind-testbed-ingress kind-metrics-server

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

# Timestamped tag ensures every build produces a unique image reference,
# so kubelet never sees a cached :dev from a previous session.
AGENT_TAG ?= dev-$(shell date +%s)

## Build the agent container image locally. Tags with both a timestamped
## :dev-N reference (so each build is unique and forces a fresh pull by
## the node's container runtime) and the sliding :dev pointer.
agent-image:
	docker build -f packages/agent/Dockerfile \
		-t kubebolt-agent:$(AGENT_TAG) \
		-t kubebolt-agent:dev .
	@echo "Built kubebolt-agent:$(AGENT_TAG)"

## Apply the dev DaemonSet manifest pinned to the most recent dev-*
## timestamp tag in the local Docker image store. Auto-detects kind and
## minikube contexts and loads the image into the cluster's runtime
## (their containerd is separate from the host Docker daemon, so
## imagePullPolicy: Never fails unless we stage the image explicitly).
agent-deploy:
	@LATEST_TAG=$$(docker images kubebolt-agent --format '{{.Tag}}' | grep -E '^dev-[0-9]+$$' | sort -rn -t- -k2 | head -1); \
		if [ -z "$$LATEST_TAG" ]; then \
			echo "No kubebolt-agent:dev-N image found. Run 'make agent-image' first."; \
			exit 1; \
		fi; \
		CTX=$$(kubectl config current-context 2>/dev/null); \
		case "$$CTX" in \
			kind-*) \
				KIND_NAME="$${CTX#kind-}"; \
				echo "kind context detected ($$KIND_NAME) — loading image into nodes..."; \
				kind load docker-image kubebolt-agent:$$LATEST_TAG --name $$KIND_NAME || exit 1; \
				;; \
			minikube) \
				echo "minikube context detected — loading image..."; \
				minikube image load kubebolt-agent:$$LATEST_TAG || exit 1; \
				;; \
			docker-desktop) \
				: ;; \
			*) \
				echo "Context '$$CTX' — assuming image is reachable (real cluster with registry, etc.)"; \
				;; \
		esac; \
		echo "Deploying kubebolt-agent:$$LATEST_TAG"; \
		sed "s|image: kubebolt-agent:dev$$|image: kubebolt-agent:$$LATEST_TAG|" \
			deploy/agent/kubebolt-agent-dev.yaml | kubectl apply -f -
	@kubectl rollout status ds/kubebolt-agent -n kubebolt-system --timeout=90s

## Follow logs from all agent pods.
agent-logs:
	kubectl logs -n kubebolt-system -l app=kubebolt-agent -f --tail=50

## Inner loop: build image, deploy, follow logs.
agent-dev: agent-image agent-deploy agent-logs

## Tear down the dev DaemonSet (keeps the namespace).
agent-undeploy:
	kubectl delete -f deploy/agent/kubebolt-agent-dev.yaml --ignore-not-found

# ─── Kind testbed ──────────────────────────────────────────────────────────
#
# For iterating on Monitor charts with real data. Installs metrics-server
# (so the Metrics Server donut fallback keeps working) and a small workload
# that generates continuous CPU / memory / network traffic.
#
# Not docker-desktop specific, but the patches below assume kind's
# self-signed kubelet certs.

## Install metrics-server with --kubelet-insecure-tls so it works on kind.
kind-metrics-server:
	kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
	@# The upstream manifest ships with strict TLS verification against kubelet;
	@# kind's kubelet uses a self-signed cert, so patch the flag in.
	kubectl patch deployment metrics-server -n kube-system --type='json' \
		-p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]' || true
	kubectl rollout status deployment/metrics-server -n kube-system --timeout=120s

## Install the demo workload (nginx + loadgen + redis StatefulSet).
kind-testbed: kind-metrics-server
	kubectl apply -f deploy/test/demo-workload.yaml
	kubectl rollout status deployment/demo-web -n demo --timeout=90s
	kubectl rollout status deployment/demo-load -n demo --timeout=90s
	kubectl rollout status statefulset/demo-cache -n demo --timeout=120s
	@echo ""
	@echo "Testbed up. Open the KubeBolt UI and check:"
	@echo "  - Deployment 'demo-web' in namespace 'demo' (3 nginx replicas)"
	@echo "  - StatefulSet 'demo-cache' in namespace 'demo' (2 redis replicas)"
	@echo "  - Any pod of demo-web for per-pod charts"

## Remove the demo workload (keeps metrics-server).
kind-testbed-down:
	kubectl delete -f deploy/test/demo-workload.yaml --ignore-not-found

## Install ingress-nginx + add Ingress routing to demo-web so external
## HTTP can be simulated from the host. demo-workload.yaml already has
## the Ingress resource and a CiliumNetworkPolicy that turns on L7
## visibility for the ingress-nginx pod — so once traffic flows, the
## cluster map's Traffic mode shows status codes on the Ingress → Pod
## hop too. The controller install is from the official cloud
## manifest, which works in kind without hostNetwork tricks.
##
## After `make kind-testbed-ingress`, start a port-forward and curl:
##   kubectl -n ingress-nginx port-forward svc/ingress-nginx-controller 8080:80 &
##   curl -H 'Host: demo.localhost' http://localhost:8080/
##   curl -H 'Host: demo.localhost' http://localhost:8080/err500
kind-testbed-ingress: kind-testbed
	kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.2/deploy/static/provider/cloud/deploy.yaml
	kubectl -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=120s
	kubectl apply -f deploy/test/demo-workload.yaml
	@echo ""
	@echo "Ingress ready. Port-forward to test from the host:"
	@echo "  kubectl -n ingress-nginx port-forward svc/ingress-nginx-controller 8080:80"
	@echo "  curl -H 'Host: demo.localhost' http://localhost:8080/"

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
