# ⚡ KubeBolt

[![GitHub stars](https://img.shields.io/github/stars/clm-cloud-solutions/kubebolt?style=social)](https://github.com/clm-cloud-solutions/kubebolt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=white)](https://react.dev)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-client--go-326CE5?logo=kubernetes&logoColor=white)](https://kubernetes.io)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/kubebolt)](https://artifacthub.io/packages/search?repo=kubebolt)

**Instant Kubernetes Monitoring & Management**

Full cluster visibility in under 2 minutes. No agents, no configuration, no Prometheus required. Connect your kubeconfig and go.

### Why KubeBolt?

| | kubectl | k9s | Lens | KubeBolt |
|---|:---:|:---:|:---:|:---:|
| Web-based UI | | | | **Yes** |
| Zero install | | | | **Yes** |
| Multi-cluster | | | Yes | **Yes** |
| RBAC-aware degradation | | | | **Yes** |
| Pod terminal | | Yes | Yes | **Yes** |
| Pod file browser | | | | **Yes** |
| Port forwarding | | Yes | Yes | **Yes** |
| YAML editing & apply | Yes | Yes | Yes | **Yes** |
| kubectl describe | Yes | Yes | | **Yes** |
| Restart / Scale / Delete | | Yes | Yes | **Yes** |
| Global search (Cmd+K) | | Yes | Yes | **Yes** |
| Cluster topology map | | | | **Yes** |
| Insights engine | | | | **Yes** |
| AI Copilot (BYO key) | | | | **Yes** |
| Gateway API support | | | | **Yes** |
| Helm chart (OCI) | | | | **Yes** |
| < 70 MB RAM | | Yes | | **Yes** |

---

### Dashboard Overview
![KubeBolt Dashboard](docs/images/kubebolt-dashboard.webp)

### Cluster Topology Map
![KubeBolt Cluster Map](docs/images/kubebolt-cluster-map.webp)

### Resource Views with Live Metrics
![KubeBolt Deployments](docs/images/kubebolt-deployments.webp)

## Quick Start

### Option 1: Helm Chart (recommended for Kubernetes)

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
```

KubeBolt will be deployed with a ServiceAccount that has read access to your cluster. Access via `kubectl port-forward svc/kubebolt 3000:80` or configure an Ingress.

For custom configuration:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=kubebolt.example.com
```

### Option 2: Docker Compose

Runs the full stack (Go API + React frontend via nginx) in containers.

**Prerequisites:** Docker Desktop with a reachable Kubernetes cluster.

#### Remote clusters (EKS, GKE, AKS, etc.)

If your kubeconfig points to remote cluster endpoints, it works directly:

```bash
# Set your kubectl context to the desired cluster
kubectl config use-context my-cluster

# Start the stack
cd deploy
docker compose up -d
```

> **EKS note:** The API container needs the AWS CLI to obtain tokens. The Dockerfile already includes `aws-cli`, and the compose file mounts `~/.aws` for credentials. Make sure your AWS profile/SSO session is active before starting.

#### Docker Desktop Kubernetes

Docker Desktop's built-in K8s uses `127.0.0.1:6443` as the API server address, which doesn't work from inside a container. A helper script rewrites the kubeconfig to use `kubernetes.docker.internal` instead:

```bash
# 1. Enable Kubernetes in Docker Desktop (Settings > Kubernetes > Enable)
# 2. Switch to the docker-desktop context
kubectl config use-context docker-desktop

# 3. Generate a container-compatible kubeconfig
./deploy/docker-kubeconfig.sh

# 4. Start the stack
cd deploy
docker compose up -d
```

Open http://localhost:3000 — the nginx frontend proxies API and WebSocket requests to the backend.

To stop: `docker compose down`

To rebuild after code changes: `docker compose up -d --build`

### Option 3: Local development — single command

Requires Go 1.25+ and Node 20+.

```bash
# Runs both API and Web in a single terminal
make dev
```

API on http://localhost:8080, Web on http://localhost:5173. Press `Ctrl+C` to stop both.

Other useful commands:

```bash
make build        # Build API binary + frontend bundle
make build-api    # Build only the Go binary
make build-web    # Build only the frontend
make test         # Run Go tests
```

### Option 4: Local development — separate terminals

If you prefer running each service independently:

```bash
# Terminal 1 — Start the backend
cd apps/api
go run cmd/server/main.go --kubeconfig ~/.kube/config

# Terminal 2 — Start the frontend
cd apps/web
npm install && npm run dev
```

Open http://localhost:5173 — Vite proxies `/api` and `/ws` to the backend on port 8080.

## Features

### Monitoring & Observability
- **23 resource views** — Pods, Deployments, Services, Ingresses, Gateways, HTTPRoutes, Nodes, and more
- **Cluster Map** — Interactive topology with Grid and Flow layouts, namespace grouping, resource type filters
- **Live metrics** — CPU/Memory usage bars with request/limit markers and hover tooltips
- **Insights Engine** — 12 built-in rules: crash loops, OOM kills, CPU throttling, HPA saturation, pending PVCs
- **Real-time updates** — WebSocket-powered live updates via K8s shared informers
- **Configurable refresh** — Choose refresh interval from 5s to 2m, persisted across sessions

### Cluster Management
- **Pod Terminal** — Interactive shell access from the browser (xterm.js + SPDY exec). Auto-detects bash/sh. Multi-container support. Workload pod selector for Deployments/StatefulSets/DaemonSets.
- **Pod File Browser** — Browse directories, view file contents, and download files from any container. Works with distroless images via `find` fallback.
- **Port Forwarding** — Forward pod ports with one click. Active forwards shown in Topbar with Open/Stop controls.
- **Restart & Scale** — Rollout restart for Deployments/StatefulSets/DaemonSets. Scale replicas for Deployments/StatefulSets. Confirmation popovers.
- **YAML Editing** — CodeMirror 6 editor with YAML syntax highlighting. Edit and apply changes directly from the browser.
- **Export/Copy YAML** — Copy to clipboard or download as `.yaml` file.
- **Delete Resources** — Confirmation modal with name-to-confirm input, force delete option, and cascade control.
- **kubectl describe** — Full `kubectl describe` output in a modal with syntax highlighting.
- **Workload History** — Deployment revision history via ReplicaSets. StatefulSet/DaemonSet history via ControllerRevisions.
- **CronJob Jobs** — Child job listing with status, completions, duration, and age.
- **Multi-cluster** — All kubeconfig contexts auto-discovered, switch clusters in one click with connection overlay

### Security & RBAC
- **RBAC-aware** — Auto-detects permissions at connect time via SelfSubjectAccessReview
- **Namespace-scoped SAs** — Works with RoleBinding-only ServiceAccounts using per-namespace informers
- **Sensitive data redaction** — Secret values always redacted. ConfigMap values with sensitive keys (passwords, tokens, API keys) auto-redacted in YAML view.
- **Graceful degradation** — Restricted resources dimmed in sidebar, "Access Restricted" pages, "No access" indicators on dashboard cards

### Developer Experience
- **Global Search (Cmd+K)** — Search across all resource types. Results grouped by kind with icons. Keyboard navigation.
- **Gateway API** — Native support for `gateway.networking.k8s.io` Gateways and HTTPRoutes
- **YAML viewer** — Syntax highlighted with theme-aware colors, works in light and dark mode
- **Search & filter** — Debounced search across resources with namespace filtering
- **Dark/Light mode** — Full theme support with CSS custom properties

### AI Copilot (Optional)
- **In-app chat (Cmd+J)** — Ask questions about your cluster, troubleshoot issues, explain insights
- **16 cluster tools** — The copilot can fetch resource details, logs, events, topology, history, and more
- **Multi-provider** — Works with Anthropic Claude, OpenAI, Groq, OpenRouter, Azure OpenAI, DeepSeek, Mistral, or self-hosted models (Ollama, vLLM, LM Studio)
- **Fallback model** — Auto-retry with a secondary provider on rate limits or 5xx errors
- **BYO API key** — KubeBolt has no managed AI service. You bring your own provider key. Disabled by default.
- Setup: [docs/guides/copilot.md](docs/guides/copilot.md) · Provider reference: [docs/guides/copilot-providers.md](docs/guides/copilot-providers.md)

## Architecture

```
┌─────────────────────────────────┐
│      Kubernetes Cluster(s)      │
│   API Server + Metrics Server   │
└───────────────┬─────────────────┘
                │ kubeconfig (all contexts)
┌───────────────▼─────────────────┐
│   KubeBolt Backend (Go)         │
│   ├─ Permission Probe (SSAR)    │
│   ├─ Shared Informers (gated)   │
│   ├─ Dynamic Client (GW API)    │
│   ├─ Metrics Collector          │
│   ├─ Insights Engine (12 rules) │
│   ├─ SPDY Exec Bridge           │
│   └─ Port Forward Manager       │
└───────────────┬─────────────────┘
                │ REST API + WebSocket
┌───────────────▼─────────────────┐
│   KubeBolt Frontend (React)     │
│   ├─ Dashboard Overview         │
│   ├─ Cluster Map (Grid/Flow)    │
│   ├─ 23 Resource Views          │
│   ├─ Pod Terminal (xterm.js)    │
│   ├─ Pod File Browser           │
│   └─ Port Forward UI            │
└─────────────────────────────────┘
```

## RBAC & Permissions

KubeBolt works with any level of Kubernetes access — from full cluster-admin to namespace-scoped read-only ServiceAccounts.

| Access Level | What You See |
|---|---|
| **Cluster-admin** | Everything — all resources, metrics, insights, terminal, port-forward |
| **Cluster read-only** (ClusterRoleBinding `view`) | All namespace resources, no Secrets/RBAC. Restricted items dimmed in sidebar |
| **Namespace-scoped** (RoleBindings in specific namespaces) | Only resources in permitted namespaces. Metrics polled per-namespace |

At connection time, KubeBolt probes permissions via `SelfSubjectAccessReview` and adapts:
- Informers only start for accessible resources (no 403 errors in logs)
- Namespace-scoped SAs get per-namespace informer factories with merged results
- UI shows a "Limited access" banner, dims restricted sidebar items, and displays clear "Access Restricted" pages

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Backend | Go 1.25+ with client-go, Chi v5, gorilla/websocket |
| K8s Client | Shared informers (typed) + dynamic client (Gateway API CRDs) |
| Terminal | SPDY exec bridge + xterm.js |
| YAML Editor | CodeMirror 6 with One Dark theme + YAML language |
| Frontend | React 18 + TypeScript + Vite 5 + Tailwind CSS 3.4 |
| Cluster Map | React Flow 11 with custom nodes, edges, namespace group nodes |
| Data Fetching | TanStack Query 5 + TanStack Table 8 |
| Icons | Lucide React |

## Performance

| Metric | Value |
|--------|-------|
| Backend RAM | ~70 MB (production cluster) |
| Frontend bundle | ~1.2 MB JS + 40 KB CSS (~347 KB gzipped) |
| API response time | < 5ms (from informer cache) |
| Startup time | < 5s (permission probe + informer sync) |

## Roadmap

See [docs/SPEC.md](docs/SPEC.md) for the detailed technical specification and roadmap.

**Coming next:**
- Animated cluster map with traffic visualization
- Cluster management UI (add/remove/rename clusters)
- Notifications (Slack, email) for insights alerts
- Lightweight node agent for network/disk metrics
- Live traffic visualization on cluster map

## License

MIT License — see [LICENSE](LICENSE) for details.
