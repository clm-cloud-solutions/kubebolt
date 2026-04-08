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

## 4. Phase 2 — KubeBolt Agent

### 4.1 Agent Design

The `kubebolt-agent` is a lightweight DaemonSet written in Go that runs one pod per node.

**Requirements:**
- Single static binary, <20MB compressed image
- Resource consumption: <50MB RAM, <0.05 CPU per node
- Install: `kubectl apply -f https://get.kubebolt.dev/agent.yaml`
- Read-only access, no cluster-admin, no exec
- Metrics collected every 15 seconds (configurable via env var)
- Reconnects automatically if backend is unavailable (buffer up to 5min)

### 4.2 Metrics Collected

| Category | Metrics | Source | Granularity |
|----------|---------|--------|-------------|
| Network | rx_bytes, tx_bytes, rx_errors, tx_errors, rx_packets, tx_packets | cAdvisor `/stats/summary` | Per container |
| Disk | fs_usage_bytes, fs_capacity_bytes, fs_available_bytes, inode_usage | kubelet `/stats/summary` | Per volume, per node |
| CPU (detailed) | usage_core_nanoseconds, throttled_time, throttled_periods, nr_periods | cAdvisor `/stats/summary` | Per container |
| Memory (detailed) | working_set_bytes, rss_bytes, cache_bytes, swap_bytes, page_faults | cAdvisor `/stats/summary` | Per container |

### 4.3 Agent Communication

```protobuf
// proto/agent.proto
syntax = "proto3";
package kubebolt.agent.v1;

service AgentService {
  rpc StreamMetrics(stream NodeMetrics) returns (StreamAck);
}

message NodeMetrics {
  string node_name = 1;
  int64 timestamp = 2;
  repeated ContainerMetric containers = 3;
  NodeResourceMetric node_resources = 4;
}

message ContainerMetric {
  string pod_namespace = 1;
  string pod_name = 2;
  string container_name = 3;
  int64 cpu_usage_nanocores = 4;
  int64 memory_working_set_bytes = 5;
  int64 network_rx_bytes = 6;
  int64 network_tx_bytes = 7;
  int64 fs_usage_bytes = 8;
  int64 cpu_throttled_time_ns = 9;
}

message NodeResourceMetric {
  int64 cpu_usage_nanocores = 1;
  int64 memory_usage_bytes = 2;
  int64 fs_usage_bytes = 3;
  int64 fs_capacity_bytes = 4;
  int64 network_rx_bytes = 5;
  int64 network_tx_bytes = 6;
}

message StreamAck {
  bool ok = 1;
}
```

### 4.4 Agent RBAC

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubebolt-agent-reader
rules:
  - apiGroups: [""]
    resources: ["nodes/stats"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["nodes/proxy"]
    verbs: ["get"]
  # No write, no exec, no secret access
```

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
| **Artifact Hub** | Medium | Pending | Register the Helm chart on artifacthub.io for public discoverability. Add `artifacthub-repo.yml` metadata to the repository. |
| **Helm NOTES.txt** | Medium | Pending | Post-install instructions template showing `kubectl port-forward` command, ingress setup hints, and connection verification steps. |
| **Cloud-specific Guides** | Medium | Pending | Dedicated guides for EKS (IAM roles, IRSA), GKE (Workload Identity), and AKS (AAD integration) with their specific RBAC and authentication requirements. |

### Phase 1.6 — Animated Traffic Map & Settings

| Feature | Impact | Description |
|---------|--------|-------------|
| **Animated Cluster Map** | High | Enhance the existing Cluster Map (Flow layout) with animated traffic lines — moving dots along edges to visualize communication flow between resources. Line styles differentiated by relationship type (selector, ownerRef, parentRef, volume). Uses existing topology data — no agent or service mesh required. |
| **Cluster Management** | Medium | Add/remove/rename clusters from UI. Upload kubeconfig. Connection health status per cluster. |
| **Slack Notifications** | Medium | Webhook integration for insights alerts. Configurable severity threshold. Channel selector. |
| **Email Notifications** | Low | SMTP configuration. Digest mode (daily/hourly). Per-insight-type subscription. |

### Phase 2.0 — Agent, Historical Data & Network Observability

| Feature | Impact | Description |
|---------|--------|-------------|
| **kubebolt-agent DaemonSet** | Critical | Lightweight agent per node. Single static binary, <50MB RAM, <0.05 CPU. Install: `kubectl apply -f`. |
| **Network connection tracking** | High | Agent reads `/proc/net/tcp` or conntrack to capture pod-to-pod TCP connections with bytes transferred. No eBPF required — works on any kernel. Feeds real traffic data to the cluster map. |
| **Network/Disk metrics** | High | Agent reads cAdvisor/kubelet for network I/O (bytes in/out), disk/filesystem usage per container. |
| **gRPC streaming** | High | Agent streams metrics to backend every 15s. Reconnects automatically if backend unavailable. |
| **Historical TSDB** | High | VictoriaMetrics for time-series storage. Retention policies. Materialized rollups (1m, 5m, 1h). |
| **Live traffic cluster map** | High | Cluster map edges show real traffic volume (line thickness), direction (animated dots), and health (green/yellow/red based on error rate). Data from agent conntrack. |
| **Container-level metrics** | Medium | Per-container CPU, memory, network — not just pod-level aggregates. |

### Phase 2.1 — Service Mesh Integration & Advanced Observability

| Feature | Impact | Description |
|---------|--------|-------------|
| **Prometheus integration** | High | Remote write receiver for Prometheus compatibility. Read existing Prometheus data. |
| **Istio/Linkerd metrics** | High | If service mesh present, read `istio_requests_total`, `istio_request_duration_milliseconds` etc. from Prometheus. Show HTTP status codes, gRPC codes, latency p50/p95/p99 on cluster map edges. Full Kiali-like traffic visualization. |
| **Protocol-aware traffic lines** | High | Differentiate cluster map edges by protocol: HTTP/S (solid blue), gRPC (solid green), TCP (dashed gray), high error rate (pulsing red). Line thickness = request volume. |
| **Custom alert rules engine** | High | Threshold, anomaly, absence, composite rules on metrics/logs/events. |
| **AI-powered insights** | High | Anomaly detection baselines per workload. Incident correlation and root cause analysis. |
| **Cost analysis** | Medium | Right-sizing recommendations based on actual usage vs requests/limits. |

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
