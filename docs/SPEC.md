# KubeBolt — Technical Specification

> **Version:** 1.1
> **Status:** Phase 1 Implemented
> **Last Updated:** March 2026

---

## 1. Executive Summary

KubeBolt is a Kubernetes monitoring platform that provides instant cluster visibility with zero configuration. Users connect their kubeconfig and get a visual, intuitive dashboard with actionable insights in under 2 minutes.

**Tagline:** *Connect. See. Fix.*

**Target users:** Development teams and small-to-medium engineering orgs that deploy on Kubernetes but don't have dedicated SRE teams.

**Core principles:**
- Zero-config: only a kubeconfig needed
- Insights over metrics: human-readable recommendations, not raw numbers
- Universal: Docker Desktop, Minikube, EKS, AKS, GKE, k3s
- Progressive enhancement: start with zero install, optionally add agent for richer data

---

## 2. Architecture Overview

```
┌─────────────────────────────────────────────┐
│          Kubernetes Cluster (User)          │
│                                             │
│  ┌──────────┐ ┌──────────┐ ┌─────────────┐ │
│  │ API      │ │ Metrics  │ │ Events API  │ │
│  │ Server   │ │ Server   │ │             │ │
│  └────┬─────┘ └────┬─────┘ └──────┬──────┘ │
│       │            │              │         │
│  ┌────┴────────────┴──────────────┴──────┐  │
│  │   kubebolt-agent (Phase 2 only)       │  │
│  │   DaemonSet · reads kubelet/cAdvisor  │  │
│  └───────────────────┬───────────────────┘  │
└──────────────────────┼──────────────────────┘
                       │ kubeconfig (Phase 1)
                       │ gRPC stream (Phase 2)
┌──────────────────────┼──────────────────────┐
│         KubeBolt Backend (Go)               │
│                                             │
│  ┌──────────────┐  ┌────────────────────┐   │
│  │ Cluster      │  │ Metrics Collector  │   │
│  │ Manager      │  │ (Metrics Server +  │   │
│  │ (client-go)  │  │  Agent receiver)   │   │
│  └──────┬───────┘  └────────┬───────────┘   │
│         │                   │               │
│  ┌──────┴───────────────────┴───────────┐   │
│  │         Insights Engine              │   │
│  │   Rules · Heuristics · Alerts        │   │
│  └──────────────────┬───────────────────┘   │
│                     │                       │
│  ┌─────────┐  ┌─────┴──────┐  ┌──────────┐ │
│  │ REST API│  │ WebSocket  │  │ SQLite / │ │
│  │ (Chi)   │  │ Hub        │  │ TSDB     │ │
│  └────┬────┘  └─────┬──────┘  └──────────┘ │
└───────┼─────────────┼───────────────────────┘
        │             │
┌───────┼─────────────┼───────────────────────┐
│       Frontend (React + TypeScript)         │
│                                             │
│  ┌──────────┐  ┌──────────┐  ┌───────────┐ │
│  │Dashboard │  │ Cluster  │  │ Resource  │ │
│  │ Overview │  │ Map      │  │ Views x23 │ │
│  └──────────┘  └──────────┘  └───────────┘ │
└─────────────────────────────────────────────┘
```

### Data Flow

**Phase 1 (zero install):**
1. User provides kubeconfig (all contexts auto-discovered for multi-cluster)
2. Cluster Manager connects to selected K8s API Server via client-go
3. **Permission probe** runs SelfSubjectAccessReview for 22 resource types to detect access level
4. Shared informer factory watches **only permitted** resource types (skips denied resources)
5. For namespace-scoped ServiceAccounts: per-namespace informer factories with multi-lister aggregation
6. Dynamic client discovers Gateway API resources (Gateways, HTTPRoutes)
7. Metrics Collector polls `metrics.k8s.io/v1beta1` every 30s (per-namespace when cluster-wide denied)
8. Insights Engine evaluates 12 rules against current state
9. REST API serves resource lists, details, topology (with metrics enrichment). Returns 403 for restricted resources.
10. WebSocket broadcasts real-time updates to frontend
11. User can switch clusters at runtime via API

**Phase 2 (agent installed):**
1. `kubebolt-agent` DaemonSet reads kubelet/cAdvisor on each node
2. Streams metrics via gRPC to backend every 15s
3. Backend merges with API Server data
4. Network I/O, disk, container-level metrics now available
5. TSDB stores historical time-series

---

## 3. Phase 1 — MVP Specification

### 3.1 Data Sources

| Source | Data Provided | K8s API |
|--------|--------------|---------|
| API Server | Resource state, relationships, events, labels, annotations, ownerReferences | Watch/List via client-go shared informers |
| Metrics Server | Current CPU and memory per pod and node (point-in-time) | `metrics.k8s.io/v1beta1` → PodMetrics, NodeMetrics |
| Events API | Warnings, errors, state changes, scheduling decisions | `core/v1` Events with field selectors |

### 3.2 Resources Auto-Discovered

**Workloads:**
- Pods (status, containers, restarts, conditions)
- Deployments (replicas, strategy, conditions)
- StatefulSets (replicas, update strategy)
- DaemonSets (desired, current, ready)
- Jobs (completions, duration, status)
- CronJobs (schedule, last run, active)
- ReplicaSets (used for ownerReference resolution)

**Traffic:**
- Services (type, clusterIP, ports, selector)
- Ingresses (hosts, paths, backends, TLS)
- Gateways (gateway.networking.k8s.io — class, address, listeners, status)
- HTTPRoutes (gateway.networking.k8s.io — hostnames, gateway parent, backends)
- EndpointSlices (addresses, ports, ready) — migrated from deprecated v1.Endpoints

**Storage:**
- PersistentVolumeClaims (status, volume, capacity, class)
- PersistentVolumes (capacity, access, reclaim, status)
- StorageClasses (provisioner, reclaim, binding mode)

**Config:**
- ConfigMaps (key count, namespace)
- Secrets (type, key count — NEVER read values)
- HorizontalPodAutoscalers (targets, min/max, current)

**Cluster:**
- Nodes (status, capacity, allocatable, conditions, version)
- Namespaces (status, resource count)
- Events (type, reason, message, involvedObject)
- RBAC (Roles, ClusterRoles, RoleBindings, ClusterRoleBindings)

### 3.3 What Phase 1 Cannot Provide

| Metric | Why Not Available | Phase 2 Solution |
|--------|------------------|-----------------|
| Network I/O (bytes in/out) | Metrics Server only provides CPU/memory | Agent reads cAdvisor |
| Disk / Filesystem usage | Not exposed by Metrics Server | Agent reads kubelet stats |
| Historical time-series (>5min) | Metrics Server is point-in-time only | Agent streams to TSDB |
| Container-level granularity | Metrics Server aggregates at pod level | Agent reads per-container |
| CPU throttling metrics | Not in Metrics Server | Agent reads cAdvisor cpu.cfs_throttled |

### 3.4 Metrics Server Compatibility

| Provider | Status | Action Required |
|----------|--------|----------------|
| EKS | Installed by default | None |
| GKE | Installed by default | None |
| AKS | Installed by default | None |
| Docker Desktop | Not installed | `kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml` |
| Minikube | Addon available | `minikube addons enable metrics-server` |
| k3s | Installed by default | None |
| k0s | Installed by default | None |

**Graceful degradation:** If Metrics Server is not available, KubeBolt shows all resource state and events but CPU/memory bars display "Metrics Server not detected — install for CPU/memory data" with a one-click install command.

### 3.5 RBAC Permission Detection

KubeBolt auto-detects the connected kubeconfig's permissions at connection time and adapts its behavior. Works with any access level — from cluster-admin to namespace-scoped read-only ServiceAccounts.

**Permission probe (`permissions.go`):**
- Uses `SelfSubjectAccessReview` API to test `list` verb for each of the 22 resource types
- Two-phase probe: cluster-wide first, then namespace-level fallback for RoleBinding-based access
- Concurrent execution (semaphore of 10), completes in ~2-5s
- If SSAR API itself is unavailable, falls back to assume full access (preserves existing behavior)

**Access levels supported:**

| Level | Detection | Backend Behavior | Frontend Behavior |
|---|---|---|---|
| Cluster-admin | All 22 SSAR probes pass | All informers start normally | Full UI, no restrictions |
| Cluster read-only | Some probes fail (e.g., Secrets, RBAC) | Informers only for permitted resources | Restricted items dimmed, "Limited access" banner |
| Namespace-scoped | Cluster-wide probes fail, namespace probes pass | Per-namespace `SharedInformerFactory` instances with multi-lister aggregation (`nslister.go`) | Same as above, resources scoped to permitted namespaces |

**Namespace-scoped informers (`nslister.go`):**
- When a ServiceAccount has RoleBindings (not ClusterRoleBindings), cluster-wide list/watch is denied
- KubeBolt creates one `SharedInformerFactory` per accessible namespace using `informers.WithNamespace(ns)`
- Multi-lister wrappers aggregate `List()` across all factories and `Get()` tries each until found
- Metrics Collector polls per-namespace (`PodMetricses(ns).List()`) instead of cluster-wide

**Frontend permission UI:**
- "Limited access — showing X of Y resource types" banner in Layout
- Sidebar items dimmed with shield icon for restricted resources
- Summary cards show "No access" instead of misleading "0"
- CPU/Memory panels show "No access to Nodes — capacity data unavailable" when node access denied
- `PermissionDenied` component for 403 resource pages
- "Connecting to cluster" overlay during cluster switch (permission probe + informer sync)

**API endpoint:** `GET /cluster/permissions` returns the full permission map per resource type. The `GET /cluster/overview` response also includes a `permissions` field with simplified `key → bool` access map.

### 3.6 Relationship Detection

KubeBolt builds a cluster topology graph by analyzing:

| Relationship | Detection Method |
|-------------|-----------------|
| Service → Pods | Match service `.spec.selector` against pod labels |
| Deployment → ReplicaSet → Pods | `ownerReferences` chain |
| StatefulSet → Pods | `ownerReferences` |
| DaemonSet → Pods | `ownerReferences` |
| Job → Pods | `ownerReferences` |
| CronJob → Jobs | `ownerReferences` |
| Ingress → Service | `.spec.rules[].http.paths[].backend.service` |
| Gateway → HTTPRoute | HTTPRoute `.spec.parentRefs` (dynamic client) |
| HTTPRoute → Service | HTTPRoute `.spec.rules[].backendRefs` (dynamic client) |
| Pod → PVC | `.spec.volumes[].persistentVolumeClaim.claimName` |
| PVC → PV | `.spec.volumeName` |
| Pod → ConfigMap | `.spec.volumes[].configMap` + `.spec.containers[].envFrom[].configMapRef` |
| Pod → Secret | `.spec.volumes[].secret` + `.spec.containers[].envFrom[].secretRef` + `.spec.imagePullSecrets` |
| HPA → Deployment/StatefulSet | `.spec.scaleTargetRef` |

---

## 4. Phase 2 — Data Ingestion Architecture

Phase 2 adds historical telemetry to KubeBolt via two new components:

1. **VictoriaMetrics** as the time-series database (TSDB) — stores every metric received from agents and exposes PromQL for query.
2. **kubebolt-agent** — lightweight Go DaemonSet that collects per-container metrics from each node's kubelet/cAdvisor and streams them to the backend via gRPC.

The full technical design lives in `internal/kubebolt-agent-technical-spec.md`. This section summarizes the canonical architecture and the MVP scope. Strategic alternatives considered (OTel Collector fork, fully agentless ingestion) are preserved in `internal/agentless-ingestion-analysis.md` and in strategy v2.1 §8 for future reconsideration.

### 4.1 Storage: VictoriaMetrics

- Single-binary Go TSDB running alongside the backend (Docker Compose service, Helm sub-chart, or operator-managed deployment).
- Accepts ingest via Prometheus `remote_write` protocol (`/api/v1/write`). OTLP and native VM protocols also supported for future reuse.
- Query via PromQL (`/api/v1/query`, `/api/v1/query_range`).
- Retention policies configurable per deployment. Native downsampling support for long-term storage.
- Abstracted behind a `MetricsStorage` interface in the backend to allow future substitution (ClickHouse at SaaS scale) without rewriting ingest or query layers.

### 4.2 Agent: kubebolt-agent

The `kubebolt-agent` is a lightweight DaemonSet written in Go that runs one pod per node.

**Requirements:**
- Single static binary (Go, `CGO_ENABLED=0`), <20MB compressed image (distroless)
- Resource consumption: <50MB RAM, <0.05 CPU per node (hard targets, non-negotiable)
- Install via Helm: `helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent`
- Read-only access, no cluster-admin, no exec, no secret access
- Metrics collected every 15 seconds (configurable 5-60s)
- Reconnects automatically if backend unavailable; in-memory ring buffer (FIFO drop on overflow) up to 5 minutes
- Read-only `/host/proc` mount; no privileged container; all capabilities dropped; `runAsNonRoot: true`
- Independent per-node: no peer-to-peer coordination, no disk persistence, crash-clean restart

### 4.3 Metrics Collected (MVP core)

| Category | Metrics | Source | Granularity |
|----------|---------|--------|-------------|
| CPU (detailed) | `usage_core_nanoseconds`, `throttled_time`, `throttled_periods`, `nr_periods`, `nr_throttled` | cAdvisor `/stats/summary` | Per container |
| Memory (detailed) | `working_set_bytes`, `rss_bytes`, `cache_bytes`, `swap_bytes`, `major/minor_page_faults`, `failcnt` | cAdvisor | Per container |
| Network | `rx_bytes`, `tx_bytes`, `rx_errors`, `tx_errors`, `rx_packets_dropped`, `tx_packets_dropped` | cAdvisor | Per container |
| Disk (volumes) | `fs_usage_bytes`, `fs_capacity_bytes`, `fs_available_bytes`, `inodes_used`, `inodes_total` | kubelet `/stats/summary` | Per volume, per node |
| Process | `process_count`, `thread_count`, `open_fds` | cAdvisor | Per container |
| Node system | `load_average_1m/5m/15m`, `kernel_memory_bytes`, `uptime_seconds` | `/host/proc` | Per node |

Full catalog (including optional super-powers like process-level, conntrack, eBPF network flows, GPU, DNS observability, log tailing) in the technical spec §4 and §20. These are **post-MVP extensions**, not part of Phase 2.0.

### 4.4 Agent Communication

gRPC service `AgentIngest` defined in `packages/proto/agent.proto` with three RPCs:

- `Register` — initial handshake, agent sends node metadata, backend returns `AgentConfig` and `agent_id`.
- `StreamMetrics` — bidirectional stream: agent pushes `MetricBatch` messages, backend responds with `IngestAck` (can carry config updates for hot-reload).
- `Heartbeat` — periodic liveness + self-reported metrics (samples collected/sent/dropped, buffer size, own CPU/memory).

Authentication: ServiceAccount token validated by the backend via `TokenReview` against the origin cluster's Kube API (cached 5 min). TLS mandatory. Full proto schema in `internal/kubebolt-agent-technical-spec.md` §5.

### 4.5 Agent RBAC

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubebolt-agent-reader
rules:
  - apiGroups: [""]
    resources: ["nodes/stats", "nodes/proxy", "nodes/metrics"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list", "watch"]   # fallback when kubelet /pods is unavailable
  # No write, no exec, no secret access
```

### 4.6 MVP Implementation Plan

Five sprints, ~8-10 weeks calendar for one full-time engineer (compressible with two engineers in parallel):

| Sprint | Scope | Duration |
|--------|-------|----------|
| **0** | Walking skeleton: VM in Docker Compose + gRPC server skeleton + agent sending one hardcoded metric end-to-end + minimal query endpoint | 1-2 weeks |
| **1** | Real collectors: `kubelet_stats_summary`, `kubelet_pods`, `node_proc`, enrichment processor, ring buffer, backend write to VM | 2 weeks |
| **2** | Production backend: TokenReview auth, agent registry (BoltDB), rate limiting, cardinality guard, metrics history query API, heartbeat alerting | 2 weeks |
| **3** | Frontend charts: chart library integration, `<MetricChart>` component, Monitor tab with historical charts replacing SVG donuts, agent admin page | 2-3 weeks |
| **4** | Packaging: Dockerfile distroless, Helm chart `kubebolt-agent`, multi-arch build (amd64 + arm64), install docs | 1-2 weeks |

Post-MVP extensions (eBPF network flows, conntrack, process-level metrics, GPU observability, DNS observability, log tailing, CNI-specific collectors, CSI observability) are documented in technical spec §20 and are deferred to later phases.

---

## 5. Backend Architecture

### 5.1 Technology Stack

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| Language | Go 1.22+ | Native K8s ecosystem, single binary, performance |
| K8s client | client-go v0.35+ with shared informers | Official library, watch/list with local cache |
| K8s dynamic | k8s.io/client-go/dynamic | Gateway API CRD discovery without typed clients |
| HTTP framework | Chi v5 | Lightweight, idiomatic, middleware |
| WebSocket | gorilla/websocket | Standard Go WebSocket |
| Database | SQLite (Phase 1) | Zero-dependency for self-hosted, embedded |
| TSDB | VictoriaMetrics (Phase 2+) | Prometheus-compatible, efficient storage |
| gRPC | google.golang.org/grpc | Agent communication (Phase 2) |
| Auth | JWT (go-jwt) | Multi-tenant SaaS support |

### 5.2 Package Structure

```
apps/api/
├── cmd/server/
│   └── main.go                 # Entry point, wiring, graceful shutdown
├── internal/
│   ├── cluster/
│   │   ├── connector.go        # Kubeconfig loading, clientset creation, resource listing
│   │   ├── manager.go          # Multi-cluster manager, context switching
│   │   ├── graph.go            # In-memory topology graph
│   │   ├── relationships.go    # Edge detection (selectors, ownerRefs, Gateway API)
│   │   └── state.go            # ClusterState accessor methods for insights
│   ├── metrics/
│   │   └── collector.go        # Metrics Server polling loop + in-memory cache
│   ├── insights/
│   │   ├── engine.go           # Rule evaluation loop
│   │   ├── rules.go            # All insight rules (see Section 5.5)
│   │   └── types.go            # Insight, Rule types
│   ├── api/
│   │   ├── router.go           # Chi router setup, middleware
│   │   ├── handlers.go         # HTTP handlers for each endpoint
│   │   ├── middleware.go       # CORS, auth, logging
│   │   └── responses.go       # Standardized JSON response helpers
│   ├── websocket/
│   │   ├── hub.go              # Connection registry, broadcast
│   │   ├── client.go           # Individual WebSocket connection
│   │   └── events.go           # Event type definitions
│   ├── agent/                  # Phase 2
│   │   ├── receiver.go         # gRPC server receiving agent streams
│   │   └── merger.go           # Merge agent data with API Server data
│   ├── models/
│   │   └── types.go            # All shared data types
│   └── config/
│       └── config.go           # Configuration loading (env, flags, file)
├── go.mod
└── go.sum
```

### 5.3 Cluster Manager & Connector Implementation

```go
// manager.go — Multi-cluster lifecycle management
// Reads all contexts from kubeconfig, manages connector/collector/engine per cluster

manager, err := cluster.NewManager(kubeconfigPath, wsHub, metricInterval, insightInterval)
// Switches: tears down old connector, creates new one for target context
manager.SwitchCluster("production-eks")
// Lists all available contexts
clusters := manager.ListClusters()

// connector.go — Key implementation details

// 1. Load kubeconfig for specific context
rules := clientcmd.NewDefaultClientConfigLoadingRules()
overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}

// 2. Create clientsets (typed + dynamic for CRDs)
clientset, err := kubernetes.NewForConfig(config)
dynamicClient, err := dynamic.NewForConfig(config)  // For Gateway API
metricsClient, err := metricsv.NewForConfig(config)

// 3. Create shared informer factory (resync every 30s)
factory := informers.NewSharedInformerFactory(clientset, 30*time.Second)

// 4. Register informers for all resource types + event handlers
// 5. Topology rebuild is debounced (2s) to coalesce rapid events
// 6. Gateway API resources discovered via dynamic client with 5s timeout
```

### 5.4 Metrics Collector Implementation

```go
// collector.go — Metrics Server polling

func (c *Collector) Start(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    for {
        select {
        case <-ticker.C:
            c.collectPodMetrics(ctx)
            c.collectNodeMetrics(ctx)
        case <-ctx.Done():
            return
        }
    }
}

func (c *Collector) collectPodMetrics(ctx context.Context) {
    // Use metrics.k8s.io/v1beta1
    podMetrics, err := c.metricsClient.MetricsV1beta1().
        PodMetricses("").List(ctx, metav1.ListOptions{})
    if err != nil {
        if isMetricsServerNotAvailable(err) {
            c.metricsAvailable = false // Flag for UI degradation
            return
        }
    }
    c.metricsAvailable = true
    for _, pm := range podMetrics.Items {
        point := models.MetricPoint{
            Timestamp: time.Now(),
            Resource:  fmt.Sprintf("pod/%s/%s", pm.Namespace, pm.Name),
            CPUUsage:  pm.Containers[0].Usage.Cpu().MilliValue(),
            MemUsage:  pm.Containers[0].Usage.Memory().Value(),
        }
        c.store.Save(point)
        c.cache.Update(pm.Namespace, pm.Name, point)
    }
}
```

### 5.5 Insights Engine Rules

Each rule is a function that evaluates against the current cluster state:

```go
type Rule struct {
    Name     string
    Severity string // "critical", "warning", "info"
    Evaluate func(state *ClusterState) []Insight
}
```

**Phase 1 Rules (implement all):**

| ID | Severity | Condition | Message Template |
|----|----------|-----------|-----------------|
| `crash-loop` | critical | Pod in CrashLoopBackOff with restarts > 3/hour | `"{pod}" is crash-looping with {restarts} restarts in the last hour. Check logs: kubectl logs {pod} -n {ns}` |
| `oom-killed` | critical | Pod terminated with OOMKilled (exit code 137) | `"{pod}" was killed for exceeding memory limit ({limit}). Current usage trends suggest increasing to {suggested}.` |
| `cpu-throttle-risk` | warning | CPU usage > 80% sustained for > 15 min | `"{deployment}" CPU at {pct}% for {duration}. Risk of throttling. Consider scaling to {n} replicas or increasing CPU limits.` |
| `memory-pressure` | warning | Memory usage > 85% of limit | `"{deployment}" memory at {pct}% of limit ({used}/{limit}). OOMKill risk if usage spikes.` |
| `resource-underrequest` | info | Requests < 40% of actual usage | `"{deployment}" requests ({req}) are well below actual usage ({actual}). Scheduler may overpack nodes.` |
| `zero-replicas` | critical | Deployment with 0 available replicas | `"{deployment}" has no available pods. Check events for scheduling failures or image pull errors.` |
| `pvc-pending` | warning | PVC in Pending state for > 5 min | `PVC "{pvc}" has been pending for {duration}. Check StorageClass and available PVs.` |
| `node-not-ready` | critical | Node condition Ready != True | `Node "{node}" is not ready. Status: {condition}. This affects {n} pods.` |
| `hpa-maxed-out` | warning | HPA current replicas == max replicas | `HPA "{hpa}" has scaled to maximum ({max} replicas). Workload may need higher limits or different scaling strategy.` |
| `frequent-restarts` | warning | Pod with > 5 restarts in last 24h (non-crash-loop) | `"{pod}" has restarted {n} times in 24h. Check for intermittent issues in logs.` |
| `image-pull-backoff` | critical | Pod in ImagePullBackOff | `"{pod}" cannot pull image "{image}". Verify the image exists and credentials are configured.` |
| `evicted-pods` | info | Pods evicted from node | `{n} pods evicted from node "{node}" due to {reason}. Consider adjusting resource limits.` |

### 5.6 REST API

**Base URL:** `/api/v1`

#### Endpoints

```
GET  /clusters
  Response: [{ name, context, server, active }]

POST /clusters/switch
  Body: { context: "context-name" }
  Response: { status: "ok", context: "context-name" }

GET  /cluster/overview
  Response: { clusterName, kubernetesVersion, platform,
              nodes, pods, namespaces, services, deployments, statefulSets, daemonSets, jobs,
              cpu, memory, health, events, namespaceWorkloads }
  ResourceCount: { total, ready, notReady, warning }
  ResourceUsage: { used, requested, limit, allocatable, percentUsed, percentRequested }

GET  /cluster/health
  Response: { status, score, insights: {critical, warning, info}, checks: [{name, status, message}] }

GET  /resources/:type
  Params: ?namespace=X&search=Y&status=Z&sort=name&order=asc&limit=50
  Types: pods, deployments, statefulsets, daemonsets, jobs, cronjobs,
         services, ingresses, gateways, httproutes, endpoints,
         pvcs, pvs, storageclasses, configmaps, secrets, hpas,
         nodes, namespaces, events, roles, clusterroles, rolebindings, clusterrolebindings
  Response: { kind, items: [...], total }
  Note: pods/deployments/nodes items include cpuUsage, memoryUsage, cpuPercent, memoryPercent

GET  /resources/:type/:namespace/:name
  Response: { resource details as map }

GET  /topology
  Response: { nodes: [TopologyNode], edges: [TopologyEdge] }

GET  /insights
  Params: ?severity=critical,warning&resolved=false
  Response: { items: [Insight], total }

GET  /events
  Params: ?type=Warning&namespace=X&limit=100
  Response: { kind, items: [...], total }

GET  /metrics/:type/:namespace/:name
  Response: MetricPoint (current)

WS   /ws
  Subscribe: { type: "subscribe", resources: ["pods", "events", "insights"] }
  Receive: { type: "resource:updated|deleted", data: {...} }
           { type: "event:new", data: {...} }
           { type: "insight:new|resolved", data: {...} }
           { type: "metrics:refresh", data: {...} }
           { type: "cluster.switched", data: { context } }
```

### 5.7 WebSocket Events

```typescript
// Event types use colon notation (resource:updated, not resource.updated)
type WSEvent =
  | { type: "resource:updated"; data: K8sObject }
  | { type: "resource:deleted"; data: K8sObject }
  | { type: "event:new"; data: Event }
  | { type: "insight:new" | "insight:resolved"; data: Insight }
  | { type: "metrics:refresh"; data: { resources: Array<{ name: string; cpu: number; memory: number }> } }
  | { type: "cluster.switched"; data: { context: string } }
```

**Implementation notes:**
- Broadcast channel buffer: 4096 messages
- Messages dropped silently when no clients connected (avoids log spam during cluster switches)
- Topology rebuild debounced to 2s to prevent broadcast floods

---

## 6. Frontend Architecture

### 6.1 Technology Stack

| Layer | Technology | Rationale |
|-------|-----------|-----------|
| Framework | React 18+ with TypeScript | Type safety for K8s data, ecosystem |
| Bundler | Vite 5 | Fast HMR, optimized builds |
| Styling | Tailwind CSS 3.4 | Custom dark theme, consistency |
| Cluster Map | React Flow 11 | Built-in pan/zoom/minimap, node-based UI |
| Charts | Recharts 2 | React-native, composable |
| Data fetching | TanStack Query 5 | Cache, refetch, loading/error |
| Tables | TanStack Table 8 | Sort, filter, paginate, virtualize |
| Real-time | Native WebSocket | Event stream |
| Routing | React Router 6 | Sidebar nav mapped to routes |
| Icons | Lucide React | Consistent icon set |

### 6.2 Frontend Structure

```
apps/web/
├── public/
├── src/
│   ├── App.tsx                     # Root: router + query provider + WS provider
│   ├── main.tsx                    # Entry point
│   ├── components/
│   │   ├── layout/
│   │   │   ├── Sidebar.tsx         # Navigation sidebar (all resource types)
│   │   │   ├── Topbar.tsx          # Cluster selector, search, view toggle
│   │   │   └── Layout.tsx          # Main layout wrapper
│   │   ├── dashboard/
│   │   │   ├── OverviewPage.tsx    # Summary cards + CPU/Mem + events + workloads
│   │   │   ├── SummaryCards.tsx    # Nodes/Pods/NS/Services count cards
│   │   │   ├── ResourceUsage.tsx   # CPU/Memory bars with requests vs limits
│   │   │   ├── WorkloadHealth.tsx  # Stacked health bars by type
│   │   │   ├── EventsFeed.tsx      # Recent events timeline
│   │   │   ├── NamespaceSection.tsx # Workloads grouped by namespace
│   │   │   └── DeploymentCard.tsx  # Individual deployment card with pod dots
│   │   ├── resources/
│   │   │   ├── ResourceListPage.tsx # Generic table view for any resource type
│   │   │   ├── ResourceTable.tsx    # TanStack Table wrapper
│   │   │   ├── FilterBar.tsx        # Namespace + search + status filters
│   │   │   ├── StatusBadge.tsx      # Ok/Warning/Error status pill
│   │   │   ├── UsageBar.tsx         # Inline CPU/Memory bar
│   │   │   ├── NodesPage.tsx        # Node cards with detailed metrics
│   │   │   ├── EventsPage.tsx       # Full events timeline with filters
│   │   │   ├── NamespacesPage.tsx   # Namespace cards with resource counts
│   │   │   ├── RBACPage.tsx         # Roles and bindings view
│   │   │   └── SettingsPage.tsx     # Cluster info + agent install CTA
│   │   ├── map/
│   │   │   ├── ClusterMap.tsx       # React Flow canvas wrapper
│   │   │   ├── ResourceNode.tsx     # Custom React Flow node component
│   │   │   ├── ConnectionEdge.tsx   # Custom edge with animation
│   │   │   ├── NamespaceRegion.tsx  # Background region for namespace grouping
│   │   │   ├── MapControls.tsx      # Zoom buttons, minimap toggle
│   │   │   └── NodeDetailPanel.tsx  # Flyout panel on node click
│   │   ├── shared/
│   │   │   ├── Phase2Placeholder.tsx # "Requires KubeBolt Agent" component
│   │   │   ├── LoadingSpinner.tsx
│   │   │   ├── ErrorState.tsx
│   │   │   └── EmptyState.tsx
│   │   └── insights/
│   │       ├── InsightCard.tsx      # Individual insight with severity
│   │       └── InsightsList.tsx     # Sidebar or dedicated view
│   ├── hooks/
│   │   ├── useClusterOverview.ts   # TanStack Query for overview data
│   │   ├── useResources.ts         # Generic resource list query
│   │   ├── useTopology.ts          # Topology graph for cluster map
│   │   ├── useInsights.ts          # Active insights
│   │   ├── useWebSocket.ts         # WebSocket connection + event dispatch
│   │   └── useMetrics.ts           # Resource metrics with polling
│   ├── services/
│   │   ├── api.ts                  # REST API client (fetch wrapper)
│   │   └── websocket.ts            # WebSocket manager with reconnect
│   ├── types/
│   │   └── kubernetes.ts           # All TypeScript interfaces (mirrors backend)
│   ├── utils/
│   │   ├── formatters.ts           # CPU/memory/age/byte formatters
│   │   └── colors.ts               # Status color mapping
│   └── styles/
│       └── globals.css             # Tailwind imports + custom theme
├── tailwind.config.ts
├── vite.config.ts
├── tsconfig.json
└── package.json
```

### 6.3 Custom Theme (Tailwind)

```typescript
// tailwind.config.ts
export default {
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        kb: {
          bg: '#0a0b0f',
          surface: '#101118',
          card: '#161720',
          'card-hover': '#1c1d2a',
          elevated: '#22243a',
          sidebar: '#0d0e14',
          border: 'rgba(255,255,255,0.06)',
          'border-active': 'rgba(255,255,255,0.14)',
        },
        status: {
          ok: '#22d68a',
          'ok-dim': 'rgba(34,214,138,0.12)',
          warn: '#f5a623',
          'warn-dim': 'rgba(245,166,35,0.12)',
          error: '#ef4056',
          'error-dim': 'rgba(239,64,86,0.12)',
          info: '#4c9aff',
          'info-dim': 'rgba(76,154,255,0.10)',
        }
      },
      fontFamily: {
        sans: ['DM Sans', 'sans-serif'],
        mono: ['JetBrains Mono', 'monospace'],
      }
    }
  }
}
```

### 6.4 UI Views — Phase Mapping

| View | Route | Phase 1 Data | Phase 2 Data |
|------|-------|-------------|-------------|
| Overview | `/` | Summary cards, CPU/Mem usage bars, events, workload health, namespace workloads | Network chart, Resource Utilization 6h |
| Pods | `/pods` | Full table: name, ns, status, CPU, mem, restarts, age | Container-level breakdown, network per pod |
| Nodes | `/nodes` | Node cards: CPU, mem, pod count, kubelet version | Disk I/O, network per node |
| Deployments | `/deployments` | Full table: ready, up-to-date, CPU, mem, age | Historical trend sparklines |
| StatefulSets | `/statefulsets` | Full table | — |
| DaemonSets | `/daemonsets` | Full table | — |
| Jobs | `/jobs` | Full table: completions, duration, status | — |
| CronJobs | `/cronjobs` | Full table: schedule, last run | — |
| Services | `/services` | Full table: type, IP, ports, endpoints | Traffic flow metrics |
| Ingresses | `/ingresses` | Full table: hosts, paths, backends, TLS | Request rate, latency |
| Gateways | `/gateways` | Full table: class, address, listeners, status | — |
| HTTPRoutes | `/httproutes` | Full table: hostnames, gateway, backends | — |
| Endpoints | `/endpoints` | Full table: addresses, ports | — |
| PVCs | `/pvcs` | Full table: status, volume, capacity, class | Disk usage % |
| PVs | `/pvs` | Full table: capacity, access, reclaim, claim | — |
| StorageClasses | `/storageclasses` | Full table: provisioner, reclaim, binding | — |
| ConfigMaps | `/configmaps` | Full table: keys, namespace, age | — |
| Secrets | `/secrets` | Full table: type, keys (not values), age | — |
| HPAs | `/hpas` | Full table: targets, min/max, current | Historical scaling events |
| Namespaces | `/namespaces` | Cards: resource counts, status | Resource usage per namespace |
| Events | `/events` | Full timeline with type filters | — |
| RBAC | `/rbac` | Roles, bindings | — |
| Settings | `/settings` | Cluster info, agent install CTA | Cluster management (add/remove/rename), agent status, config |
| Cluster Map | `/map` | Full topology with connections | Traffic flow animation |

---

## 7. Data Models

### 7.1 Go Types (Backend)

```go
package models

import "time"

// ═══ Cluster Overview ═══

type ClusterOverview struct {
    ClusterName        string              `json:"clusterName"`
    KubernetesVersion  string              `json:"kubernetesVersion"`
    Platform           string              `json:"platform"`
    Nodes              ResourceCount       `json:"nodes"`
    Pods               ResourceCount       `json:"pods"`
    Namespaces         ResourceCount       `json:"namespaces"`
    Services           ResourceCount       `json:"services"`
    Deployments        ResourceCount       `json:"deployments"`
    StatefulSets       ResourceCount       `json:"statefulSets"`
    DaemonSets         ResourceCount       `json:"daemonSets"`
    Jobs               ResourceCount       `json:"jobs"`
    CPU                ResourceUsage       `json:"cpu"`
    Memory             ResourceUsage       `json:"memory"`
    Health             ClusterHealth       `json:"health"`
    Events             []KubeEvent         `json:"events"`
    NamespaceWorkloads []NamespaceWorkload `json:"namespaceWorkloads"`
}

type ResourceCount struct {
    Total    int `json:"total"`
    Ready    int `json:"ready"`
    NotReady int `json:"notReady"`
    Warning  int `json:"warning"`
}

type ResourceUsage struct {
    Used             int64   `json:"used"`
    Requested        int64   `json:"requested"`
    Limit            int64   `json:"limit"`
    Allocatable      int64   `json:"allocatable"`
    PercentUsed      float64 `json:"percentUsed"`
    PercentRequested float64 `json:"percentRequested"`
}

// ═══ Health ═══

type ClusterHealth struct {
    Status   string         `json:"status"` // healthy, warning, critical
    Score    int            `json:"score"`
    Insights InsightCount   `json:"insights"`
    Checks   []HealthCheck  `json:"checks"`
}

type HealthCheck struct {
    Name    string `json:"name"`    // nodes, api-server, metrics, pods
    Status  string `json:"status"`  // pass, warn, fail
    Message string `json:"message"`
}

type InsightCount struct {
    Critical int `json:"critical"`
    Warning  int `json:"warning"`
    Info     int `json:"info"`
}

// ═══ Events & Workloads (embedded in Overview) ═══

type KubeEvent struct {
    Type      string `json:"type"`      // Normal, Warning
    Reason    string `json:"reason"`
    Message   string `json:"message"`
    Object    string `json:"object"`
    Namespace string `json:"namespace"`
    Timestamp string `json:"timestamp"`
    Count     int32  `json:"count"`
}

type NamespaceWorkload struct {
    Namespace string            `json:"namespace"`
    Workloads []WorkloadSummary `json:"workloads"`
}

type WorkloadSummary struct {
    Name          string        `json:"name"`
    Kind          string        `json:"kind"`
    Namespace     string        `json:"namespace"`
    Replicas      int32         `json:"replicas"`
    ReadyReplicas int32         `json:"readyReplicas"`
    Status        string        `json:"status"`
    CPU           ResourceUsage `json:"cpu"`
    Memory        ResourceUsage `json:"memory"`
    Pods          []PodSummary  `json:"pods"`
}

type PodSummary struct {
    Name   string `json:"name"`
    Status string `json:"status"`
    Ready  bool   `json:"ready"`
}

// ═══ Metrics ═══

type MetricPoint struct {
    Timestamp time.Time `json:"timestamp"`
    Resource  string    `json:"resource"`
    CPUUsage  int64     `json:"cpuUsage"`  // millicores
    MemUsage  int64     `json:"memUsage"`  // bytes
    CPULimit  int64     `json:"cpuLimit,omitempty"`
    MemLimit  int64     `json:"memLimit,omitempty"`
}

// ═══ Insights ═══

type Insight struct {
    ID         string     `json:"id"`
    Severity   string     `json:"severity"`   // critical, warning, info
    Category   string     `json:"category"`
    Resource   string     `json:"resource"`
    Namespace  string     `json:"namespace"`
    Title      string     `json:"title"`
    Message    string     `json:"message"`
    Suggestion string     `json:"suggestion"`
    FirstSeen  time.Time  `json:"firstSeen"`
    LastSeen   time.Time  `json:"lastSeen"`
    Resolved   bool       `json:"resolved"`
    ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

// ═══ Topology (Cluster Map) ═══

type Topology struct {
    Nodes []TopologyNode `json:"nodes"`
    Edges []TopologyEdge `json:"edges"`
}

type TopologyNode struct {
    ID        string            `json:"id"`
    Type      string            `json:"type"`
    Name      string            `json:"name"`
    Label     string            `json:"label"`
    Namespace string            `json:"namespace"`
    Status    string            `json:"status"`
    Kind      string            `json:"kind"`
    Metrics   *ResourceMetrics  `json:"metrics,omitempty"`
    Metadata  map[string]string `json:"metadata,omitempty"`
    CPU       *ResourceUsage    `json:"cpu,omitempty"`
    Memory    *ResourceUsage    `json:"memory,omitempty"`
    Pods      []PodSummary      `json:"pods,omitempty"`
}

type TopologyEdge struct {
    ID       string `json:"id"`
    Source   string `json:"source"`
    Target   string `json:"target"`
    Type     string `json:"type"`  // owns, selects, routes, hpa, bound, mounts, envFrom
    Label    string `json:"label,omitempty"`
    Animated bool   `json:"animated,omitempty"`
}

type ResourceMetrics struct {
    CPUPercent float64 `json:"cpuPercent"`
    MemPercent float64 `json:"memPercent"`
    PodCount   int     `json:"podCount"`
    PodReady   int     `json:"podReady"`
    Restarts   int     `json:"restarts"`
}

// ═══ Generic Resource List ═══

type ResourceList struct {
    Kind  string                   `json:"kind"`
    Items []map[string]interface{} `json:"items"`
    Total int                      `json:"total"`
}

// ═══ Cluster Info (Multi-cluster) ═══

type ClusterInfoResponse struct {
    Name    string `json:"name"`
    Context string `json:"context"`
    Server  string `json:"server"`
    Active  bool   `json:"active"`
}

// ═══ WebSocket ═══

type WSMessage struct {
    Type string      `json:"type"`
    Data interface{} `json:"data"`
}
```

### 7.2 TypeScript Types (Frontend)

Mirror the Go types exactly. Located at `apps/web/src/types/kubernetes.ts`.

---

## 8. Deployment

### 8.1 Self-hosted (Docker Compose)

```yaml
version: "3.8"
services:
  api:
    image: kubebolt/api:latest
    ports: ["8080:8080"]
    volumes:
      - ~/.kube/config:/root/.kube/config:ro
      - kubebolt-data:/data
    environment:
      KUBEBOLT_DB_PATH: /data/kubebolt.db
      KUBEBOLT_PORT: "8080"

  web:
    image: kubebolt/web:latest
    ports: ["3000:80"]
    environment:
      VITE_API_URL: http://localhost:8080

volumes:
  kubebolt-data:
```

### 8.2 Development

```bash
# Backend
cd apps/api
go run cmd/server/main.go --kubeconfig ~/.kube/config

# Frontend (separate terminal)
cd apps/web
npm install
npm run dev  # → http://localhost:5173
```

### 8.3 SaaS Deployment

- Backend: Go binary on Railway / Fly.io / AWS ECS
- Frontend: Vercel / Cloudflare Pages
- Database: Managed PostgreSQL + VictoriaMetrics
- Auth: JWT with refresh tokens

---

## 9. Security

**Principle of Least Privilege:**
- Phase 1: auto-detects kubeconfig permissions via SelfSubjectAccessReview; only starts informers for permitted resources. Works with cluster-admin, read-only ClusterRoles, or namespace-scoped RoleBindings.
- Phase 2: agent uses dedicated ServiceAccount with minimal ClusterRole
- KubeBolt NEVER reads Secret values, environment variable contents, or container filesystem data
- ConfigMap and Secret views show key names only, NOT values

**kubeconfig Handling:**
- Self-hosted: kubeconfig never leaves user infrastructure
- SaaS: encrypted at rest (AES-256-GCM), encrypted in transit (TLS 1.3)
- Users can revoke access by rotating kubeconfig credentials

**Network Security:**
- All API communication over TLS
- WebSocket connections over WSS
- Agent-to-backend uses mTLS (Phase 2)
- CORS configured to allow only the frontend origin

---

## 10. Development Roadmap

### Phase 1.0 — Core Platform (DONE)

Go backend (cluster manager, metrics collector, insights engine, REST API, WebSocket). React frontend with 23 views. Multi-cluster support. Gateway API support. Cluster Map with Grid/Flow layouts. Docker Compose self-hosted. RBAC permission detection with namespace-scoped SA support. Configurable refresh intervals. Sensitive value redaction in ConfigMap/Secret YAML. Tested on Docker Desktop + EKS.

### Phase 1.3 — Terminal & Cluster Actions (DONE)

All features implemented. Users can manage clusters entirely from KubeBolt without switching to `kubectl`.

| Feature | Impact | Status | Implementation |
|---------|--------|--------|----------------|
| **Pod Terminal** | Critical | Done | WebSocket-to-SPDY exec bridge + xterm.js. Auto-detects bash/sh. Multi-container + workload pod selector. Permission-aware with error countdown. |
| **Port Forwarding** | Critical | Done | SPDY port-forward with TCP listener on backend host. Topbar indicator with active forwards dropdown (Open/Stop). Per-port forward buttons in pod detail. **Limitation:** localhost only — remote requires Phase 3 subdomain proxy. |
| **Restart/Scale** | High | Done | Restart: PATCH `restartedAt` annotation (rollout restart) for Deployments/StatefulSets/DaemonSets. Scale: scale subresource for Deployments/StatefulSets. Popover confirmations with descriptions. |
| **Describe** | High | Done | `k8s.io/kubectl/pkg/describe` for exact `kubectl describe` output. Full-screen modal with syntax highlighting (keys, values, events). |
| **YAML Editing** | High | Done | CodeMirror 6 with One Dark theme + YAML language. Edit/Apply/Cancel workflow. Backend applies via dynamic client Update(). managedFields stripped from output. |
| **Delete Resources** | High | Done | Full confirmation modal: resource info, type-name-to-confirm input, force delete option (grace period 0). Navigates to list on success. |
| **Global Search** | High | Done | Cmd+K modal searching across 16 resource types. Results grouped by kind with icons. Keyboard navigation (↑↓ + Enter). Debounced, min 3 chars. Portal rendering for full-screen overlay. |

### Phase 1.4 — File Browser & History (DONE)

| Feature | Impact | Status | Implementation |
|---------|--------|--------|----------------|
| **Files Tab** | Medium | Done | Exec-based file browser (`ls -la` / `find` fallback). Directory navigation with breadcrumbs, file content viewer, download. Handles distroless containers gracefully. Permission denied shown as centered icon state. |
| **StatefulSet/DaemonSet History** | Medium | Done | ControllerRevision listing via clientset, sorted by revision. Deployments continue using ReplicaSet-based history with active/inactive differentiation. |
| **CronJob → Jobs** | Medium | Done | Child job listing via ownerReferences filtering, sorted newest first. Shows name, status, completions, duration, age. |
| **Export/Copy YAML** | Medium | Done | Copy to clipboard with "Copied!" feedback. Download as `.yaml` file. Buttons alongside Edit in YAML tab. |

### Phase 1.5 — Distribution & Community

Priority: critical for open source adoption.

| Feature | Impact | Status | Implementation |
|---------|--------|--------|----------------|
| **Helm Chart** | Critical | Done | OCI-based Helm chart at `ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt`. Configurable values (images, resources, ingress, RBAC, ServiceAccount). ClusterRole with full KubeBolt permissions. In-cluster config auto-detection. |
| **Container Images** | High | Done | Multi-arch images (amd64/arm64) on ghcr.io. Native platform builds to avoid QEMU timeout. API uses Go cross-compilation, Web uses native Node.js build + multi-arch nginx runtime. |
| **GitHub Releases** | High | Done | Automated release workflow on `v*` tags. Categorized changelog (features, fixes, docs, performance). Docker pull instructions and install commands in release notes. |
| **User Documentation** | High | Done | README with feature comparison table, quick start guides (Helm, Docker Compose, local dev), architecture diagram, RBAC docs, tech stack, and performance metrics. |
| **Artifact Hub** | Medium | Done | `artifacthub-repo.yml` in repo root + annotations in Chart.yaml (license, links). Chart keywords expanded for discoverability. |
| **Helm NOTES.txt** | Medium | Done | Post-install instructions template: port-forward command for ClusterIP, ingress URL when enabled, pod verification command. |
| **Cloud-specific Guides** | Medium | Done | Dedicated guides at `docs/guides/` for EKS (IRSA, Pod Identity, ALB, Fargate), GKE (Workload Identity, GCE Ingress, Autopilot), and AKS (Azure AD Workload Identity, AGIC, Azure RBAC). |

### Phase 1.6 — Cluster Management, Notifications & Traffic Map

| Feature | Impact | Status | Description |
|---------|--------|--------|-------------|
| **Animated Cluster Map** | High | Done (Phase 1.0) | React Flow with animated traffic lines — moving dots on `selects`/`routes` edges, glow effect, error pulses, dashed config edges, color-coded by relationship type (`ConnectionEdge.tsx`). Uses topology graph data — no agent required. |
| **Cluster Management** | Medium | Done | Add/remove/rename clusters from UI. Upload kubeconfig via paste or file. Contexts persist in BoltDB, merged with kubeconfig file in memory — never modifies the user's file. Source badges ("Uploaded", "In-Cluster") and display name overrides. Admin-only mutations. |
| **Slack Notifications** | Medium | Done | Webhook integration with Block Kit formatting. Severity threshold filter, dedup by `(cluster, resource, title)` with cooldown. Deep links to affected resource. Admin-only config + test endpoint. |
| **Discord Notifications** | Medium | Done | Webhook integration with embeds, color-coded by severity. Same filtering/dedup/deep-link infrastructure as Slack. |
| **Email Notifications** | Low | Done | SMTP configuration with STARTTLS/implicit TLS. Multiple recipients. Three digest modes: instant (default), hourly, daily. Display-name support in From header. Severity-colored banners. Per-channel test endpoint. |
| **Global notification settings** | Low | Done | Master enabled toggle (`KUBEBOLT_NOTIFICATIONS_ENABLED`) as kill switch for maintenance windows. Base URL exposed in admin UI. Optional notifications on insight resolution (`KUBEBOLT_NOTIFICATIONS_INCLUDE_RESOLVED`) with a separate dedup key and `[Resolved]` title prefix. |

### Phase 1.7 — Authentication & Access Control

Priority: critical for production deployments where multiple users access KubeBolt.

**Design principle: Grafana-style local auth first.** Start with built-in username/password authentication and application-level roles. No external identity providers required. OAuth2/OIDC and Kubernetes Impersonation are deferred as optional enhancements — the goal is a secure, self-contained auth system that works out of the box with zero external dependencies.

#### Deployment scenarios

| Scenario | Auth | Kubernetes access | Users |
|----------|------|-------------------|-------|
| **Local / team** (`--kubeconfig`) | Admin user with configured password | Shared kubeconfig — all users see what the kubeconfig permits | Optional — works as single-user; additional users can be created |
| **In-cluster / production** (Helm) | Admin password via K8s Secret + additional users | KubeBolt ServiceAccount — all users share the SA's permissions | Multi-user with role-based access control |
| **Auth disabled** (`auth.enabled: false`) | No login required | Same as today — open access | Single implicit admin (current behavior preserved) |

In both authenticated scenarios, Kubernetes-level permissions depend on the kubeconfig or ServiceAccount — KubeBolt roles control **application-level** actions (who can edit, delete, manage users), not which K8s resources are visible.

#### Core features

| Feature | Impact | Description |
|---------|--------|-------------|
| **Built-in authentication** | Critical | Username + password login with bcrypt-hashed credentials. JWT access tokens (short-lived, in-memory) + httpOnly refresh token cookie. Login page with username/password form. Configurable session expiry. |
| **Default admin user** | Critical | On first boot, if no users exist, seed an `admin` user. Password from `KUBEBOLT_ADMIN_PASSWORD` env var (or Helm secret). If not configured, generate a random password and print it to stdout on startup (same pattern as Grafana). Email defaults to `admin@localhost`. |
| **Application roles** | Critical | Three roles with hierarchical permissions: **Viewer** (read-only — browse all resources, view logs, view YAML, use Copilot), **Editor** (Viewer + edit YAML, scale, restart, port-forward, exec terminal), **Admin** (Editor + delete resources, manage users, configure clusters/copilot settings). |
| **User management (Admin only)** | Critical | CRUD operations for users: create (username, email, password, role), list with search/filter, edit profile/role, reset password, delete. Table view with columns: Login, Email, Name, Role, Last active, Created. Inspired by Grafana's Administration > Users and access > Users page. |
| **Role enforcement middleware** | High | Backend middleware checks the authenticated user's role before executing mutating actions. Viewers get 403 on write endpoints (YAML apply, scale, restart, delete, user management). Editors get 403 on admin endpoints (user CRUD, cluster config). |
| **UI role adaptation** | High | Frontend adapts to the logged-in user's role: action buttons (Delete, Scale, Restart, YAML Edit/Apply) hidden or disabled for Viewers. User management section only visible to Admins. Current user's role shown in the user menu. |
| **User profile** | Medium | Logged-in users can change their own password and display name. Admins can change any user's password and role. |
| **Session management** | Medium | Token refresh flow, logout endpoint that invalidates refresh token. Optional: active sessions list for admins. |

#### Administration UI structure (Grafana-inspired)

Sidebar section under **Administration** (Admin role only):

```
Administration
├── Users and access
│   ├── Users              ← User list + CRUD (Phase 1.7)
│   ├── Teams              ← Team management (deferred)
│   ├── Service accounts   ← API tokens for automation (deferred)
│   └── Authentication     ← SSO provider config (deferred)
└── General
    └── Settings           ← App-level settings (deferred)
```

Only **Users** is implemented in the initial Phase 1.7 release. The remaining items are listed in the sidebar as "Coming soon" placeholders to establish the navigation structure.

#### Configuration (env vars)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `KUBEBOLT_AUTH_ENABLED` | No | `true` | Enable/disable authentication. When `false`, KubeBolt works as today with no login. |
| `KUBEBOLT_ADMIN_PASSWORD` | No | (auto-generated) | Initial admin password. If not set, a random 16-char password is generated and printed to stdout at first boot. |
| `KUBEBOLT_JWT_SECRET` | No | (auto-generated) | Secret for signing JWT tokens. Auto-generated if not set (persisted in the data store). Should be set explicitly in HA deployments. |
| `KUBEBOLT_JWT_EXPIRY` | No | `15m` | Access token expiry duration. |
| `KUBEBOLT_JWT_REFRESH_EXPIRY` | No | `7d` | Refresh token expiry duration. |
| `KUBEBOLT_DATA_DIR` | No | `./data` | Directory for the embedded database file. |

#### API endpoints

| Endpoint | Method | Auth | Role | Description |
|----------|--------|------|------|-------------|
| `/auth/login` | POST | No | — | Authenticate with username + password, returns JWT + sets refresh cookie |
| `/auth/refresh` | POST | Cookie | — | Refresh access token using httpOnly refresh cookie |
| `/auth/logout` | POST | Yes | — | Invalidate refresh token, clear cookie |
| `/auth/me` | GET | Yes | Any | Current user profile (username, email, name, role) |
| `/auth/me/password` | PUT | Yes | Any | Change own password (requires current password) |
| `/users` | GET | Yes | Admin | List all users with search/filter/pagination |
| `/users` | POST | Yes | Admin | Create new user (username, email, password, role) |
| `/users/:id` | GET | Yes | Admin | Get user details |
| `/users/:id` | PUT | Yes | Admin | Update user (name, email, role) |
| `/users/:id/password` | PUT | Yes | Admin | Reset user's password (admin override, no current password required) |
| `/users/:id` | DELETE | Yes | Admin | Delete user (cannot delete self) |

All existing resource endpoints remain under their current paths. The `requireConnector` middleware is unchanged. A new `requireAuth` middleware wraps all routes when auth is enabled, and a `requireRole(minRole)` middleware gates write/admin actions.

#### Implementation components

**Backend (Go):**
- `internal/auth/store.go` — User store backed by embedded SQLite (`modernc.org/sqlite`, pure Go, no CGO). Schema: `users` table (id, username, email, name, password_hash, role, created_at, updated_at, last_login). Migrations on startup.
- `internal/auth/service.go` — Auth service: `Login`, `Register`, `ChangePassword`, `ResetPassword`. Bcrypt hashing with cost 12. Admin seed logic on first boot.
- `internal/auth/jwt.go` — JWT token generation/validation. Access token (short-lived, in Authorization header) + refresh token (long-lived, httpOnly cookie). Claims: `sub` (user ID), `username`, `role`, `exp`.
- `internal/auth/middleware.go` — Chi middleware: `RequireAuth` (validates JWT, injects user into context), `RequireRole(role)` (checks minimum role level: Viewer < Editor < Admin).
- `internal/api/auth_handlers.go` — HTTP handlers for `/auth/*` endpoints (login, refresh, logout, me, change password).
- `internal/api/user_handlers.go` — HTTP handlers for `/users/*` CRUD endpoints (admin only).
- `internal/api/router.go` — Conditional middleware: if `auth.enabled`, wrap routes with `RequireAuth`; gate mutating endpoints with `RequireRole(Editor)`, admin endpoints with `RequireRole(Admin)`.
- `internal/config/auth.go` — Load auth config from env vars.
- `cmd/server/main.go` — Initialize auth store, run migrations, seed admin user, print generated password if applicable.

**Frontend (React):**
- `contexts/AuthContext.tsx` — Auth state: `user`, `isAuthenticated`, `login()`, `logout()`, `refreshToken()`. Stores JWT in memory (not localStorage). Auto-refresh before expiry.
- `components/auth/LoginPage.tsx` — Username + password form, error handling, redirect to previous page after login.
- `components/admin/UsersPage.tsx` — User list table (Login, Email, Name, Role, Last active, Created) with search, "New user" button. Edit user modal. Role badge (color-coded). Inspired by Grafana's user management UI.
- `components/admin/CreateUserModal.tsx` — Form: username, email, display name, password, role selector.
- `components/admin/AdminLayout.tsx` — Administration section layout with sidebar navigation (Users, Teams placeholder, Service accounts placeholder, Authentication placeholder).
- `components/shared/UserMenu.tsx` — Topbar user avatar/dropdown: display name, role badge, "Profile", "Change password", "Logout". Replaces the current anonymous header when auth is enabled.
- `services/auth.ts` — API client for auth endpoints. Axios/fetch interceptor to attach JWT and handle 401 → refresh flow.
- `hooks/useAuth.ts` — Hook wrapping AuthContext for convenience.
- `hooks/useRequireRole.ts` — Hook that returns whether the current user has at minimum a given role. Used to conditionally render action buttons.
- Route guards: redirect to `/login` when not authenticated. `/admin/*` routes guarded by Admin role.

**Helm chart:**
- `values.yaml` — `auth:` block with `enabled`, `adminPassword`, `existingSecret`, `jwtSecret`, `dataDir` (PVC for SQLite).
- `templates/auth-secret.yaml` — Secret for admin password and JWT secret.
- `templates/deployment.yaml` — Mount data volume for SQLite, inject `KUBEBOLT_AUTH_*` env vars.
- `templates/pvc.yaml` — Optional PersistentVolumeClaim for the data directory.

**Docker Compose:**
- `docker-compose.yml` — Volume mount for `./data` directory, pass `KUBEBOLT_AUTH_*` env vars.
- `.env.example` — Template with `KUBEBOLT_ADMIN_PASSWORD=` and `KUBEBOLT_AUTH_ENABLED=true`.

#### Role enforcement matrix

| Action | Viewer | Editor | Admin |
|--------|--------|--------|-------|
| View resources, logs, YAML, describe | ✅ | ✅ | ✅ |
| Use Copilot | ✅ | ✅ | ✅ |
| View topology, insights, events | ✅ | ✅ | ✅ |
| Global search | ✅ | ✅ | ✅ |
| Edit & apply YAML | ❌ | ✅ | ✅ |
| Scale deployments/statefulsets | ❌ | ✅ | ✅ |
| Restart workloads | ❌ | ✅ | ✅ |
| Port-forward | ❌ | ✅ | ✅ |
| Pod terminal (exec) | ❌ | ✅ | ✅ |
| Delete resources | ❌ | ❌ | ✅ |
| Manage users | ❌ | ❌ | ✅ |
| Switch clusters | ✅ | ✅ | ✅ |
| Configure clusters (add/remove) | ❌ | ❌ | ✅ |
| Configure Copilot settings | ❌ | ❌ | ✅ |

#### Deferred enhancements (within Phase 1.7)

These features extend the auth system but are not required for the initial release:

| Feature | Description |
|---------|-------------|
| **OAuth2/OIDC providers** | Login via GitHub, Google, Azure AD, Keycloak, any OIDC provider. Authentication page with provider cards (enabled/disabled status). Configured via env vars or admin UI. |
| **Kubernetes Impersonation** | After authentication, KubeBolt impersonates the user when calling the K8s API. Each user sees only what their K8s RBAC permits. The ServiceAccount becomes a ceiling, not the effective permission set. |
| **Teams** | Group users into teams. Assign roles at team level. Team members inherit the team's role (highest wins). |
| **Service accounts** | API tokens for automation/CI pipelines. CRUD with expiry, role assignment, last-used tracking. |
| **Organizations** | Multi-tenant isolation. Users belong to one or more orgs, each org has its own set of clusters and users. |
| **Audit log** | Record who accessed what: user, action, resource, timestamp, source IP. Searchable audit log page for Admins. Configurable retention. |
| **Per-user RBAC (K8s-backed)** | When Kubernetes Impersonation is active, UI adapts to the impersonated user's K8s permissions per-user instead of per-ServiceAccount. |

### Phase 1.8 — AI Copilot

Priority: high — adds an in-app AI assistant that combines deep Kubernetes expertise with real-time access to the user's cluster data via KubeBolt's REST API.

**Key principle: BYO API key.** KubeBolt is **not a managed AI service**. The administrator configures their own API key (Anthropic, OpenAI, or any custom provider) at install time via env vars. KubeBolt has no AI billing — users pay their LLM provider directly. If no key is configured, the copilot is disabled but the rest of KubeBolt works fully.

The complete skill specification, including system prompt, tool definitions, knowledge base, and integration guide, lives at `skills/kubebolt-copilot/`.

#### Features

| Feature | Impact | Description |
|---------|--------|-------------|
| **In-app chat panel** | Critical | Slide-out panel from the right side with FAB button. React component using existing theme tokens. Streaming responses via SSE. |
| **16 tool integrations** | Critical | LLM tools mapped to KubeBolt REST endpoints: cluster overview, list/detail/yaml/describe of 23 resource types, pod logs, workload pods/history, CronJob jobs, topology, insights, events, search, permissions, clusters, metrics. |
| **Multi-provider support** | High | Pluggable LLM providers: Anthropic Claude, OpenAI, custom (self-hosted Ollama, vLLM, etc.). Configurable per deployment. |
| **Backend proxy mode** | Critical | Production deployments route LLM requests through the Go backend so API keys never reach the browser. Streamed via Server-Sent Events. |
| **Browser-direct mode** | Medium | For local dev, the user can store their key in `localStorage` and call the LLM provider directly. |
| **Fallback model** | High | When the primary provider fails (rate limit, 5xx, network), the backend automatically retries with a configured fallback (different provider, cheaper model, or self-hosted endpoint). UI shows a "via fallback" badge. |
| **Tool calling loop** | Critical | Multi-step tool calling with max 10 rounds. Tool execution shown as collapsed indicators ("Checked cluster overview"). |
| **Context awareness** | High | Current cluster name and active page (`/deployments`, `/pods/ns/name`) injected into the system prompt for relevance. |
| **Markdown rendering** | High | Code blocks for kubectl commands, tables for resource lists, bold for key values, ResourceLink components for clickable navigation to KubeBolt views. |
| **Permission awareness** | High | Copilot recognizes 403 responses and adapts ("I can't see Secrets — your kubeconfig doesn't have access"). Same RBAC degradation philosophy as the rest of KubeBolt. |
| **Destructive command warnings** | High | When recommending `kubectl delete`, `--force`, `rollout undo`, etc., the copilot prefixes with ⚠️ warnings and suggests `--dry-run=server` or safer alternatives. |
| **Privacy guards** | Medium | Sensitive data in logs (API keys, tokens, DSNs) is redacted before being shown in chat. Warns the user if credentials are detected. |
| **Language matching** | Medium | Copilot responds in the same language the user writes in (Spanish, English, etc.). Technical terms stay in English. |
| **Settings UI** | Medium | When backend has a key configured: read-only display. When in browser mode: provider/model/key inputs with localStorage warning. |
| **Capability endpoint** | High | `GET /api/v1/copilot/config` returns enabled status, provider, model, fallback metadata (without API keys). Frontend uses this to show/hide the panel. |

#### Configuration (env vars)

| Variable | Required | Description |
|---|---|---|
| `KUBEBOLT_AI_PROVIDER` | Yes | `anthropic`, `openai`, or `custom` |
| `KUBEBOLT_AI_API_KEY` | Yes | User's API key (enables the copilot) |
| `KUBEBOLT_AI_MODEL` | No | Model name (defaults: `claude-sonnet-4-6` / `gpt-4o`) |
| `KUBEBOLT_AI_BASE_URL` | No | Custom endpoint for self-hosted or proxy |
| `KUBEBOLT_AI_MAX_TOKENS` | No | Max tokens per response (default 4096) |
| `KUBEBOLT_AI_FALLBACK_PROVIDER` | No | Fallback provider (defaults to primary) |
| `KUBEBOLT_AI_FALLBACK_API_KEY` | No | Fallback API key (enables fallback if set) |
| `KUBEBOLT_AI_FALLBACK_MODEL` | No | Fallback model name |
| `KUBEBOLT_AI_FALLBACK_BASE_URL` | No | Fallback custom endpoint |

The Helm chart exposes these via a `copilot:` block in `values.yaml` with support for `existingSecret` so the API key is managed via Kubernetes Secrets (Sealed Secrets, External Secrets, Vault, etc.) instead of inline values.

#### Implementation Components

**Backend (Go):**
- `internal/config/copilot.go` — Env var loader with primary + optional fallback
- `internal/copilot/` — New package: `provider.go` (interface), `anthropic.go`, `openai.go`, `errors.go` (with `isRecoverable`)
- `internal/api/copilot.go` — `HandleCopilotChat` (SSE + fallback logic) and `HandleCopilotConfig`
- `internal/api/router.go` — Route registration: `/api/v1/copilot/chat` and `/api/v1/copilot/config`
- `cmd/server/main.go` — Load `CopilotConfig` at startup

**Frontend (React):**
- `services/copilot/types.ts`, `tools.ts`, `providers.ts` — Tool definitions, dispatcher, provider adapters
- `contexts/CopilotContext.tsx` — State, sendMessage, tool calling loop
- `hooks/useCopilotConfig.ts` — Detect if copilot is enabled
- `components/copilot/` — `CopilotPanel`, `CopilotToggle`, `MessageList`, `MessageBubble`, `MessageInput`, `ToolCallIndicator`, `FallbackBadge`, `ResourceLink`
- `services/api.ts` — Add `/copilot/chat` (SSE) and `/copilot/config` wrappers
- Settings UI for browser mode (when no backend key)

**Helm chart:**
- `values.yaml` — `copilot:` block with primary + fallback config, `existingSecret` support
- `templates/deployment.yaml` — Conditional env var injection
- `templates/copilot-secret.yaml` — Inline secret for `apiKey` mode (not recommended for production)
- `README.md` — Document the copilot section

**Docker Compose:**
- `docker-compose.yml` — Pass through `KUBEBOLT_AI_*` from environment
- `.env.example` — Template with empty values

**Documentation:**
- `README.md` — AI Copilot section with screenshots
- `docs/guides/copilot.md` — How to obtain API keys for each provider, fallback recipes, troubleshooting
- `CLAUDE.md` — Architecture notes for the copilot

**Skill (already complete):**
- `skills/kubebolt-copilot/SKILL.md` — Role, response guidelines, error handling, safety, formatting, language matching
- `skills/kubebolt-copilot/references/api-tools.md` — All 16 tool definitions with schemas
- `skills/kubebolt-copilot/references/insights-rules.md` — All 12 KubeBolt insight rules
- `skills/kubebolt-copilot/references/kubernetes-knowledge.md` — Kubernetes knowledge base for general questions
- `skills/kubebolt-copilot/references/integration-guide.md` — Implementation guide for backend proxy, BYO key model, fallback behavior, Helm config
- `skills/kubebolt-copilot/references/examples.md` — 12 few-shot conversation examples

#### Tool efficiency & token optimization (additional)

After Phase 1.8 shipped, the backend was instrumented (see logging foundation) and real-world measurement revealed that tool results dominate token consumption in multi-round sessions — a single diagnostic query can accumulate 50–80K tokens when tool outputs are returned verbatim. This addendum tracks the optimization work that cuts context cost while preserving diagnostic value.

**Principle:** never silently hide information. Where filtering exists, the LLM opts in based on intent; where caps fire, the response carries explicit metadata so the LLM can request a narrower window. Token savings must not come from removing capability.

##### Shipped

| Improvement | Scope | Result |
|---|---|---|
| **Token accounting** ✅ | All providers + chat loop | Usage struct on ChatResponse; per-round and per-session totals logged; SSE `usage` event per round; per-tool `toolBreakdown` (calls/bytes/errors/durationMs) in session summary. Provider input/output/cache tokens match Anthropic and OpenAI billing to the token. Shipped in v1.5.0-rc.1. |
| **`get_pod_logs` optimization** ✅ | copilot/executor + connector | Optional `grep` (case-insensitive regex) + `since` duration window. Dual cap: 500 lines OR 48KB, tail-preserving (newest kept) with line-aligned truncation. Response carries `originalLines`, `returnedLines`, `truncated`, `bytesDropped` so the LLM knows what was cut. Shipped in v1.5.0-rc.1. |
| **Intent-aware system prompt** ✅ | copilot/prompt | Decision logic (read-intent vs diagnostic-intent) is language-agnostic — no per-language triggers. The LLM classifies intent in whatever language the user writes and picks `grep`/`since` accordingly. Shipped in v1.5.0-rc.1. |
| **GPT-5 / o-series compatibility** ✅ | copilot/openai | `max_completion_tokens` parameter switch for reasoning and GPT-5+ models; classic `max_tokens` preserved for gpt-4o and OpenAI-compatible endpoints (Azure, Ollama, LiteLLM, vLLM). Shipped in v1.5.0-rc.1. |
| **Anthropic prompt caching** ✅ | copilot/anthropic | System prompt + last tool definition marked with `cache_control: ephemeral`. Confirmed working on Sonnet 4.6 (`cacheReadTokens > 0` in round 2+). Haiku 4.5 requires ≥4,096 tokens per cacheable block so the current prefix (~3.5K combined) silently no-ops there — documented as a known limitation. Shipped in v1.5.0-rc.1. |
| **OpenAI automatic-cache reporting** ✅ | copilot/openai | Parse `prompt_tokens_details.cached_tokens` from OpenAI responses and normalize to the Anthropic convention (InputTokens = non-cached, CacheReadTokens = cached). Session summary logs now accurately reflect OpenAI's auto-caching (gpt-4o+, prompts ≥1024 tokens). Shipped post-RC. |
| **Scope guardrail** ✅ | copilot/prompt | Explicit in-scope / out-of-scope section in the system prompt. In-scope: Kubernetes, DevOps/SRE supporting cluster operations, KubeBolt product. Out-of-scope: general coding unrelated to cluster resources, non-technical topics, unrelated cloud products. The LLM refuses out-of-scope questions with a one-sentence polite redirect in the user's language — never answers partially. Shipped post-RC. |
| **Product knowledge base** ✅ | copilot/kubebolt_docs + tool + prompt | `get_kubebolt_docs` tool exposes ~25 hand-curated topics covering UI surfaces, admin pages, configuration. Topic list injected into the tool description so the LLM discovers valid keys without a round-trip; fuzzy matching on unknown keys. System prompt identity section lists main surfaces so the LLM answers trivial "what is X" without calling the tool. Shipped post-RC. |

##### Tool catalog — current state and proposed improvements

Each tool reviewed for typical output size and optimization opportunity. Status reflects what's shipped, what's proposed, and what doesn't need changes.

| Tool | Typical output | Status | Proposed change | Priority |
|---|---|---|---|---|
| `get_cluster_overview` | ~25KB (structural, constant) | Needs work | Add `detail=summary` default (~5–8KB: counts, top insights, aggregates) and `detail=full` opt-in (current behavior) | **High** |
| `get_topology` | Unbounded (entire graph) | Needs work | Require at least one of `namespace` or `focus=<kind/name>`; add `depth=1\|2\|3`, default 1 | **High** |
| `list_resources` | Up to ~37KB | Needs work | Add `fields=minimal` default (name/namespace/status/age) vs `fields=full` (current). Existing `limit` kept. | **High** |
| `get_events` | 100 items default, JSON-heavy | Needs work | Add `since` duration window + `fields=summary` default (reason/message/lastTimestamp/count) | Medium |
| `get_pod_logs` | Variable | ✅ Done | `grep`, `since`, dual cap, metadata — shipped in v1.5.0-rc.1 | — |
| `get_workload_pods` | ~11KB | Optional | `fields=minimal` default | Medium |
| `get_resource_detail` | Variable, usually small | No change | — | — |
| `get_resource_yaml` | Variable | No change | Secrets already redacted, managedFields already stripped | — |
| `get_resource_describe` | Variable, medium | No change | — | — |
| `get_workload_history` | Small-medium | No change | — | — |
| `get_cronjob_jobs` | Small-medium | No change | — | — |
| `get_insights` | ~7–8KB | No change | Existing `severity`/`resolved` filters adequate | — |
| `search_resources` | Small | No change | — | — |
| `get_permissions` | Small | Optional | Session-level cache (TTL 60s) — never changes mid-session | Low |
| `list_clusters` | Small | Optional | Session-level cache (TTL 60s) | Low |

##### Cross-cutting improvements

| Improvement | Rationale | Priority |
|---|---|---|
| **Anthropic prompt caching** ✅ | Shipped in v1.5.0-rc.1. System prompt + last tool definition marked with `cache_control: ephemeral`. See row above. | — |
| **OpenAI `cached_tokens` parsing** ✅ | Shipped post-RC. See row above. | — |
| **JSON-aware truncation** | Current `truncateToolResult` (32KB generic cap) chops bytes mid-structure producing broken JSON that the LLM then has to interpret. Replace with structure-aware truncation: for arrays, keep first N items + `{"_truncated": true, "omitted": N, "hint": "..."}`; for objects, drop heaviest array fields first. Applies to `list_resources`, `get_events`, `get_topology`, `get_workload_pods`. | High |
| **Response envelope normalization** | Standardize all tool responses as `{data, truncated, meta}` so the LLM always knows if it's looking at partial data and can inspect `meta` (count, durationMs, latencyMs) uniformly. | Medium |

##### Estimated combined impact

Baseline: a diagnostic session on a real production cluster consumed **79K tokens** with raw tool outputs. With shipped work alone (intent-aware `grep`), comparable sessions drop to **~53K** (−33%). Expected stacking with proposed work:

| Layer | Incremental saving | Cumulative |
|---|---|---|
| Baseline (no optimization) | — | 79K |
| + `get_pod_logs` intent-aware grep (shipped) | −27K | 52K |
| + Anthropic prompt caching | −12 to −15K | 37–40K |
| + Summary mode on aggregate tools | −8 to −12K | 25–32K |
| + JSON-aware truncation | quality, not tokens | — |

A ~60% reduction from baseline is achievable without changing the LLM, the query pattern, or the tool surface the user sees. All diagnostic capability remains intact — every optimization is opt-out (LLM can request `detail=full`, `fields=full`, broader `tailLines`, etc.) or invisible (caching).

##### Considered and deferred

- **Multi-pass default tool calling** (broad + focused on every query): gives back most of the savings without quality win. Better to let the LLM opt into `grep`/`since` based on intent.
- **Migration to OpenAI Responses API**: breaks the `openai` provider for Azure OpenAI, Ollama, LiteLLM, vLLM, and other OpenAI-compatible endpoints. Revisit only if a specific feature (server-side stateful sessions) becomes compelling for a concrete customer.
- **Backend-side AI summarization of tool outputs**: adds cost and hallucination risk, reduces determinism. The final LLM already summarizes what it returns to the user.

#### Contextual Copilot triggers (additional)

The Copilot panel is powerful but discoverable only through a toggle. Users at the point of decision — looking at a failing pod, reading an insight card, inspecting an event with 345K occurrences — should not have to (a) remember the Copilot exists, (b) open it, (c) formulate a question that re-describes context they're already seeing. This addendum adds contextual "Ask Copilot" entry points across the UI that launch the assistant with pre-loaded context.

Two benefits stack: adoption (the assistant meets the user where decisions happen) and token efficiency (pre-loaded context means fewer rounds spent gathering basic info, and the LLM arrives with everything it needs to answer in ronda 0).

##### Trigger placement (prioritized)

Ordered by ROI — which trigger delivers most value vs. UI clutter cost:

| Entry point | Canonical question | Priority | Status |
|---|---|---|---|
| **Insight card** (active insight in sidebar or insights page) | "Explain this insight and recommend a fix" | **High** — problem already detected, Copilot provides the next step | ✅ Shipped in v1.5.0-rc.1 |
| **Resource detail page** when the resource is in a not-ready state (pod CrashLoop, deployment with unavailable replicas, job failed, HPA pinned at max) | "Diagnose this resource and suggest fixes" | **High** — context is visible, user wants the why | ✅ Shipped in v1.5.0-rc.1 (pods/deployments/statefulsets) |
| **Event row** with Warning type and high count | "Explain this event and its impact" | Medium | Pending |
| **Service with 0 endpoints** | "Why is this service not routing traffic?" | Medium | Pending |
| **Node with Pressure condition** | "Analyze this node's health" | Low | Pending |

The two **High** entries shipped in the MVP. The three **Medium/Low** are queued for 1.5.0 stable as they complete the UX arc and reuse the same `AskCopilotButton` + `services/copilot/triggers.ts` infrastructure.

##### Interaction patterns

Three options with real trade-offs:

- **A. Click → launch directly** (single canonical prompt per trigger). Lowest friction, least control. Best for obvious cases.
- **B. Click → popover with 2–3 canned questions** ("Why?", "How do I fix it?", "Is this critical?"). Balance of control and friction. Best for complex cases.
- **C. Click → open panel with editable prefilled prompt**. Maximum control, maximum friction. Redundant with the existing manual chat.

Recommendation: **A for simple triggers** (event rows, single obvious question); **B for complex triggers** (insight cards, resource detail). **C is rejected** — if the user wants to rephrase, the existing chat already does that.

##### Technical approach

Builds entirely on existing primitives. `useCopilot()` already exposes `openPanel()` and `sendMessage(text)` — a trigger is two sequential calls.

New artifacts:

- `services/copilot/triggers.ts` — centralized prompt templates, one per trigger type, versioned. Easy to iterate without scattering strings across the UI.
- `components/copilot/AskCopilotButton.tsx` — small reusable button (icon + tooltip) with variants for each trigger type. Accepts a typed `TriggerPayload` and internally resolves the prompt.
- `CopilotContext` — extend `sendMessage` signature (or add `sendTriggeredMessage`) to accept an optional `trigger` metadata field that propagates to the backend session log.
- Backend: extend the `copilot session` log event with a `trigger` field (default `"manual"`, or one of the canonical trigger names).

Example prompt template for an insight trigger:

```text
Diagnose this insight in detail and recommend a fix.

Insight: {severity} — {title}
Resource: {namespace}/{kind}/{name}
Detected: {timestamp}
Message: {message}

What's the root cause, and what should I do?
```

Example for a not-ready pod:

```text
Investigate this pod and tell me what's wrong.

Pod: {namespace}/{name}
Status: {phase}
Containers: {containerStatuses}
Restart count: {restarts}

Explain what's happening and suggest actionable fixes.
```

Prompts are kept short deliberately — the LLM will call tools for more context when needed. Pre-loading the identifier and the symptom is enough for ronda 0.

##### Tracking

Every triggered session logs a `trigger` field in the `copilot session` event. Enables:

- **Adoption by entry point**: which triggers are used? If an entry point gets <5% of sessions, remove it — it's UI clutter earning nothing.
- **Token cost by trigger type**: different triggers will have different average consumption. Feeds the credit calibration doc (`internal/copilot-credits-pricing-calibration.md`) with per-activity data.
- **Conversion**: does a triggered session lead to follow-up questions from the same user (engagement) or is it a dead-end?

##### Guardrails

- If Copilot is not configured (`config.enabled === false`), all triggers must be **invisible**, not disabled. Dead-ends hurt trust.
- Trigger prompts use the active cluster and resource context — if the cluster is disconnected, the button is hidden or shows "Reconnect to ask".
- Trigger prompts never include secrets, raw YAML, or redacted fields. The backend tools the LLM then calls will re-fetch with proper permission scoping.
- Templates are versioned (`v1`, `v2`, …). Changing a template bumps the version — ensures the log retains prompt provenance for later tuning.

##### MVP scope (2 days)

1. Add `AskCopilotButton` component with icon + tooltip, styled to blend with existing insight cards and detail page headers.
2. Wire it into `InsightCard` and the header of `ResourceDetailPage` for pods/deployments/statefulsets in not-ready state.
3. Centralize prompts in `services/copilot/triggers.ts` with two templates (`insight`, `notReadyResource`).
4. Extend `CopilotContext.sendMessage` to accept optional `trigger` metadata.
5. Add `trigger` field to the backend SSE `usage` event and to the `copilot session` log.
6. Hide triggers when `config.enabled === false`.

Post-MVP increments (data-driven, only if used):

- Event row trigger.
- Service with 0 endpoints trigger.
- Node conditions trigger.
- Popover variant (interaction B) for insight triggers offering 2–3 canned questions.

#### Post-MVP shipped in 1.5.0 stable

- **Conversation memory management** ✅ — auto-compact at `KUBEBOLT_AI_SESSION_BUDGET_TOKENS × KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD` using the provider's cheap-tier model (Haiku 4.5 / gpt-4o-mini). Manual "new session with summary" button (scissors) folds the full transcript into a single summary. Backend emits the full `messages` array on `done` so the frontend persists tool history across turns, matching Anthropic/OpenAI's accumulative context model. Session counter in the panel shows the real input size reported by the provider (input + cache read + cache creation). Configured via `KUBEBOLT_AI_AUTO_COMPACT`, `KUBEBOLT_AI_SESSION_BUDGET_TOKENS`, `KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD`, `KUBEBOLT_AI_COMPACT_MODEL`, `KUBEBOLT_AI_COMPACT_PRESERVE_TURNS`.
- **Usage metrics & analytics** ✅ — every session persists to a BoltDB bucket (shared with auth) with a 30-day / 5000-entry retention cap. Admin endpoints (`/admin/copilot/usage/summary`, `/timeseries`, `/sessions`) power the `/admin/copilot-usage` page with token counts, cache hit rate, estimated USD cost (list-price table per provider/model), tool breakdown, compact events, and a per-session drill-down modal.
- **Product knowledge base** ✅ — `get_kubebolt_docs` tool returns terse product docs by topic (overview, navigation, cluster-map, resource-detail, pod-terminal, port-forward, insights, copilot, compact, admin-*, etc.). Fuzzy-matched keys. System prompt has a short identity section naming the main UI surfaces so the LLM doesn't call the tool for trivial facts.
- **Scope guardrail** ✅ — system prompt's "Scope" section defines in-scope (Kubernetes, DevOps/SRE supporting cluster ops, KubeBolt product) vs out-of-scope (general coding help, non-technical topics, unrelated cloud products). Explicit instruction to refuse with a one-sentence polite redirect in the user's language, never answer partially. Observed refusals in practice log as `outputTokens ≈ 40`.
- **Auto-clear on cluster switch** ✅ — Copilot transcript wipes on successful cluster switch so prior conversation doesn't mislead the LLM on resources in the new cluster.

#### Pending (post-1.5.0)

- Automated test suite (regression scenarios, mock cluster fixtures)
- WebSocket integration for proactive context (Phase 2 enhancement)
- JSON-aware truncation in tool results (finer control than byte cap)

### Phase 1.9 — Extended Distribution

Priority: high — lowers the barrier to adoption by offering multiple installation methods beyond Helm and Docker Compose.

**Key principle: Single binary foundation.** All distribution methods build on a single Go binary with embedded frontend assets (`embed.FS`). The binary serves both the API and the React UI from one HTTP server on one port.

The full specification is in `docs/kubebolt-distribution-spec.md`.

| Feature | Impact | Description |
|---------|--------|-------------|
| **Single Binary** | Critical | Statically-linked binary with embedded frontend. One command to install (`curl -fsSL https://get.kubebolt.dev \| sh`), one command to run (`kubebolt`). Cross-compiled for Linux/macOS/Windows (amd64/arm64). CLI flags: `--kubeconfig`, `--context`, `--port`, `--host`, `--open`. < 30MB compressed. Install script with checksum verification. |
| **Docker Single-Container** | High | Single image running the embedded binary. `docker run -p 3000:3000 -v ~/.kube:/root/.kube:ro ghcr.io/clm-cloud-solutions/kubebolt`. No nginx needed. Auto-detect Docker Desktop K8s (`kubernetes.docker.internal` rewrite). Multi-arch (amd64/arm64). |
| **Homebrew** | High | `brew install clm-cloud-solutions/tap/kubebolt`. Tap repository with formula pointing to GitHub Release binaries. Auto-updated on release via CI. |
| **kubectl Plugin (krew)** | Medium | `kubectl krew install kubebolt` → `kubectl kubebolt`. Same binary renamed to `kubectl-kubebolt`. Krew manifest with platform-specific archives. Submit to official krew-index. |
| **Kubernetes Operator** | Medium | `KubeBolt` CRD managed by a Kubebuilder operator. Declarative lifecycle: install, upgrade, config changes, self-healing. Manages Deployment, Service, RBAC, Ingress. Status tracking (Pending/Running/Upgrading/Failed). |
| **MCP Server Surface** | High | Expose KubeBolt's tool executor as a Model Context Protocol server (stdio + HTTP/SSE transports). Reuses the existing Copilot tool layer so external MCP clients (Claude Desktop, Cursor, VSCode, etc.) can operate the cluster through KubeBolt. Differentiator vs. generic K8s MCP servers: our tools carry insights, topology edges, and permission-awareness, not just CRUD. Supports `--read-only` and `--disable-destructive` flags for safe AI operation. |
| **Release Automation** | High | Extend CI to: build frontend → embed in binary → cross-compile 5 platforms → generate checksums → attach to GitHub Release → update Homebrew tap → package krew archives. |

#### Implementation order

```
Phase 1 (foundation):
  [1] Single Binary ← embed.FS + CLI flags + install script + release workflow

Phase 2 (quick wins — reuse the binary):
  [2] Docker Single-Container ← thin Dockerfile around the binary
  [3] Homebrew ← formula pointing to release assets
  [4] kubectl Plugin (krew) ← rename binary + krew manifest

Phase 3 (dedicated effort):
  [5] Kubernetes Operator ← Kubebuilder controller project
```

### Phase 2.0 — Agent, Historical Data & Network Observability

The MVP agent (shipped in five sprints, see §4.6) covers aggregate per-pod
telemetry. Connection-level flows (who talks to whom) are deliberately
deferred to Phase 2.1 where multiple data sources are integrated —
see "Traffic observability" below.

| Feature | Impact | Description |
|---------|--------|-------------|
| **kubebolt-agent DaemonSet** | Critical | Lightweight agent per node. Single static binary, <50MB RAM, <0.05 CPU. Install: `kubectl apply -f`. |
| **Network/Disk metrics** | High | Agent reads kubelet `/stats/summary` (primary) and `/metrics/cadvisor` (fallback collector for kubelets that don't populate the pod-level network block — seen on docker-desktop) for network I/O per pod, disk/filesystem per container. Aggregate volume only — no peer identity; connection-level data is Phase 2.1. |
| **gRPC streaming** | High | Agent streams metrics to backend every 15s. Reconnects automatically if backend unavailable. |
| **Historical TSDB** | High | VictoriaMetrics for time-series storage. Retention policies. Materialized rollups (1m, 5m, 1h). |
| **Container-level metrics** | Medium | Per-container CPU, memory, network — not just pod-level aggregates. |
| **Live traffic cluster map (structural)** | High | Cluster map edges derived from topology (ownerRefs, selectors, Gateway parentRefs, volumes) with per-edge volume aggregated from aggregate pod counters. Live flow edges (actual peer-to-peer) arrive in Phase 2.1. |
| **Node kubelet logs** | Medium | Stream kubelet and system logs from nodes via Kubernetes API proxy (`/api/v1/nodes/{name}/proxy/logs/...`). Enables debugging node-level issues from the UI without SSH access. Exposed as backend endpoint and Copilot/MCP tool. |
| **Node PSI stats** | Medium | Collect Pressure Stall Information (CPU / memory / IO pressure) via node `/stats/summary` endpoint. Feeds new insights rules (real memory pressure, IO contention) that Metrics Server alone cannot detect. |
| **Ephemeral debug pod** | Medium | Launch a short-lived debug pod from an image with optional port exposure and auto-cleanup on disconnect. Wraps `kubectl debug` / `kubectl run` semantics for UI and Copilot-driven troubleshooting (e.g. `busybox`, `nicolaka/netshoot`). |

### Phase 2.1 — Traffic Observability, Service Mesh & Advanced Observability

Aggregate per-pod network counters (Phase 2.0) tell you how much
bandwidth a pod consumes, not who it talks to. To draw real flow
edges on the cluster map — pod A → pod B with bytes, protocol,
latency, error rate — a different class of data is required.

The ladder of sources, ordered by accessibility (easier to deploy
first) and data quality (richer later):

| Level | Source | Requires | Data quality |
|-------|--------|----------|--------------|
| **0** | Pod aggregate counters (already in Phase 2.0) | Nothing beyond the MVP agent | Volume only, no peers |
| **1** | Service mesh metrics (Istio / Linkerd) via Prometheus | Mesh installed on the cluster | L7 — HTTP/gRPC methods, status codes, latency percentiles |
| **2** | Cilium Hubble API | Cilium CNI installed | L4 flows + optional L7 parsing for HTTP/DNS |
| **3** | Agent conntrack collector (opt-in privileged mode) | Agent with `hostNetwork: true` + `CAP_NET_ADMIN`; conntrack module loaded (default on most kernels) | L4 — src/dst IP:port + bytes. Universal fallback. |
| **4** | Agent eBPF collector (opt-in) | Kernel 5.8+ with BTF; `CAP_BPF` + `CAP_PERFMON` | L4 + L7 + connect/DNS latencies. Lowest overhead at scale. |

KubeBolt consumes all of these via source-specific adapters that feed
a unified `flows` schema (src_pod, dst_pod, protocol, bytes,
optional L7 fields). The cluster map and service detail views render
from this unified data regardless of which source provided it.

| Feature | Impact | Description |
|---------|--------|-------------|
| **Prometheus integration** | High | Remote write receiver for Prometheus compatibility. Read existing Prometheus data. |
| **Istio/Linkerd metrics adapter** (Level 1) | High | Read `istio_requests_total`, `istio_request_duration_milliseconds`, `envoy_cluster_upstream_*` etc. from Prometheus. Show HTTP status codes, gRPC codes, latency p50/p95/p99 on cluster map edges. Full Kiali-like traffic visualization. |
| **Cilium Hubble adapter** (Level 2) | High | When Cilium is detected on the cluster, query the Hubble relay API (gRPC) for flow records. Richest pod-level flow source without requiring a service mesh. Zero additional agent footprint; Cilium already captures this. |
| **Agent conntrack collector** (Level 3) | High | Opt-in privileged collector (`KUBEBOLT_AGENT_ENABLE_CONNTRACK=true`) that reads `/proc/net/nf_conntrack` and emits `pod_flow_*` metrics keyed by src/dst pod (resolved via the agent's pods cache). Documented trade-off: agent runs with `hostNetwork: true` + CAP_NET_ADMIN. Universal fallback when no mesh and no Cilium. |
| **Agent eBPF collector** (Level 4) | Medium | Deeper flow collection via eBPF programs attached to TCP connect/close and socket ops. CO-RE + BTF required; kernel 5.8+. Deliberate opt-in due to cross-kernel compatibility complexity. Target: <1% CPU at 10k flows/s/node. |
| **Unified flow schema** | Critical | Normalized `pod_flow_*` metrics written to VictoriaMetrics regardless of source. Adapter layer emits: `pod_flow_bytes_total`, `pod_flow_packets_total`, `pod_flow_requests_total` (L7 only), `pod_flow_latency_seconds_bucket` (L7 only). Source label identifies provenance (mesh / hubble / conntrack / ebpf). |
| **Live traffic cluster map** | High | Cluster map edges colored and sized by real observed flows. Line thickness = bytes/s or requests/s. Direction animated via moving dots. Color by health (green ok, yellow degraded, red errors >threshold). Protocol-aware styling when L7 data available: HTTP/S solid blue, gRPC solid green, TCP dashed gray, high error rate pulsing red. |
| **Istio config CRUD** | Medium | List, view, create, patch, and delete Istio configuration objects (VirtualServices, DestinationRules, Gateways, PeerAuthentications, etc.) from the UI and via Copilot/MCP tools. |
| **Distributed trace viewer** | Medium | List traces for a service (with error filtering) and drill into a single trace's call hierarchy. Data source: Tempo / Jaeger / OpenTelemetry collector via Prometheus or native query. |
| **Mesh health dashboard** | Medium | Mesh status page: control plane + data plane health, sidecar injection coverage, proxy version drift. |
| **Kiali parity checklist** | Medium | Functional parity with the 10 Kiali MCP tools: mesh traffic graph, mesh status, Istio config read, Istio config write, resource details, trace list, trace details, pod performance (current vs requests/limits), pod logs with mesh annotations, Istio metrics (latency/throughput). Tracked as acceptance criteria for this phase. |
| **Custom alert rules engine** | High | Threshold, anomaly, absence, composite rules on metrics/logs/events. |
| **AI-powered insights** | High | Anomaly detection baselines per workload. Incident correlation and root cause analysis. |
| **Cost analysis** | Medium | Right-sizing recommendations based on actual usage vs requests/limits. |

#### Delivery order within Phase 2.1

1. **Istio/Linkerd adapter first** — Prometheus is already our ingest path, so this is the cheapest source to integrate (weeks, not months) and covers the ~30-40% of clusters that run service mesh.
2. **Cilium Hubble adapter** — second because it's also zero-agent on the cluster side (Cilium ships it) and avoids the privilege step. Covers ~15-20% of clusters, growing.
3. **Unified flow schema + cluster map rendering** — pulls 1 and 2 together; the cluster map feature is only useful once at least one source is landing data.
4. **Agent conntrack collector** — universal fallback for clusters without mesh or Cilium. Opt-in privilege; documented trade-off. Implemented as an extension to the existing agent, see `internal/kubebolt-agent-technical-spec.md` §20.
5. **Agent eBPF collector** — last, once the value of flows is proven and the cross-kernel complexity is justifiable. Same opt-in pattern.

#### MVP state and open follow-ups

What landed in the first Hubble-adapter cut (see commits `feat(flows)` and
`feat(map): render live traffic edges`) versus the full Phase 2.1 vision:

| Area | MVP state | Follow-up work |
|------|-----------|----------------|
| **Metric shape** | `pod_flow_events_total` — cumulative count of flow events per `(src_pod, dst_pod, verdict)`. Hubble is event-oriented and doesn't report bytes per flow natively. | Scrape Cilium's own Prometheus metrics (`cilium_forward_bytes_total`, etc.) to enrich edges with true byte rates. Emit `pod_flow_bytes_total` / `pod_flow_packets_total` alongside events. |
| **Event dedup** | Aggregator filters `IsReply=true` and keeps only `TrafficDirection=EGRESS` so a single HTTP round trip contributes one counter bump instead of four (request + reply × ingress + egress perspectives). | No pending work — current behavior is correct. Document in the adapter source. |
| **Source detection** | Manual env var: `KUBEBOLT_HUBBLE_RELAY_ADDR` + user-run `kubectl port-forward` when the backend is on the host. | Auto-detect the `hubble-relay` Service on cluster connect. When the backend is in-cluster, dial the Service DNS directly. When running on the host with a kubeconfig, open an in-process port-forward via client-go and use the local port automatically. Env var stays as an override for non-standard installs. |
| **L7 enrichment** | L4 only (`src_pod`, `dst_pod`, `verdict`). L7 events arrive over the same Hubble stream when a `CiliumNetworkPolicy` with HTTP parsing is applied, but the adapter ignores them today. | Emit `pod_flow_requests_total{method, status}` and `pod_flow_latency_seconds_bucket` from L7 events. Cluster map edges light up with HTTP status color (2xx ok, 5xx red) and latency percentile. |
| **Intent flows** | Edges reflect physical pod-to-pod connections. Services never appear in the path because Cilium's socket-level LB rewrites the destination before the packet leaves the emitter's socket, so Hubble's L3/L4 view never sees the Service IP. | Optional "Show intent flows" overlay on the cluster map that routes edges caller → Service → callee using either: (a) DNS lookups observed by Hubble, (b) HTTP Host headers when L7 is enabled, or (c) Cilium service-tracking metadata pre-rewrite. |
| **Edge category filters** | Shipped in the cluster map: six per-group toggles (Ownership / Service / Config / Storage / Autoscale / Traffic) with localStorage persistence. | No pending work — UI primitive to reuse for other map-like views. |
| **Multi-cluster Hubble** | Adapter is single-cluster; collector goroutine is started once on backend boot and tied to whatever context the kubeconfig points at. | On cluster switch, stop the active adapter and restart against the new cluster's Hubble Relay (if present). Keep per-cluster aggregators so flow counters don't mix. |
| **Security (TLS / mTLS)** | Insecure gRPC to the relay; fine for port-forwarded dev and unencrypted in-cluster relays. | mTLS support for production Cilium installs that require it. Read the client cert / CA from a configurable Secret or the agent's existing mount. |

### Phase 2.2 — Kubernetes Ecosystem Breadth

Priority: high — closes the gap against ecosystem-focused tooling (notably `containers/kubernetes-mcp-server` and similar Red Hat-adjacent projects) by covering the Kubernetes-native stacks that appear in most production clusters. Without this phase, KubeBolt depth (insights, topology, permissions) is not matched by ecosystem breadth.

| Feature | Impact | Description |
|---------|--------|-------------|
| **Generic resources layer** | Critical | Accept arbitrary `apiVersion` / `kind` in list / get / create-or-update / delete / scale via the existing dynamic client. Unlocks every CRD in the cluster without code changes. Today the backend is bounded to the 22 probed resource types. |
| **CRD-aware detail views** | High | Detect installed CRDs at connection time, discover their schema (OpenAPI v3 from the CRD spec), and render generic detail pages (Overview / YAML / Describe / Events) without per-kind code. Custom resource instances become first-class citizens alongside built-ins. |
| **Helm integration** | High | Backend wrapper around the Helm Go SDK: `list` releases across namespaces, `install` from chart (repo, OCI, or local), `upgrade` with values diff, `rollback` to prior revision, `uninstall`, view computed values and release history. Exposed as UI pages, REST endpoints, Copilot tools, and MCP tools. |
| **OpenShift Routes & Projects** | Medium | First-class support for OpenShift-specific kinds: Routes (with TLS termination details and hostname), Projects (namespaces + project metadata). Detected at connection time when the OpenShift API group is present — no-op on vanilla clusters. |
| **Tekton Pipelines** | Medium | Manage Tekton CI/CD resources: list Pipelines / PipelineRuns / Tasks / TaskRuns, start a PipelineRun or TaskRun, restart with identical spec, stream TaskRun logs via child pod resolution. Gated on Tekton CRDs being present. |
| **`configuration_view` tool** | Low | Sanitized kubeconfig dump (no tokens, no secrets) exposed as a read-only Copilot/MCP tool so the LLM can reason about context/cluster/server URL without guessing. |

### Phase 2.3 — Extended Workload Types (opt-in)

Priority: medium-low — covers specialized workload kinds that matter for specific customer segments but not for the majority of clusters. Delivered behind feature flags so they do not bloat the default experience.

| Feature | Impact | Description |
|---------|--------|-------------|
| **KubeVirt VMs** | Medium | VirtualMachine lifecycle: list, view (VM + VMI), start / stop / restart, clone with a new name, create from instance type + storage class. Gated on KubeVirt CRDs. Unlocks virtualization-on-K8s segment. |
| **KCP workspaces** | Low | Multi-tenant control plane support: list and describe kcp Workspaces. Deferred until clear customer demand — kcp adoption is still narrow. |

### Phase 3.0 — SaaS Platform

Multi-tenant platform. OAuth2/SSO. Team collaboration. Custom dashboards. Billing. See `internal/ROADMAP-SAAS.md` for detailed planning.

---

## 11. Repository Structure

```
kubebolt/
├── apps/
│   ├── api/                      # Go backend
│   │   ├── cmd/server/main.go
│   │   ├── internal/
│   │   │   ├── cluster/          # K8s connection + multi-cluster manager + graph
│   │   │   ├── metrics/          # Metrics Server collector + cache
│   │   │   ├── insights/         # Rules engine
│   │   │   ├── api/              # REST router + handlers
│   │   │   ├── websocket/        # Real-time hub
│   │   │   ├── agent/            # Phase 2: gRPC receiver
│   │   │   ├── models/           # Shared types
│   │   │   └── config/           # Configuration
│   │   ├── go.mod
│   │   └── Dockerfile
│   └── web/                      # React frontend
│       ├── src/
│       │   ├── components/       # UI components organized by feature
│       │   ├── hooks/            # Data fetching hooks
│       │   ├── services/         # API + WebSocket clients
│       │   ├── types/            # TypeScript interfaces
│       │   └── utils/            # Formatters, helpers
│       ├── package.json
│       ├── tailwind.config.ts
│       └── Dockerfile
├── packages/
│   ├── agent/                    # Phase 2: Go DaemonSet agent
│   │   ├── cmd/agent/main.go
│   │   ├── internal/
│   │   │   ├── collector/        # kubelet/cAdvisor reader
│   │   │   └── sender/           # gRPC stream sender
│   │   └── Dockerfile
│   ├── proto/                    # gRPC protobuf definitions
│   │   └── agent.proto
│   └── shared/                   # Shared Go packages
├── deploy/
│   ├── docker-compose.yml        # Self-hosted deployment
│   ├── helm/kubebolt/            # Helm chart
│   └── agent/
│       └── kubebolt-agent.yaml   # Phase 2 agent manifest
├── docs/
│   ├── SPEC.md                   # This document
│   └── ui-prototype.html         # Interactive UI prototype
├── .github/workflows/
│   └── ci.yml
├── go.work
├── README.md
└── LICENSE
```

---

*End of specification. This document is the source of truth for KubeBolt implementation.*
