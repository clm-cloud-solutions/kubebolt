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

### Local stack with empty kubeconfig
```bash
make dev-clean       # API + Web on host with /tmp/kb-empty-kubeconfig.yaml (no contexts)
make dev-api-clean   # API only with empty kubeconfig
```
Use these when testing the persistent-registry boot-restore path or
the no-clusters / waiting-for-agent empty-state UX without touching
your real `~/.kube/config`. The empty kubeconfig is regenerated on
every invocation so accidental edits don't persist.

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

### Helm Chart
```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
kubectl port-forward svc/kubebolt 3000:80
```
When deployed via Helm, the API auto-detects in-cluster config (ServiceAccount token). The web container uses `API_BACKEND` env var (set by Helm template) to point nginx at the correct API service name.

## Architecture

### Go Workspace Monorepo

Uses `go.work` with three modules:
- `apps/api` — main backend server
- `packages/agent` — DaemonSet node agent. Ships kubelet stats (`metrics/collector.go`) and Hubble flow events (`internal/flows/`) over the gRPC AgentChannel into the backend's VictoriaMetrics ingest. **Aggregator gotcha:** `internal/flows/aggregator.go` filters pod-to-pod flows whose direction != `EGRESS` to dedupe forwarded traffic (each forwarded packet appears twice — egress on source node, ingress on destination), but bypasses that filter when `verdict == "dropped"` because Cilium emits drops with `TRAFFIC_DIRECTION_UNKNOWN` and they appear exactly once at the denial point. Without the bypass, every drop in clusters with active NetworkPolicies is silently swallowed and `pod_flow_events_total{verdict="dropped"}` never reaches VM. **Live-aggregator handle for self-metrics:** `internal/flows/collector.go` keeps a package-level `atomic.Pointer[Aggregator]` (`ActiveAggregator()`) populated by `RunCollector` on the leader pod and cleared on return. `internal/self/collector.go` reads it via the `AggregatorSizer` interface to emit `kubebolt_agent_aggregator_keys{type="flows|http_reqs|http_lat|externals|dns"}` gauges — non-leader pods emit nothing because the pointer is nil. **Memory observability surface:** the same `self/collector.go` emits 12 deep `runtime.MemStats` gauges (`heap_alloc_bytes`, `heap_sys_bytes`, `heap_inuse_bytes`, `heap_idle_bytes`, `heap_released_bytes`, `total_sys_bytes`, `stack_sys_bytes`, `mspan_sys_bytes`, `mcache_sys_bytes`, `other_sys_bytes`, `gc_num_total`, `next_gc_bytes`) so operators can attribute a `container_memory_working_set_bytes` gap to the right runtime layer (Go heap retention vs off-Go mappings). **Live pprof endpoint** opt-in via `KUBEBOLT_AGENT_PPROF_ADDR` env var (default empty = off); recommended value `127.0.0.1:6060` is loopback-bound and only reachable via `kubectl port-forward`. The agent chart's default `extraEnv` sets `GOMEMLIMIT=100MiB` to drive the Go scavenger when allocation churn from Hubble flow parsing makes `HeapSys` ratchet up.
- `packages/shared` — shared Go utilities

### Backend (`apps/api`)

Entry point: `cmd/server/main.go` (flags: `--kubeconfig`, `--port`)

Key packages under `internal/`:
- **cluster/manager.go** — Multi-cluster manager: reads all kubeconfig contexts, handles cluster switching, manages connector/collector/engine lifecycle per cluster. Initial connection is **async** — HTTP server binds immediately; manager starts in disconnected state if the default cluster is unreachable. `ConnError()` exposes the last connection error. **In-cluster support:** when no kubeconfig file is found, auto-detects ServiceAccount token via `rest.InClusterConfig()` and creates a single "in-cluster" context. **Agent-proxy resilience:** `connectToContextLocked` fast-fails (sub-millisecond, no 20s `WaitForCacheSync` wait) when the target context is agent-proxy and `agentRegistry.CountByCluster()==0`. `AddAgentProxyCluster` spawns a goroutine that re-runs the connect on every fresh agent registration when the active context's connector is currently failed; on success, broadcasts `cluster:connected` so the frontend invalidates `['clusters']` + `['cluster-overview']` immediately instead of waiting for the 30s refetch tick.
- **cluster/connector.go** — Kubernetes client-go shared informers for all resource types + dynamic client for Gateway API (Gateways, HTTPRoutes). `Start()` returns an error if `WaitForCacheSync` does not complete within 20s. `rest.Config.Timeout = 15s` prevents hanging on mid-session cluster failures. Informers are **gated by permissions** — only started for resources the connected SA can access. For namespace-scoped SAs, creates per-namespace `SharedInformerFactory` instances instead of a single cluster-wide factory.
- **cluster/permissions.go** — RBAC permission probing via `SelfSubjectAccessReview`. Probes 24 resource types at connection time (list verb only, ~2-5s). Two-phase probe: cluster-wide first, then namespace-level fallback for RoleBinding-based access. `PermissionDeniedError` type for 403 responses. `ResourcePermissions` map tracks `CanList`/`CanWatch`/`CanGet` per resource, plus `NamespaceScoped` flag and `Namespaces` list for namespace-scoped SAs.
- **cluster/nslister.go** — Multi-namespace lister wrappers that aggregate results from per-namespace informer factories. Implements all client-go lister interfaces (`PodLister`, `DeploymentLister`, etc.) with `List()` merging across factories and `Get()` trying each factory until found. Required for namespace-scoped ServiceAccounts.
- **cluster/graph.go** — In-memory topology graph with debounced rebuild (2s)
- **cluster/relationships.go** — Edge detection: ownerRefs, selectors, Gateway parentRefs, volumes. All lister calls nil-guarded for partial-permission scenarios.
- **metrics/collector.go** — Polls Metrics Server API (`metrics.k8s.io/v1beta1`) every 30s with synchronous initial poll. In-memory cache, no DB. Supports **per-namespace polling** when cluster-wide metrics access is denied (namespace-scoped SAs). Distinguishes 403 Forbidden from "metrics server not installed" via `apierrors.IsForbidden()`.
- **insights/engine.go** — 15 rule-based insight evaluations (crash-loop, OOM, CPU throttle, memory pressure, NetworkPolicy coverage, etc.)
- **websocket/hub.go** — WebSocket connection management (4096 buffer, silent drops when no clients). Event types in `websocket/events.go`: `resource:updated`, `resource:deleted`, `event:new`, `insight:new`, `insight:resolved`, `metrics:refresh`, `cluster:connected` (fired when an agent-proxy connector recovers).
- **api/router.go** — Chi router with `requireConnector` middleware guarding all cluster-dependent routes; `/clusters`, `/clusters/switch`, `/integrations`, `/integrations/{id}` (catalog read-only) and the `/admin/*` administration endpoints are always available even when disconnected. Install / configure / uninstall on integrations stay inside `requireConnector` (admin role + cluster needed).
- **api/handlers.go** — REST handlers including resource detail with metrics injection, YAML endpoint (dynamic client), pod logs streaming, deployment/statefulset/daemonset/job pod listing, deployment history. Permission-denied errors mapped to HTTP 403 (was generic 404/500). YAML apply via PUT endpoint. New `getPermissions` handler.
- **api/exec.go** — WebSocket-to-SPDY exec bridge for pod terminal. Auto-detects shell (bash → sh). Handles permission errors, session lifecycle, terminal resize.
- **api/portforward.go** — PortForwardManager for pod port forwarding via SPDY. TCP listener on backend host with reverse proxy fallback. Start/Stop/List/StopAll with auto-cleanup on cluster switch.
- **api/actions.go** — Resource actions: restart (rollout restart via annotation patch — workloads only), scale (scale subresource), delete (dynamic client with cascade/force options). `handleRestart` dispatches to `restartPod` in `api/actions_pod.go` when `type=="pods"`.
- **api/actions_pod.go** — Pod-only actions surfaced in Pod detail toolbar: `restartPod` (synthesizes a restart by deleting the pod — owning controller recreates it; distinct audit label `restart_pod` vs `restart_workload`) and `handleEvictPod` (graceful removal via `policy/v1.Eviction` API — respects PodDisruptionBudgets; frontend renders 429+`pdbBlocked:true` payloads as a dedicated "Blocked by PodDisruptionBudget" modal instead of a generic rate-limit error).
- **api/describe.go** — kubectl describe output via `k8s.io/kubectl/pkg/describe.DescriberFor()`. Supports all resource types.
- **api/search.go** — Global search across 16 resource types using existing listers. Returns results with name, namespace, kind, status.
- **api/files.go** — Pod file browser via exec-based `ls`/`find`/`cat` commands. List directories, view file content (1MB limit), download files. Handles distroless containers and permission denied gracefully.
- **api/copilot.go** — AI Copilot chat handler with multi-step tool calling loop. SSE streaming. Auto-fallback to secondary provider on recoverable errors (429, 5xx, network). Reads `KUBEBOLT_AI_*` env vars via `config.LoadCopilotConfig()`.
- **copilot/** — Copilot package: provider interface, Anthropic + OpenAI adapters, tool executor (server-side, calls existing connector methods), system prompt builder, tool definitions. BYO key model — no KubeBolt-managed AI service.
- **auth/store.go** — User store backed by BoltDB (`go.etcd.io/bbolt`, pure Go, no CGO). Schema: `users`, `refresh_tokens`, `tenants`, `clusters`, `cluster_display`, `copilot_sessions`, `agents` (persistent agent records — see `agent/channel/registry_store.go`), and `username_index`/`tenant_*_index` indices. CRUD for users, refresh token rotation, admin seed on first boot. Bcrypt cost 12 for password hashing. Buckets created at boot via `NewStore`; missing buckets are added automatically on upgrade so the schema can evolve forward-compat.
- **auth/jwt.go** — JWT service: HS256 access tokens (short-lived, 15m default) with `uid`/`usr`/`role` claims. Refresh tokens are random hex strings stored hashed (SHA-256) in BoltDB.
- **auth/middleware.go** — `RequireAuth` middleware validates JWT from `Authorization: Bearer` header. `RequireRole(minRole)` checks role hierarchy (viewer < editor < admin). When auth is disabled, `ContextRole()` returns `RoleAdmin` (pass-through).
- **auth/handlers.go** — Login (bcrypt verify + JWT + httpOnly refresh cookie), refresh (token rotation), logout, me, change password. Cookie: `kb_refresh`, path `/api/v1/auth`, httpOnly, SameSite=Strict.
- **auth/user_handlers.go** — Admin-only user CRUD. Protections: cannot delete self, cannot delete/demote last admin. Password minimum 8 chars.
- **config/auth.go** — `LoadAuthConfig()` reads `KUBEBOLT_AUTH_*` env vars. Auto-generates admin password (printed to stderr) and JWT secret (with restart warning) if not set.
- **models/types.go** — All domain types: `ClusterOverview` (with counts for 15 resource types + `Permissions` map), `ResourceUsage`, `ResourceList` (with `Forbidden` flag), `Insight`, `TopologyNode/Edge`, `ClusterInfoResponse`
- **agent/channel/registry.go** + **agent/channel/registry_store.go** — In-memory `AgentRegistry` (live channels, keyed by `<clusterID>/<agentID>`) backed by a persistent `AgentStore` interface. `BoltAgentStore` writes JSON-encoded `AgentRecord` values (capabilities, displayName from Hello label `kubebolt.io/cluster-name`, node, version, FirstSeen/LastSeen/DisconnectedAt) to the `agents` bucket on every `Register`; `MemoryAgentStore` is the test impl. On boot, `cmd/server/main.go` lists records, filters `HasKubeProxy()`, picks the most-recent display name per `cluster_id`, and replays into `manager.AddAgentProxyCluster` BEFORE the gRPC server binds — so the cluster selector keeps showing previously-connected agent-proxy clusters from boot. A 1h ticker prunes records with non-zero `DisconnectedAt` older than the horizon (default 24h, override via `KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON`). Records for currently-connected agents (DisconnectedAt zero) never expire.
- **agent/server.go** — gRPC `AgentChannel` handler. **Welcome before Register** is a hard ordering rule: the agent's reader bails with a 1m backoff if anything other than Welcome is the first message it receives, so `Send(Welcome)` runs BEFORE `registry.Register(agent)` to prevent the multiplexor from routing a `kube_request` to an agent that's still mid-handshake. Defers stay in their LIFO teardown order: `defer maybeAutoUnregisterCluster` (fires last), `defer registeredAgent.Close` (middle), `defer registry.Unregister` (first).
- **agent/channel/tunnel.go** — `TunnelConn` (net.Conn over the gRPC channel for SPDY exec/portforward/files). Credit-based flow control via `KubeStreamAck` (256 KiB window default, configurable via `TunnelWindowBytes`). **Idle watchdog:** every successful Read/Write bumps `lastActivity` (atomic int64 unix-nano); if `idleTimeout > 0` (default `DefaultTunnelIdleTimeout = 5m`, override via `KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT`) a goroutine ticks at `timeout/4` (floored at 100ms so unit tests work) and closes the tunnel with `reason="idle timeout"` when the gap exceeds the window — catches orphan tunnels left behind when the agent crashes mid-session. **Audit log:** one `INFO agent-proxy tunnel opened` line on construction and one `agent-proxy tunnel closed` on `Close()` carrying cluster_id, agent_id, request_id, path, reason, duration, bytes_in, bytes_out. `closeReason` is stashed by `demuxLoop` on peer-EOF / `StreamClosed` / multiplexor-slot-close so the audit log distinguishes those from a `local close`.

### API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /auth/config` | Auth config (enabled flag) — public |
| `POST /auth/login` | Login with username/password — returns JWT + sets refresh cookie |
| `POST /auth/refresh` | Refresh access token via httpOnly cookie |
| `POST /auth/logout` | Invalidate refresh token, clear cookie |
| `GET /auth/me` | Current user profile |
| `PUT /auth/me/password` | Change own password |
| `GET /users` | List all users (Admin only) |
| `POST /users` | Create user (Admin only) |
| `GET /users/:id` | Get user (Admin only) |
| `PUT /users/:id` | Update user (Admin only) |
| `PUT /users/:id/password` | Reset user password (Admin only) |
| `DELETE /users/:id` | Delete user (Admin only) |
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
| `GET /copilot/config` | AI Copilot config (provider, model, enabled flag) — no API key |
| `POST /copilot/chat` | AI Copilot chat with SSE streaming + tool calling loop |

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

| Resource | Overview | YAML | Pods | Logs | Terminal | Files | Containers | Volumes | Related | History | Events | Monitor |
|----------|----------|------|------|------|----------|-------|------------|---------|---------|---------|--------|---------|
| Pods | ✅ | ✅ | — | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — | ✅ | ✅ |
| Deployments | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | ✅ | ✅ | ✅ |
| StatefulSets | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | ✅ | ✅ | ✅ |
| DaemonSets | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | ✅ | ✅ | ✅ |
| Jobs | ✅ | ✅ | ✅ | ✅ | — | — | — | — | ✅ | — | ✅ | — |
| CronJobs | ✅ | ✅ | — | — | — | — | — | — | ✅ | — | ✅ | — |
| Services | ✅ | ✅ | — | — | — | — | — | — | ✅ | — | ✅ | — |
| Nodes | ✅ | ✅ | — | — | — | — | — | — | — | — | ✅ | ✅ |
| Others | ✅ | ✅ | — | — | — | — | — | — | — | — | ✅ | — |

**Key features:**
- YAML viewer with theme-aware syntax highlighting (CSS variables for light/dark) + CodeMirror 6 editor mode with YAML language + One Dark theme
- kubectl describe modal with syntax highlighting (keys, values, events colored by severity)
- Log viewer with syntax coloring (green default, blue timestamps, red errors, yellow warnings)
- Workload logs: pod selector + container selector + tail lines (100/500/1000) + 10s auto-refresh
- Pod terminal: xterm.js with SPDY exec bridge, auto shell detection (bash → sh), multi-container, workload pod selector
- Pod file browser: directory navigation with breadcrumbs, file content viewer, download. Handles distroless containers via `find` fallback
- Port forwarding: per-port buttons in pod detail, Topbar indicator with active forwards dropdown
- Resource actions: Restart (rollout restart), Scale (replica input popover), Delete (confirmation modal with name typing)
- Global search: Cmd+K modal, debounced, grouped by kind with icons, keyboard navigation
- CPU/Memory bars with request/limit markers and hover tooltip (ResourceUsageCell component)
- Related tab uses topology API edges for parent+child navigation
- Monitor tab: SVG donut gauges from Metrics Server (Network/Disk require agent)
- Cross-resource links: Pod→Node, PVC→PV/StorageClass, HPA→target, namespace links
- Configurable refresh interval (5s–2m) persisted in localStorage, selector in DataFreshnessIndicator

### Dashboard Sub-tabs

The Dashboard surface is split into three sub-tabs that share the same `OverviewHeader` + `RangeSelector` + `DataFreshnessIndicator` chrome but offer different lenses on the cluster:

| Route | Component | Question it answers |
|-------|-----------|---------------------|
| `/` | `OverviewPage` | "Is everything fine right now?" — at-a-glance scan |
| `/capacity` | `CapacityPage` | "How is the cluster consuming, and is it sized right?" — investigation |
| `/reliability` | `ReliabilityPage` | "What's the cluster actually serving?" — Hubble L7 lens, conditional |

`DashboardSubTabs.tsx` is the underline-active sub-nav. Reliability is gated on `useHubbleAvailable()` (`apps/web/src/hooks/useHubbleAvailable.ts`) — a 60s-cached probe of `count(pod_flow_http_requests_total{source="hubble"})`. When zero, the tab disappears entirely; an empty Reliability page would be noise, not invitation. The Sidebar's Overview item AND the Topbar's Dashboard pill mark active across all three sub-tabs via `isDashboardPath()` from `apps/web/src/utils/routes.ts`. Future sub-tabs add to `DASHBOARD_PATHS` in that file.

**CapacityPage panels** — 2×2 trends grid (CPU / Memory / Network / Filesystem) from VictoriaMetrics, overlaid with deploy markers from `/deploys` (backend walks ReplicaSet creation timestamps to emit `DeployEvent[]`); Recent Deploys table; `TopWorkloadsCpu` (cluster-wide top consumers, `label_replace` chain collapses ReplicaSet → Deployment); `RightSizingPanel` (deterministic NEAR-LIMIT / OVER-PROV / NO-SPECS rules with absolute floors of 50m CPU / 100Mi memory).

**ReliabilityPage panels** — Cluster error rate chart split into 4xx + 5xx series, with `MetricChart`'s new `tooltipExtra` slot showing absolute volume context at the hovered timestamp (separate range query joined client-side via fuzzy ±step/2 lookup); `TopWorkloadsTraffic` (status_class distribution bar + chips + req/s sparkline); `TopLatencyWorkloads` (160×20 latency sparkline + inline `min..max` from the trend array, no extra query — status breakdown lives in tooltip only to avoid duplicating Traffic); `ErrorHotspots` (sorted by absolute error req/s, not %, so consistently-failing low-volume flows aren't buried); `NetworkDrops` (L4 `verdict=dropped` from `pod_flow_events_total` — the early-warning channel for NetworkPolicy violations and connection refused that HTTP panels miss).

**StatusDistribution shared module** (`apps/web/src/components/dashboard/StatusDistribution.tsx`) — `StatusDistBar`, `ClassRates`, `ClassTooltipRows` visual primitives + `useWorkloadStatusDist(rangeMinutes)` hook with shared queryKey so Traffic and Latency dedupe the same VM round-trip. Agent emits `ok` / `redir` / `client_err` / `server_err` / `info` / `unknown` for status_class — `buildDistIndex` maps these to `success` / `redirect` / `clientErr` / `serverErr` / `unknown` buckets (1xx folded into "other" since it's vanishingly rare).

**Kobi triggers for panels** (`apps/web/src/services/copilot/triggers.ts`) — `panel_inquiry` payload type with kind discriminator (`top_consumers_cpu`, `right_sizing`, `recent_deploys`, `top_workloads_traffic`, `error_hotspots`, `top_latency`, `network_drops`). Multi-row variant for panel-level Ask-Kobi, single-row variant (`singleLead` / `singleClose`) for per-row Ask-Kobi where each row is its own actionable investigation (Recent Deploys, Right-sizing, Error Hot-spots, Network Drops). Operational hints baked into the close prompts — e.g. `error_hotspots` reminds the LLM that 4xx points at the caller while 5xx points at the receiver.

**`MetricChart` `tooltipExtra` prop** — optional callback receiving the hovered timestamp (unix seconds) and returning JSX rendered below the standard payload, behind a divider. Lets a page surface out-of-band context (separate range query, joined map) without forcing every chart to learn about it. Default behavior unchanged for charts that don't pass the prop. Also added `'percent'` to `UnitKind` (label `%`, divisor 1) and `errorRate` accent (red `#ef4056`) to `METRIC_ACCENTS`.

**`collapsePodToWorkload` helper** (`apps/web/src/utils/promql.ts`) — three-pass `label_replace` chain that derives a `workload` label from a Hubble pod-keyed metric: pass 1 sets workload = pod (default fallback), pass 2 strips a single trailing hash (DaemonSet shape), pass 3 strips two trailing hashes (ReplicaSet/Deployment shape). StatefulSet pods retain their full name (numeric ordinal `redis-0` doesn't match `[a-z0-9]{4,8}`), which is correct — the pod IS the unit in a StatefulSet. Accepts `podLabel` (default `dst_pod`) and `outputLabel` (default `workload`) so both src and dst can be collapsed in the same query (used by `ErrorHotspots` and `NetworkDrops`).

**Layout empty-state precedence** (`components/layout/Layout.tsx`) — the main render branch picks one of these in order:
1. `isSwitching` → "Connecting to cluster" spinner.
2. `isPlatformRoute` (`/clusters`, `/admin/*`, `/settings`) → render `<Outlet />` regardless of cluster state, so the user can manage from inside an empty state.
3. `noClusters` (clusters list `null` or `[]`) → centered "No clusters configured" + admin-only "Add cluster" CTA → `/clusters`. Detect both `null` (Go's nil-slice JSON shape) AND `[]`.
4. `isAwaitingAgent` (503 with error message matching `/no agent connected yet|waiting for agent to register/i`) → spinner + "Waiting for agent to register" copy and **no Retry button** (clicking it just re-fast-fails). The page auto-heals via the `cluster:connected` WS broadcast.
5. `isUnavailable` (any other 503) → existing "Cluster unreachable" + Retry button + auto-retry-every-30s.
6. else → `<Outlet />`.

When adding a new platform-level route that doesn't depend on a cluster, append it to `PLATFORM_ROUTE_PREFIXES` in Layout so it bypasses the empty-state branches.

**Key frontend behaviors:**
- TanStack Query `retry` skips retries on 503 (cluster unavailable) and 403 (permission denied). Targeted invalidation via the `cluster:connected` WS event in `useWebSocket` brings stale 503-ed queries back without waiting on the 30s refetch interval.
- `ApiError` (from `api.ts`) used to detect 503/403 vs other errors. Layout regex-matches `error.message` for "no agent connected yet" / "waiting for agent to register" to choose the awaiting-agent branch over the generic unreachable one.
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

1. **Auth initialization** (if enabled): BoltDB store opened, admin user seeded on first boot, JWT service created. Auth middleware wraps all routes.
2. Cluster Manager reads kubeconfig contexts; initial K8s connection starts async in background
3. **Permission probe** runs 22 SelfSubjectAccessReview calls (cluster-wide, then namespace fallback) to detect access level
4. HTTP server is immediately available — returns 503 on cluster-dependent routes until connected
4. Shared informers start **only for permitted resources**; namespace-scoped SAs get per-namespace factories with multi-lister aggregation
5. Dynamic client discovers Gateway API resources (with 5s timeout)
6. Metrics Collector polls Metrics Server → in-memory metrics cache (per-namespace polling for namespace-scoped SAs)
7. Insights Engine evaluates 15 rules against cluster state → recommendations
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

## Release security gates

`.github/workflows/release.yml` (triggered on `v*` tag push) has two Trivy gates that **must pass** before the GitHub Release is created:

1. **`preflight-third-party-scan`** runs FIRST, before any image build. Parses the VictoriaMetrics tag out of `deploy/helm/kubebolt/values.yaml`, verifies it matches the pin in `deploy/docker-compose.yml` (drift = hard fail), and Trivy-scans `docker.io/victoriametrics/victoria-metrics:<tag>`. Fails on `CRITICAL,HIGH` with `--ignore-unfixed`. Catches the v1.8.0 class of bug where a stale third-party pin shipped vulnerable.
2. **`image-scan`** runs after `build-api` / `build-web` / `build-single-container` and scans the just-pushed images **by `@sha256` digest** (immutable, guarantees we audit the exact bits). Same severity policy. Also emits CycloneDX SBOMs that are attached to the GitHub Release as `sbom-{api,web,single}.cdx.json`. SARIF reports for both gates upload to the GitHub Security tab under categories `trivy-victoriametrics` and `trivy-release-images`.

`release` and `publish-chart` both have `image-scan` in `needs:` — a failed scan blocks chart publication and the GitHub Release. Note that the images themselves are already on GHCR by the time `image-scan` runs (push happens during build); a failure means **no Release is cut, but you must manually delete the orphaned image tags from GHCR Packages**, then bump the dependency and retag.

**Suppressions** live in `.trivyignore` at the repo root — every entry needs an owner, a justification, and a remove-by date. Don't grow that file to push a release out the door; bump the dependency instead.

**Renovate** (`renovate.json`) groups VictoriaMetrics bumps across `docker-compose.yml` and `values.yaml` into a single PR via a regex custom manager, since the helm values file isn't a stock Renovate manager target. The two pin sites are coupled — drift trips the preflight gate.

### Third-party image pins (single-source-of-truth map)

| Image | Authoritative pin | Mirror (must match) |
|-------|-------------------|----------------------|
| `victoriametrics/victoria-metrics` | `deploy/helm/kubebolt/values.yaml` (`metrics.storage.embedded.image.tag`) | `deploy/docker-compose.yml` (`victoriametrics:` service) |
| `victoriametrics/vmagent` | `deploy/helm/kubebolt-agent/values.yaml` (`scrape.image.tag`) | must match the VictoriaMetrics pin above — both binaries are built from the same source tree, so they share Go stdlib CVEs. Drift between the two trips the preflight scan. |

When adding a new third-party image, add it to this table, add a Trivy scan step in `preflight-third-party-scan`, extend the drift check, and add a Renovate `customManager` entry in `renovate.json` so the bump auto-PRs land grouped with the rest of the line.

## Key Reference

`docs/SPEC.md` contains the detailed technical specification including API endpoints, insights rules, data models, and Phase 2 roadmap. Consult it for feature work.
