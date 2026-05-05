# ⚡ KubeBolt

[![GitHub stars](https://img.shields.io/github/stars/clm-cloud-solutions/kubebolt?style=social)](https://github.com/clm-cloud-solutions/kubebolt)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
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
| Built-in auth & roles | | | | **Yes** |
| Gateway API support | | | | **Yes** |
| Helm chart (OCI) | | | | **Yes** |
| < 70 MB RAM | | Yes | | **Yes** |

---

### Dashboard Overview

The dashboard is split into three sub-tabs that share the same range
selector and freshness indicator but offer different lenses on the
cluster.

**Overview — at-a-glance scan.** KPIs, commitment bars, workload
health, namespace tiles, recent events. Answers "is everything fine
right now?".

![KubeBolt Dashboard — Overview](docs/images/kubebolt-dashboard.webp)

**Capacity — investigation surface.** 2×2 trend charts (CPU / Memory
/ Network / Filesystem) overlaid with deploy markers so a metric
shift can be correlated with what changed; Recent Deploys table;
cluster-wide Top Workloads · CPU; deterministic Right-sizing
Recommendations driven by 7d P95 (over-provisioned / near-limit /
no-specs).

![KubeBolt Dashboard — Capacity](docs/images/kubebolt-dashboard-capacity.webp)

**Reliability — Hubble L7 lens.** Surfaces only when Hubble HTTP
metrics are flowing into VictoriaMetrics: cluster error rate split
by 4xx / 5xx; Top Workloads · Traffic with status_class distribution;
Top Workloads · Latency with min..max range; Error Hot-spots ranked
by absolute error req/s; L4 Network Drops for NetworkPolicy
violations and connection refused. Empty when Hubble isn't installed
— no placeholder banner; the tab simply doesn't appear until L7
data exists.

![KubeBolt Dashboard — Reliability](docs/images/kubebolt-dashboard-reliability.webp)

Each panel ships with an Ask-Kobi affordance — panel-level for
summarization, plus per-row on rows where each one is its own
actionable investigation (Recent Deploys, Right-sizing, Error
Hot-spots, Network Drops).

### Cluster Topology Map
![KubeBolt Cluster Map](docs/images/kubebolt-cluster-map.webp)

### Resource Views with Live Metrics
![KubeBolt Deployments](docs/images/kubebolt-deployments.webp)

## Quick Start

### Option 1: Homebrew (macOS, Linux)

```bash
brew install clm-cloud-solutions/tap/kubebolt
kubebolt --kubeconfig ~/.kube/config
```

Automatic updates via `brew upgrade kubebolt`. Single-process — no TSDB
bundled. For historical metrics (Monitor tab, Top Consumers) run a
sibling VictoriaMetrics and set `KUBEBOLT_METRICS_STORAGE_URL`; see the
[historical-metrics note below](#historical-metrics-single-process-installs).

### Option 2: Docker (single container)

```bash
docker run -p 3000:3000 -v ~/.kube:/root/.kube:ro \
  ghcr.io/clm-cloud-solutions/kubebolt:latest
```

Single multi-arch image (amd64/arm64) with embedded frontend. Signed
with Cosign. No TSDB bundled — for historical metrics, run a sibling
VictoriaMetrics container and pass `-e KUBEBOLT_METRICS_STORAGE_URL=...`
(see the [historical-metrics note below](#historical-metrics-single-process-installs)),
or use Option 6 (Compose) which bundles VM.

### Option 3: kubectl Plugin (krew)

```bash
kubectl krew index add clm https://github.com/clm-cloud-solutions/krew-index.git
kubectl krew install clm/kubebolt
kubectl kubebolt
```

Uses your current kubectl context. Same single-process binary as the
Homebrew install — no TSDB bundled. For historical metrics see the
[note below](#historical-metrics-single-process-installs).

### Option 4: Single Binary (manual download)

Download directly from the [latest release](https://github.com/clm-cloud-solutions/kubebolt/releases/latest):

```bash
# macOS Apple Silicon
curl -LO https://github.com/clm-cloud-solutions/kubebolt/releases/latest/download/kubebolt-darwin-arm64
chmod +x kubebolt-darwin-arm64 && mv kubebolt-darwin-arm64 /usr/local/bin/kubebolt

# Linux amd64
curl -LO https://github.com/clm-cloud-solutions/kubebolt/releases/latest/download/kubebolt-linux-amd64
chmod +x kubebolt-linux-amd64 && sudo mv kubebolt-linux-amd64 /usr/local/bin/kubebolt

# Run
kubebolt --kubeconfig ~/.kube/config
```

Available for `darwin-arm64`, `darwin-amd64`, `linux-arm64`, `linux-amd64`, and `windows-amd64`. The binary includes the React frontend embedded — API + UI on a single port. A `.env` file in the current directory is auto-loaded for configuration.

#### Historical metrics — single-process installs

Options 1–4 (Homebrew, Docker single container, krew, raw binary) don't
bundle a TSDB. Live CPU/memory bars on resource lists work out of the
box from `metrics-server`, but the Monitor tab, Node Top Consumers, and
any agent-driven historical charts need a Prometheus-compatible
endpoint. Either run VictoriaMetrics next to the binary
(`docker run -d -p 8428:8428 victoriametrics/victoria-metrics`), point
an existing one via `KUBEBOLT_METRICS_STORAGE_URL=http://host:8428`, or
use the Helm chart / Docker Compose options below — both bundle
VictoriaMetrics. Without it, the Monitor tab simply stays empty;
nothing else degrades.

### Option 5: Helm Chart (recommended for Kubernetes)

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
```

The chart deploys three workloads: the API, the web frontend, and a
single-node **VictoriaMetrics** StatefulSet (10 GiB PVC, 30-day retention)
that stores metrics and Hubble flow events shipped by the agent. Bundled
by default so the install works out of the box; if your cluster already
runs VictoriaMetrics or a compatible TSDB, see the chart README for how
to disable the embedded instance and point at your own. Access via
`kubectl port-forward svc/kubebolt 3000:80` or configure an Ingress.

For custom configuration:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=kubebolt.example.com
```

### Option 6: Docker Compose

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

The compose stack also brings up a **VictoriaMetrics** container (30-day
retention, named volume `kubebolt-vm-data`) so historical metrics, Monitor
tab, and Node Top Consumers work end-to-end. To point at an existing
VictoriaMetrics / vmselect instead, set
`KUBEBOLT_METRICS_STORAGE_URL=http://host:8428` in `deploy/.env` and
remove (or stop) the bundled `victoriametrics` service. Retention can be
tuned via `KUBEBOLT_METRICS_RETENTION` (default `30d`).

To stop: `docker compose down`

To rebuild after code changes: `docker compose up -d --build`

### Option 7: Local development — single command

Requires Go 1.25+ and Node 20+.

```bash
# Runs both API and Web in a single terminal
make dev
```

API on http://localhost:8080, Web on http://localhost:5173. Press
`Ctrl+C` to stop both. For historical metrics during local dev, the
fastest path is `cd deploy && docker compose up -d victoriametrics` and
exporting `KUBEBOLT_METRICS_STORAGE_URL=http://localhost:8428` before
`make dev` — see the [single-process note above](#historical-metrics-single-process-installs).

Other useful commands:

```bash
make build        # Build API binary + frontend bundle
make build-api    # Build only the Go binary
make build-web    # Build only the frontend
make test         # Run Go tests
```

### Option 8: Local development — separate terminals

If you prefer running each service independently:

```bash
# Terminal 1 — Start the backend
cd apps/api
go run cmd/server/main.go --kubeconfig ~/.kube/config

# Terminal 2 — Start the frontend
cd apps/web
npm install && npm run dev
```

Open http://localhost:5173 — Vite proxies `/api` and `/ws` to the
backend on port 8080. Same TSDB requirement as Option 7 — see the
[single-process note above](#historical-metrics-single-process-installs)
if you need historical metrics in dev.

## Do I need the agent?

The default install (kubeconfig + KubeBolt running on your laptop, or
in-cluster Helm install) needs **no agent**. KubeBolt reads the
apiserver directly via the credentials it already has.

You only need the agent (`kubebolt-agent`) when **the apiserver of the
cluster you want to monitor is not reachable from KubeBolt's backend**
— private network, on-prem behind NAT, SaaS-style topology where
KubeBolt doesn't get to hold customer kubeconfigs. The agent runs as a
DaemonSet inside that cluster, dials OUT to the backend's gRPC port,
and exposes the apiserver through an SPDY tunnel.

Three install tiers depending on what you want the dashboard to be
able to do (full picker in
[`deploy/agent/README.md`](deploy/agent/README.md)):

| Tier | Read inventory | Mutate (exec / scale / delete) | Auth required |
|---|---|---|---|
| `metrics` | ❌ | ❌ | optional |
| `reader` | ✅ | ❌ | recommended |
| `operator` | ✅ | ✅ | **yes** |

Once an agent has connected, its cluster registration **survives
backend restarts** — the cluster selector keeps showing the agent-
proxy entry from boot, instead of going blank for the agent's
reconnect window. See `docs/architecture/sprint-a5-agent-proxy.md`
for the persistent-registry details.

Install paths — pick whichever fits your shop:

```bash
# Helm (most flexible)
helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system --create-namespace \
  --set backendUrl=YOUR_BACKEND:9090 --set rbac.mode=reader

# Raw manifest (simplest)
kubectl apply -f https://raw.githubusercontent.com/clm-cloud-solutions/kubebolt/main/deploy/agent/kubebolt-agent-reader.yaml

# UI wizard (only when KubeBolt's backend already has kubeconfig
# access to the target cluster — Administration → Integrations)
```

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

### Authentication & Access Control
- **Built-in auth** — Username/password login with JWT sessions. Enabled by default, can be disabled for open access.
- **Three roles** — **Viewer** (read-only), **Editor** (edit YAML, scale, restart, port-forward, exec), **Admin** (full access + user management)
- **Default admin** — Auto-created on first boot with configurable password (or randomly generated, printed to logs)
- **User management** — Grafana-style admin UI: create/edit/delete users, assign roles, reset passwords
- **Session security** — Access tokens in memory (not localStorage), httpOnly refresh cookies, token rotation

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
- **17 cluster tools** — Fetch resource details, logs (with grep + since), events, topology, history, kubectl describe, product docs, and more
- **Contextual "Ask Copilot"** — One-click buttons on insights, resource detail pages (Pods, Deployments, StatefulSets, Services, Nodes) and every Warning event. Each button pre-loads a prompt with the relevant context.
- **Conversation memory** — Auto-compact at 80% of the budget using the provider's cheap-tier model (Haiku 4.5 / gpt-4o-mini). Manual "new session with summary" via the Scissors button. Long sessions stay usable.
- **Multi-provider** — Anthropic Claude, OpenAI (including GPT-5 with `max_completion_tokens`), Groq, OpenRouter, Azure OpenAI, DeepSeek, Mistral, or self-hosted (Ollama, vLLM, LM Studio). Prompt caching on both Anthropic and OpenAI.
- **Fallback model** — Auto-retry with a secondary provider on rate limits or 5xx errors
- **BYO API key** — KubeBolt has no managed AI service. You bring your own provider key. Disabled by default.
- **Scope guardrail** — The system prompt keeps the assistant scoped to Kubernetes/DevOps/KubeBolt topics; out-of-scope questions get a polite refusal with a redirect.
- **Product knowledge base** — `get_kubebolt_docs` tool answers how-to questions about KubeBolt features without hallucinating.
- Setup: [docs/guides/copilot.md](docs/guides/copilot.md) · Provider reference: [docs/guides/copilot-providers.md](docs/guides/copilot-providers.md)

### Notifications
- **Slack & Discord webhooks** — Rich messages with severity colour-coding and deep-link buttons back to the resource
- **Email (SMTP)** — Instant, hourly digest, or daily digest modes. Multiple recipients.
- **Global settings** — Master toggle (maintenance windows), min severity threshold, dedup cooldown, base URL for deep links, resolved-insight alerts
- Admin page at `/admin/notifications` with test-send buttons per channel

### Admin analytics
- **Copilot Usage page** — Sessions, tokens billed, cache hit rate, estimated USD cost by provider/model, top tools with error rates, per-session drill-down with tool breakdown and compact events
- **BoltDB-backed** — 30-day / 5000-entry retention, shared with the auth database. Zero telemetry, zero SaaS.
- Available at `/admin/copilot-usage` when authentication is enabled (Admin role only)

## Authentication

KubeBolt includes built-in authentication with three roles (Admin, Editor, Viewer). Enabled by default.

| Environment Variable | Default | Description |
|---|---|---|
| `KUBEBOLT_AUTH_ENABLED` | `true` | Set `false` for open access (no login) |
| `KUBEBOLT_ADMIN_PASSWORD` | (auto-generated) | Initial admin password. Printed to logs if not set |
| `KUBEBOLT_JWT_SECRET` | (auto-generated) | JWT signing secret. Set explicitly to persist sessions across restarts |
| `KUBEBOLT_DATA_DIR` | `./data` | Directory for the embedded user database |

**Local development:**
```bash
# With auth (default) — password printed to terminal:
go run cmd/server/main.go --kubeconfig ~/.kube/config

# With a fixed password:
KUBEBOLT_ADMIN_PASSWORD=admin123456 go run cmd/server/main.go --kubeconfig ~/.kube/config

# Without auth (legacy mode):
KUBEBOLT_AUTH_ENABLED=false go run cmd/server/main.go --kubeconfig ~/.kube/config
```

**Docker Compose:** set variables in `deploy/.env` (see `deploy/.env.example`).

**Helm:** configure under `auth:` in `values.yaml`. Passwords can be managed via `existingSecret` for production.

## Architecture

```
┌─────────────────────────────────┐         ┌────────────────────────┐
│      Kubernetes Cluster(s)      │ ◄─ ──── │  kubebolt-agent        │
│   API Server + Metrics Server   │  gRPC   │  (DaemonSet, optional) │
└───────────────┬─────────────────┘         │   metrics + flows      │
                │ kubeconfig (all contexts) └────────────┬───────────┘
                │                                        │ samples
┌───────────────▼─────────────────┐                      │
│   KubeBolt Backend (Go)         │ ◄────────────────────┘
│   ├─ Auth (BoltDB + JWT)        │ ──┐
│   ├─ Permission Probe (SSAR)    │   │ writes + queries
│   ├─ Shared Informers (gated)   │   │
│   ├─ Dynamic Client (GW API)    │   ▼
│   ├─ Metrics Collector          │ ┌──────────────────────┐
│   ├─ Insights Engine (12 rules) │ │  VictoriaMetrics     │
│   ├─ Agent Channel (gRPC bidi)  │ │  (StatefulSet)       │
│   ├─ SPDY Exec Bridge           │ │  TSDB for metrics    │
│   └─ Port Forward Manager       │ │  + Hubble flows      │
└───────────────┬─────────────────┘ └──────────────────────┘
                │ REST API + WebSocket
┌───────────────▼─────────────────┐
│   KubeBolt Frontend (React)     │
│   ├─ Dashboard Overview         │
│   ├─ Cluster Map (Grid/Flow/    │
│   │    Traffic)                 │
│   ├─ 23 Resource Views          │
│   ├─ Pod Terminal (xterm.js)    │
│   ├─ Pod File Browser           │
│   ├─ Port Forward UI            │
│   └─ Kobi (AI Copilot)          │
└─────────────────────────────────┘
```

The bundled VictoriaMetrics is optional — for clusters that already run
a TSDB it can be replaced via `metrics.storage.externalUrl`. The agent
is also optional — KubeBolt reads the apiserver directly via kubeconfig
when the backend can reach it; the agent is for clusters where the
apiserver isn't reachable from the backend (private network, SaaS-style
multi-cluster) or when you want kubelet-level metrics without metrics-server.

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
| Frontend bundle | ~2.1 MB JS + 64 KB CSS (~605 KB gzipped) |
| API response time | < 5ms (from informer cache) |
| Startup time | < 5s (permission probe + informer sync) |
| VictoriaMetrics RAM | ~256 MB request, scales with cardinality |
| TSDB disk | ~1 GiB / 100 pods at 30-day retention (default) |

## Roadmap

See [docs/SPEC.md](docs/SPEC.md) for the detailed technical specification and roadmap.

**Coming next:**
- Install script (`curl get.kubebolt.dev | sh`) and Kubernetes Operator (pending custom domain setup)
- OAuth2/OIDC authentication (GitHub, Google, Azure AD)
- Teams and organizations
- Hierarchical AI agents (detectors → router → investigator → planner → executor → postmortem) with per-incident economy via tiered model selection
- JSON-aware truncation in tool results (structure-aware rather than byte-aligned)

## License

Apache License 2.0 — see [LICENSE](LICENSE) for the full text and [NOTICE](NOTICE) for attribution.
