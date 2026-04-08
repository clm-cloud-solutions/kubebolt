# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

KubeBolt is an instant Kubernetes monitoring platform — full cluster visibility in under 2 minutes with zero configuration. Go backend + React frontend monorepo. Supports multi-cluster switching and Gateway API resources.

## Build & Run Commands

### Backend (Go)
```bash
cd apps/api && go run cmd/server/main.go --kubeconfig ~/.kube/config  # Run dev server (port 8080)
cd apps/api && go build ./...                                          # Build
cd apps/api && go test ./...                                           # Run tests
cd apps/api && go test ./internal/insights/...                         # Run single package tests
```

### Frontend (React)
```bash
cd apps/web && npm install    # Install dependencies
cd apps/web && npm run dev    # Vite dev server (port 5173)
cd apps/web && npm run build  # Production build (TypeScript check + Vite)
```

### Docker Compose (full stack)
```bash
# Remote clusters (EKS, GKE, AKS) — works directly:
kubectl config use-context my-cluster
cd deploy && docker compose up -d

# Docker Desktop K8s — needs kubeconfig rewrite (127.0.0.1 → kubernetes.docker.internal):
kubectl config use-context docker-desktop
./deploy/docker-kubeconfig.sh   # generates /tmp/docker-kubeconfig
cd deploy && docker compose up -d

# Rebuild after code changes:
docker compose -f deploy/docker-compose.yml up -d --build
```
Frontend on http://localhost:3000 (nginx proxies /api and /ws to backend).
EKS clusters require `~/.aws` mounted (already in compose) with an active AWS session.

## Architecture

### Go Workspace Monorepo

Uses `go.work` with three modules:
- `apps/api` — main backend server
- `packages/agent` — Phase 2 lightweight node agent (stub)
- `packages/shared` — shared Go utilities

### Backend (`apps/api`)

Entry point: `cmd/server/main.go` (flags: `--kubeconfig`, `--port`)

Key packages under `internal/`:
- **cluster/manager.go** — Multi-cluster manager: reads all kubeconfig contexts, handles cluster switching, manages connector/collector/engine lifecycle per cluster. Initial connection is **async** — HTTP server binds immediately; manager starts in disconnected state if the default cluster is unreachable. `ConnError()` exposes the last connection error.
- **cluster/connector.go** — Kubernetes client-go shared informers for all resource types + dynamic client for Gateway API (Gateways, HTTPRoutes). `Start()` returns an error if `WaitForCacheSync` does not complete within 20s. `rest.Config.Timeout = 15s` prevents hanging on mid-session cluster failures. Informers are **gated by permissions** — only started for resources the connected SA can access. For namespace-scoped SAs, creates per-namespace `SharedInformerFactory` instances instead of a single cluster-wide factory.
- **cluster/permissions.go** — RBAC permission probing via `SelfSubjectAccessReview`. Probes 22 resource types at connection time (list verb only, ~2-5s). Two-phase probe: cluster-wide first, then namespace-level fallback for RoleBinding-based access. `PermissionDeniedError` type for 403 responses. `ResourcePermissions` map tracks `CanList`/`CanWatch`/`CanGet` per resource, plus `NamespaceScoped` flag and `Namespaces` list for namespace-scoped SAs.
- **cluster/nslister.go** — Multi-namespace lister wrappers that aggregate results from per-namespace informer factories. Implements all client-go lister interfaces (`PodLister`, `DeploymentLister`, etc.) with `List()` merging across factories and `Get()` trying each factory until found. Required for namespace-scoped ServiceAccounts.
- **cluster/graph.go** — In-memory topology graph with debounced rebuild (2s)
- **cluster/relationships.go** — Edge detection: ownerRefs, selectors, Gateway parentRefs, volumes. All lister calls nil-guarded for partial-permission scenarios.
- **metrics/collector.go** — Polls Metrics Server API (`metrics.k8s.io/v1beta1`) every 30s with synchronous initial poll. In-memory cache, no DB. Supports **per-namespace polling** when cluster-wide metrics access is denied (namespace-scoped SAs). Distinguishes 403 Forbidden from "metrics server not installed" via `apierrors.IsForbidden()`.
- **insights/engine.go** — 12 rule-based insight evaluations (crash-loop, OOM, CPU throttle, memory pressure, etc.)
- **websocket/hub.go** — WebSocket connection management (4096 buffer, silent drops when no clients)
- **api/router.go** — Chi router with `requireConnector` middleware guarding all cluster-dependent routes; `/clusters` and `/clusters/switch` are always available even when disconnected.
- **api/handlers.go** — REST handlers including resource detail with metrics injection, YAML endpoint (dynamic client), pod logs streaming, deployment/statefulset/daemonset/job pod listing, deployment history. Permission-denied errors mapped to HTTP 403 (was generic 404/500). YAML apply via PUT endpoint. New `getPermissions` handler.
- **api/exec.go** — WebSocket-to-SPDY exec bridge for pod terminal. Auto-detects shell (bash → sh). Handles permission errors, session lifecycle, terminal resize.
- **api/portforward.go** — PortForwardManager for pod port forwarding via SPDY. TCP listener on backend host with reverse proxy fallback. Start/Stop/List/StopAll with auto-cleanup on cluster switch.
- **api/actions.go** — Resource actions: restart (rollout restart via annotation patch), scale (scale subresource), delete (dynamic client with cascade/force options).
- **api/describe.go** — kubectl describe output via `k8s.io/kubectl/pkg/describe.DescriberFor()`. Supports all resource types.
- **api/search.go** — Global search across 16 resource types using existing listers. Returns results with name, namespace, kind, status.
- **api/files.go** — Pod file browser via exec-based `ls`/`find`/`cat` commands. List directories, view file content (1MB limit), download files. Handles distroless containers and permission denied gracefully.
- **models/types.go** — All domain types: `ClusterOverview` (with counts for 15 resource types + `Permissions` map), `ResourceUsage`, `ResourceList` (with `Forbidden` flag), `Insight`, `TopologyNode/Edge`, `ClusterInfoResponse`

### API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /clusters` | List all kubeconfig contexts |
| `POST /clusters/switch` | Switch active cluster |
| `GET /cluster/overview` | Cluster summary with resource counts, CPU/Memory, health |
| `GET /resources/:type` | List resources with pagination, filtering, metrics |
| `GET /resources/:type/:ns/:name` | Resource detail with metrics injection |
| `GET /resources/:type/:ns/:name/yaml` | Raw YAML via dynamic client (secrets redacted, managedFields stripped) |
| `PUT /resources/:type/:ns/:name/yaml` | Apply edited YAML via dynamic client Update() |
| `GET /resources/:type/:ns/:name/describe` | kubectl describe output via k8s.io/kubectl |
| `POST /resources/:type/:ns/:name/restart` | Rollout restart (Deployments, StatefulSets, DaemonSets) |
| `POST /resources/:type/:ns/:name/scale` | Scale replicas (Deployments, StatefulSets) |
| `DELETE /resources/:type/:ns/:name` | Delete resource with `?force=true&orphan=true` options |
| `GET /resources/pods/:ns/:name/logs` | Pod logs with `?container=&tailLines=` params |
| `GET /resources/pods/:ns/:name/files` | List directory contents via exec (`?container=&path=/`) |
| `GET /resources/pods/:ns/:name/files/content` | Read file content via exec (`?container=&path=`) |
| `GET /resources/pods/:ns/:name/files/download` | Download file as attachment |
| `GET /resources/cronjobs/:ns/:name/jobs` | List child Jobs of a CronJob |
| `GET /resources/:type/:ns/:name/history` | Workload revision history (ControllerRevisions for SS/DS) |
| `GET /resources/deployments/:ns/:name/pods` | Pods owned by deployment (via ReplicaSets) |
| `GET /resources/deployments/:ns/:name/history` | Revision history via ReplicaSets |
| `GET /resources/statefulsets/:ns/:name/pods` | Pods owned by statefulset |
| `GET /resources/daemonsets/:ns/:name/pods` | Pods owned by daemonset |
| `GET /resources/jobs/:ns/:name/pods` | Pods owned by job |
| `GET /search` | Global search across 16 resource types by name |
| `GET /topology` | Full topology graph (nodes + edges) |
| `GET /insights` | Evaluated insights with severity |
| `GET /events` | Events with `?involvedKind=&involvedName=` filtering |
| `GET /cluster/permissions` | Probed RBAC permissions per resource type |
| `POST /portforward` | Start port-forward to pod port |
| `GET /portforward` | List active port-forwards |
| `DELETE /portforward/:id` | Stop port-forward |
| `GET /ws` | WebSocket for real-time resource updates |
| `GET /ws/exec/:ns/:name` | WebSocket for pod terminal (SPDY exec bridge) |

### Frontend (`apps/web`)

React 18 + TypeScript + Vite + Tailwind CSS

Key libraries: TanStack Query (server state), TanStack Table, ReactFlow (cluster topology map), Lucide React (icons), React Router, xterm.js (pod terminal), CodeMirror 6 (YAML editor)

23 resource list views + Cluster Map + resource detail views with tabbed interface.

Component organization: `src/components/{dashboard,map,resources,layout,shared,insights}/`
API client: `src/services/api.ts`
Type definitions: `src/types/kubernetes.ts`
Theme: `src/contexts/ThemeContext.tsx` — light/dark mode via CSS custom properties (`--kb-*` variables in `globals.css`). `darkMode: 'class'` in Tailwind; all color tokens point to CSS vars. Theme persisted in `localStorage`.

### Resource Detail Views (`ResourceDetailPage.tsx`)

Tabbed detail page at `/:type/:namespace/:name`. Uses `_` as namespace placeholder for cluster-scoped resources.

**Tabs per resource type:**

| Resource | Overview | YAML | Pods | Logs | Containers | Volumes | Related | History | Events | Monitor |
|----------|----------|------|------|------|------------|---------|---------|---------|--------|---------|
| Pods | ✅ | ✅ | — | ✅ | ✅ | ✅ | ✅ | — | ✅ | ✅ |
| Deployments | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | ✅ | ✅ | ✅ |
| StatefulSets | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | — | ✅ | ✅ |
| DaemonSets | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | — | ✅ | ✅ |
| Jobs | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | — | ✅ | — |
| CronJobs | ✅ | ✅ | — | — | — | — | ✅ | — | ✅ | — |
| Services | ✅ | ✅ | — | — | — | — | ✅ | — | ✅ | — |
| Nodes | ✅ | ✅ | — | — | — | — | — | — | ✅ | ✅ |
| Others | ✅ | ✅ | — | — | — | — | — | — | ✅ | — |

Terminal and Files tabs are Phase 2 (marked "Coming Soon").

**Key features:**
- YAML viewer with theme-aware syntax highlighting (CSS variables for light/dark) + CodeMirror 6 editor mode with YAML language + One Dark theme
- kubectl describe modal with syntax highlighting (keys, values, events colored by severity)
- Log viewer with syntax coloring (green default, blue timestamps, red errors, yellow warnings)
- Workload logs: pod selector + container selector + tail lines (100/500/1000) + 10s auto-refresh
- Pod terminal: xterm.js with SPDY exec bridge, auto shell detection (bash → sh), multi-container, workload pod selector
- Port forwarding: per-port buttons in pod detail, Topbar indicator with active forwards dropdown
- Resource actions: Restart (rollout restart), Scale (replica input popover), Delete (confirmation modal with name typing)
- Global search: Cmd+K modal, debounced, grouped by kind with icons, keyboard navigation
- CPU/Memory bars with request/limit markers and hover tooltip (ResourceUsageCell component)
- Related tab uses topology API edges for parent+child navigation
- Monitor tab: SVG donut gauges from Metrics Server (Network/Disk require agent)
- Cross-resource links: Pod→Node, PVC→PV/StorageClass, HPA→target, namespace links
- Configurable refresh interval (5s–2m) persisted in localStorage, selector in DataFreshnessIndicator

**Key frontend behaviors:**
- TanStack Query `retry` skips retries on 503 (cluster unavailable) and 403 (permission denied)
- `ApiError` (from `api.ts`) used to detect 503/403 vs other errors
- Resource list pages support server-side pagination (50/page) with prev/next controls, debounced search with `keepPreviousData`
- Cluster switcher uses optimistic updates, shows "Connecting to cluster" overlay during switch, navigates to Overview on success
- Sidebar shows resource counters from overview API (15 resource types); restricted resources dimmed with shield icon
- "Limited access" banner when permissions are partial (shows X of Y resource types)
- `PermissionDenied` component for 403 pages (instead of generic error)
- Summary cards show "No access" for restricted resources; CPU/Memory panels show "No access to Nodes" when capacity unavailable
- Overview workload cards link to resource detail views
- WebSocket broadcast invalidation debounced (2s) to prevent request storms
- Sensitive value redaction: Secrets always redacted, ConfigMap values with sensitive keys auto-redacted in YAML view

### Data Flow

1. Cluster Manager reads kubeconfig contexts; initial K8s connection starts async in background
2. **Permission probe** runs 22 SelfSubjectAccessReview calls (cluster-wide, then namespace fallback) to detect access level
3. HTTP server is immediately available — returns 503 on cluster-dependent routes until connected
4. Shared informers start **only for permitted resources**; namespace-scoped SAs get per-namespace factories with multi-lister aggregation
5. Dynamic client discovers Gateway API resources (with 5s timeout)
6. Metrics Collector polls Metrics Server → in-memory metrics cache (per-namespace polling for namespace-scoped SAs)
7. Insights Engine evaluates 12 rules against cluster state → recommendations
8. REST API serves enriched resource lists (with CPU/MEM metrics injected), paginated (default 50/page). Returns 403 for restricted resources.
9. WebSocket hub broadcasts resource changes (debounced topology rebuilds)
10. Frontend uses TanStack Query with 30s refetch intervals; 503s shown as "Cluster unreachable", 403s shown as "Access Restricted"

### Cluster Map

Two layout modes:
- **Grid**: compact grid of resources within namespace regions
- **Flow**: horizontal dependency chain (Ingress/Gateway → HTTPRoute → Service → Deployment → ReplicaSet → Pod)

In both modes, namespace regions are arranged in a grid of up to 3 columns (`NS_COLS`). Namespace regions are ReactFlow group nodes with child resource nodes. Supports filtering by resource type and namespace.

## CI

GitHub Actions (`.github/workflows/ci.yml`) on push/PR to `main`:
- Backend: `go build ./...` (Go 1.22, ubuntu-latest)
- Frontend: `npm ci && npm run build` (Node 20, ubuntu-latest)

## Key Reference

`docs/SPEC.md` contains the detailed technical specification including API endpoints, insights rules, data models, and Phase 2 roadmap. Consult it for feature work.
