# KubeBolt API Tools Reference

Complete tool definitions for the KubeBolt Copilot. Each tool maps to a KubeBolt REST API endpoint.
The copilot's LLM uses these as function/tool definitions to fetch live cluster data.

## Tool Definitions

### get_cluster_overview

Fetches the full cluster summary including resource counts, CPU/memory usage, health score, recent events, and namespace workloads.

**Endpoint:** `GET /api/v1/cluster/overview`
**Parameters:** None
**Use when:** User asks about overall cluster state, health, resource counts, or general "what's happening"

**Response schema:**
```json
{
  "clusterName": "string",
  "kubernetesVersion": "string",
  "platform": "string",
  "nodes": { "total": 0, "ready": 0, "notReady": 0, "warning": 0 },
  "pods": { "total": 0, "ready": 0, "notReady": 0, "warning": 0 },
  "namespaces": { "total": 0 },
  "services": { "total": 0 },
  "deployments": { "total": 0, "ready": 0, "notReady": 0, "warning": 0 },
  "statefulSets": { "total": 0, "ready": 0, "notReady": 0 },
  "daemonSets": { "total": 0, "ready": 0, "notReady": 0 },
  "jobs": { "total": 0 },
  "cronJobs": { "total": 0 },
  "ingresses": { "total": 0 },
  "configMaps": { "total": 0 },
  "secrets": { "total": 0 },
  "pvcs": { "total": 0 },
  "pvs": { "total": 0 },
  "hpas": { "total": 0 },
  "cpu": {
    "used": 0, "requested": 0, "limit": 0, "allocatable": 0,
    "percentUsed": 0.0, "percentRequested": 0.0
  },
  "memory": {
    "used": 0, "requested": 0, "limit": 0, "allocatable": 0,
    "percentUsed": 0.0, "percentRequested": 0.0
  },
  "health": {
    "status": "healthy|warning|critical",
    "score": 0,
    "insights": { "critical": 0, "warning": 0, "info": 0 },
    "checks": [{ "name": "string", "status": "pass|warn|fail", "message": "string" }]
  },
  "events": [
    {
      "type": "Normal|Warning", "reason": "string", "message": "string",
      "object": "string", "namespace": "string", "timestamp": "string", "count": 0
    }
  ],
  "namespaceWorkloads": [
    {
      "namespace": "string",
      "workloads": [{
        "name": "string", "kind": "string", "namespace": "string",
        "replicas": 0, "readyReplicas": 0, "status": "string",
        "cpu": {}, "memory": {},
        "pods": [{ "name": "string", "status": "string", "ready": true }]
      }]
    }
  ],
  "permissions": { "pods": true, "deployments": true, "secrets": false }
}
```

---

### list_resources

Lists resources of any supported type with optional filtering, sorting, and pagination. Resource items include live metrics (CPU/memory) when available.

**Endpoint:** `GET /api/v1/resources/:type`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `type` | path | Resource type: `pods`, `deployments`, `statefulsets`, `daemonsets`, `jobs`, `cronjobs`, `services`, `ingresses`, `gateways`, `httproutes`, `endpoints`, `pvcs`, `pvs`, `storageclasses`, `configmaps`, `secrets`, `hpas`, `nodes`, `namespaces`, `events`, `roles`, `clusterroles`, `rolebindings`, `clusterrolebindings` |
| `namespace` | query | Filter by namespace |
| `search` | query | Search by name |
| `status` | query | Filter by status |
| `sort` | query | Sort field (default: `name`) |
| `order` | query | `asc` or `desc` |
| `page` | query | Page number for pagination (default: 1) |
| `limit` | query | Page size (default: 50) |

**Use when:** User asks about a specific resource type, wants to find resources, or needs a list view

**Response schema:**
```json
{
  "kind": "string",
  "items": [{}],
  "total": 0,
  "forbidden": false
}
```

Items for pods/deployments/nodes include `cpuUsage`, `memoryUsage`, `cpuPercent`, `memoryPercent` when Metrics Server is available.

**403 response:** When the user's kubeconfig lacks permission for this resource type, `forbidden: true` is returned.

---

### get_resource_detail

Fetches full detail of a specific resource, including live metrics injection.

**Endpoint:** `GET /api/v1/resources/:type/:namespace/:name`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `type` | path | Resource type |
| `namespace` | path | Namespace (use `_` for cluster-scoped resources like nodes) |
| `name` | path | Resource name |

**Use when:** User asks about a specific resource by name, or you need detail after finding it in a list

---

### get_resource_yaml

Fetches the raw YAML definition of a resource. Secret values are automatically redacted.

**Endpoint:** `GET /api/v1/resources/:type/:namespace/:name/yaml`
**Parameters:** Same as `get_resource_detail`
**Use when:** User wants to see the full spec, needs to check annotations/labels, or wants to understand configuration

---

### get_pod_logs

Fetches logs from a specific pod.

**Endpoint:** `GET /api/v1/resources/pods/:namespace/:name/logs`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `namespace` | path | Pod namespace |
| `name` | path | Pod name |
| `container` | query | Container name (required for multi-container pods) |
| `tailLines` | query | Number of lines from end (default: 100, options: 100, 500, 1000) |

**Use when:** User asks why a pod is crashing, wants to see errors, or needs log analysis

---

### get_workload_pods

Lists pods owned by a workload controller (deployment, statefulset, daemonset, or job).

**Endpoint:** `GET /api/v1/resources/:type/:namespace/:name/pods`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `type` | path | `deployments`, `statefulsets`, `daemonsets`, or `jobs` |
| `namespace` | path | Workload namespace |
| `name` | path | Workload name |

**Use when:** User asks about pods in a deployment, wants to see which pods are unhealthy in a workload

For deployments, pods are resolved through the ownerReference chain: Deployment → ReplicaSet → Pods.

---

### get_workload_history

Fetches revision history of a workload (Deployment, StatefulSet, or DaemonSet). Deployments use ReplicaSet-based history; StatefulSets and DaemonSets use ControllerRevisions.

**Endpoint:** `GET /api/v1/resources/:type/:namespace/:name/history`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `type` | path | `deployments`, `statefulsets`, or `daemonsets` |
| `namespace` | path | Workload namespace |
| `name` | path | Workload name |

**Use when:** User asks about rollout history, recent changes, or wants to investigate when something changed

---

### get_topology

Fetches the full cluster topology graph with nodes and edges representing resource relationships.

**Endpoint:** `GET /api/v1/topology`
**Parameters:** None

**Use when:** User asks about how resources are connected, what services route to, or dependency chains

**Response schema:**
```json
{
  "nodes": [{
    "id": "string", "type": "string", "name": "string",
    "namespace": "string", "status": "string", "kind": "string",
    "metrics": { "cpuPercent": 0, "memPercent": 0, "podCount": 0, "restarts": 0 }
  }],
  "edges": [{
    "id": "string", "source": "string", "target": "string",
    "type": "owns|selects|routes|hpa|bound|mounts|envFrom|imagePull|uses",
    "label": "string"
  }]
}
```

**Edge types explained:**
- `owns` — ownerReference (Deployment→ReplicaSet→Pod, Job→Pod, etc.)
- `selects` — label selector (Service→Pods)
- `routes` — traffic routing (Ingress→Service, HTTPRoute→Service, Gateway→HTTPRoute)
- `hpa` — autoscaler target (HPA→Deployment/StatefulSet)
- `bound` — storage binding (PVC→PV)
- `uses` — volume usage (Pod→PVC)
- `mounts` — volume mount (Pod→ConfigMap, Pod→Secret)
- `envFrom` — environment injection (Pod→ConfigMap, Pod→Secret)
- `imagePull` — image pull secret reference (Pod→Secret)

---

### get_insights

Fetches active insights — issues detected by KubeBolt's rules engine with severity and recommendations.

**Endpoint:** `GET /api/v1/insights`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `severity` | query | Filter: `critical`, `warning`, `info` |
| `resolved` | query | `true` to include resolved insights (default: only active) |

**Use when:** User asks what's wrong, wants recommendations, or asks about a specific issue type

**Response schema:**
```json
{
  "items": [{
    "id": "string",
    "severity": "critical|warning|info",
    "category": "string",
    "resource": "string",
    "namespace": "string",
    "title": "string",
    "message": "string",
    "suggestion": "string",
    "firstSeen": "timestamp",
    "lastSeen": "timestamp",
    "resolved": false,
    "resolvedAt": "timestamp|null"
  }],
  "total": 0
}
```

---

### get_events

Fetches Kubernetes events with optional filtering.

**Endpoint:** `GET /api/v1/events`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `type` | query | `Normal` or `Warning` |
| `namespace` | query | Filter by namespace |
| `involvedKind` | query | Filter by involved object kind |
| `involvedName` | query | Filter by involved object name |
| `limit` | query | Max results (default: 100) |

**Use when:** User asks about recent events, warnings, or wants event context for a specific resource

---

### get_permissions

Fetches the RBAC permissions detected for the current kubeconfig connection.

**Endpoint:** `GET /api/v1/cluster/permissions`
**Parameters:** None

**Use when:** User asks why they can't see certain resources, or you get a 403 and need to explain

**Response schema:**
```json
{
  "pods": { "canList": true, "canWatch": true, "canGet": true, "namespaceScoped": false },
  "secrets": { "canList": false, "canWatch": false, "canGet": false },
  ...
}
```

---

### list_clusters

Lists all available kubeconfig contexts.

**Endpoint:** `GET /api/v1/clusters`
**Parameters:** None

**Use when:** User asks which clusters are available or wants to know about their multi-cluster setup

**Response schema:**
```json
[
  {
    "name": "string",
    "context": "string",
    "server": "string",
    "active": true,
    "status": "connected|disconnected|error",
    "error": "string"
  }
]
```

---

### get_resource_describe

Fetches the equivalent of `kubectl describe` output for any resource. Includes events, conditions, and detailed status — extremely useful for troubleshooting.

**Endpoint:** `GET /api/v1/resources/:type/:namespace/:name/describe`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `type` | path | Resource type |
| `namespace` | path | Namespace (use `_` for cluster-scoped) |
| `name` | path | Resource name |

**Use when:** User wants detailed info, needs to see events for a resource, or you need to investigate scheduling/condition issues. Returns plain text formatted like `kubectl describe`.

---

### search_resources

Global search across all resource types by name. Returns results grouped by kind. Useful when the user knows a name but not the resource type.

**Endpoint:** `GET /api/v1/search`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `q` | query | Search query (substring match against resource names) |

**Use when:** User asks "find resources matching X" or mentions a name without specifying the type.

**Response schema:**
```json
{
  "results": [{
    "name": "string",
    "namespace": "string",
    "kind": "string",
    "status": "string",
    "resourceType": "string"
  }]
}
```

---

### get_cronjob_jobs

Lists Job children of a CronJob. Useful to investigate CronJob execution history.

**Endpoint:** `GET /api/v1/resources/cronjobs/:namespace/:name/jobs`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `namespace` | path | CronJob namespace |
| `name` | path | CronJob name |

**Use when:** User asks why a CronJob is failing, wants to see recent runs, or needs Job execution history.

---

### get_metrics

Fetches detailed metrics (CPU/Memory usage, requests, limits) for a specific resource.

**Endpoint:** `GET /api/v1/metrics/:type/:namespace/:name`
**Parameters:**
| Param | Type | Description |
|-------|------|-------------|
| `type` | path | Resource type (`pods`, `deployments`, `nodes`, etc.) |
| `namespace` | path | Namespace (use `_` for cluster-scoped) |
| `name` | path | Resource name |

**Use when:** User asks for specific metrics on a resource, or you need precise CPU/memory numbers beyond the summary in `get_resource_detail`.
