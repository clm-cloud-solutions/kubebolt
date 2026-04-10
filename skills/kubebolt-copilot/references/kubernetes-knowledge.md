# Kubernetes Knowledge Base

Comprehensive Kubernetes reference for the KubeBolt Copilot. Organized by topic for quick lookup
when answering user questions.

## Table of Contents

1. [Workload Troubleshooting](#workload-troubleshooting)
2. [Pod Lifecycle & States](#pod-lifecycle--states)
3. [Resource Management](#resource-management)
4. [Networking](#networking)
5. [Storage](#storage)
6. [RBAC & Security](#rbac--security)
7. [Scheduling](#scheduling)
8. [Gateway API](#gateway-api)
9. [Autoscaling](#autoscaling)
10. [Common Failure Patterns](#common-failure-patterns)

---

## Workload Troubleshooting

### Pod Not Starting Checklist

When a pod is stuck in a non-Running state, work through this decision tree:

**Pending** → Scheduling issue
- Insufficient resources: check node allocatable vs pod requests
- Node selector/affinity mismatch: check pod spec vs node labels
- Taints without tolerations: check node taints vs pod tolerations
- PVC not bound: check PVC status
- Too many pods: check `pods` resource quota

**ContainerCreating** → Image or volume issue
- Image pulling: check imagePullPolicy and image name
- Volume mount: check PVC/ConfigMap/Secret exists
- Init containers: check init container status

**CrashLoopBackOff** → Application error
- Check logs with `--previous` flag for crashed container
- Check exit code: 137 = OOM, 1 = app error, 126 = permission, 127 = command not found
- Verify entrypoint and command

**ImagePullBackOff** → Registry/credential issue
- Verify image name and tag exist
- Check imagePullSecrets
- Test registry connectivity

**Error** → Runtime failure
- Check pod events for specific error messages
- Check container status reason

### Deployment Issues

**Rollout stuck (progressing but not completing):**
- New pods failing readiness probe → check probe configuration
- New pods crash-looping → check logs of new revision
- Insufficient resources for new pods → check cluster capacity
- Rollback: `kubectl rollout undo deployment/<name>`

**Deployment at 0 replicas (unintentional):**
- Check if scaled to 0 by HPA (minReplicas=0 with KEDA)
- Check if `spec.replicas` was set to 0 accidentally
- Check for deployment controller issues in kube-controller-manager

### StatefulSet Specifics
- Pods are created sequentially (0, 1, 2...) — if pod 0 is stuck, all others wait
- Each pod gets a stable hostname and persistent storage
- Deleting a pod preserves its PVC — data persists across restarts
- `volumeClaimTemplates` create PVCs automatically

### DaemonSet Specifics
- One pod per node (or per selected node)
- If a DaemonSet pod is missing on a node, check taints and tolerations
- DaemonSet pods bypass the scheduler — they're created directly on nodes
- `NotReady` nodes won't have DaemonSet pods scheduled

### Job Specifics
- `completions` = total successful runs needed
- `parallelism` = max concurrent pods
- `backoffLimit` = retries before marking as failed (default 6)
- `activeDeadlineSeconds` = timeout for the entire job
- Failed job pods are kept for debugging (check logs before cleanup)

---

## Pod Lifecycle & States

### Container States

| State | Meaning |
|-------|---------|
| Waiting | Not yet running — pulling image, creating container, init containers |
| Running | Container is executing |
| Terminated | Container finished (success or failure) |

### Pod Phases

| Phase | Meaning |
|-------|---------|
| Pending | Accepted but not yet running (scheduling, image pull, init containers) |
| Running | At least one container running |
| Succeeded | All containers terminated successfully (exit 0) |
| Failed | At least one container failed (non-zero exit) |
| Unknown | Pod state cannot be determined (usually lost node contact) |

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General application error |
| 126 | Command cannot be invoked (permission issue) |
| 127 | Command not found |
| 128+N | Terminated by signal N (e.g., 137 = 128+9 = SIGKILL = OOM) |
| 137 | OOMKilled (or manual SIGKILL) |
| 143 | SIGTERM (graceful shutdown) |

### Probes

| Probe | Purpose | Failure Effect |
|-------|---------|----------------|
| Startup | Wait for slow-starting apps | Kill + restart container |
| Liveness | Detect deadlocked apps | Kill + restart container |
| Readiness | Detect temp unavailability | Remove from Service endpoints |

**Common probe mistakes:**
- No `initialDelaySeconds` on liveness → kills pod during startup
- Liveness and readiness hitting the same endpoint → if app is slow, gets both killed AND removed
- TCP probe on a port that accepts connections even when unhealthy

---

## Resource Management

### CPU

- **Requests**: Guaranteed minimum. Used by scheduler for placement. 1 CPU = 1000m (millicores).
- **Limits**: Maximum allowed. Exceeding causes throttling (NOT killing). CFS quota enforcement.
- **Best practice**: Set requests to average usage, limits to 2-3x requests (or no limit for non-latency-sensitive).
- **Throttling detection**: High CPU throttling % in cAdvisor metrics (Phase 2).

### Memory

- **Requests**: Guaranteed minimum. Used by scheduler. Affects QoS class.
- **Limits**: Hard ceiling. Exceeding causes OOMKill (exit code 137).
- **Best practice**: Set requests close to actual usage, limits 1.25-1.5x requests.
- **Never set memory limit lower than request** — pod will be immediately OOMKilled.

### QoS Classes

| Class | Conditions | Eviction Priority |
|-------|-----------|-------------------|
| Guaranteed | All containers: requests == limits for both CPU and memory | Last to be evicted |
| Burstable | At least one container has request or limit set | Middle priority |
| BestEffort | No requests or limits set at all | First to be evicted |

### Resource Quotas
- Namespace-level limits on total resources (CPU, memory, object counts)
- If a pod exceeds quota, the create request is rejected (not the pod killed later)
- `LimitRange` sets defaults and min/max per container

---

## Networking

### Service Types

| Type | Behavior | Use Case |
|------|----------|----------|
| ClusterIP | Internal cluster IP only | Inter-service communication |
| NodePort | ClusterIP + port on every node (30000-32767) | External access without LB |
| LoadBalancer | NodePort + cloud LB provisioned | Production external access |
| ExternalName | CNAME alias to external DNS | Pointing to external services |
| Headless (ClusterIP: None) | No cluster IP, DNS returns pod IPs | StatefulSets, direct pod access |

### DNS Resolution
- Service: `<service>.<namespace>.svc.cluster.local`
- Pod: `<pod-ip-dashes>.<namespace>.pod.cluster.local`
- Headless StatefulSet: `<pod-name>.<service>.<namespace>.svc.cluster.local`

### EndpointSlices
- Replaced legacy Endpoints API (more scalable)
- One EndpointSlice per ~100 endpoints
- Show which pods back a Service and their readiness

### Ingress
- L7 load balancing (HTTP/HTTPS)
- Host and path-based routing
- TLS termination
- Requires an Ingress Controller (nginx, Traefik, ALB, etc.)

### Network Policies
- Namespace-level firewall rules
- Default: all traffic allowed (no policies = open)
- Adding any policy to a namespace switches to default-deny for the selected direction

---

## Storage

### PVC Lifecycle

```
PVC Created → Pending → Bound → In Use → Released
```

**Pending reasons:**
- No matching PV (size, access mode, storage class)
- Provisioner not working
- `WaitForFirstConsumer` and no pod scheduled yet

### Access Modes

| Mode | Abbreviation | Meaning |
|------|-------------|---------|
| ReadWriteOnce | RWO | Single node read-write |
| ReadOnlyMany | ROX | Multiple nodes read-only |
| ReadWriteMany | RWX | Multiple nodes read-write |
| ReadWriteOncePod | RWOP | Single pod read-write (K8s 1.27+) |

### Reclaim Policies

| Policy | Behavior |
|--------|----------|
| Retain | PV kept after PVC deleted (manual cleanup) |
| Delete | PV and underlying storage deleted |
| Recycle | Deprecated — basic `rm -rf` on the volume |

### StorageClass
- Defines how volumes are provisioned
- `provisioner`: which driver creates volumes (e.g., `ebs.csi.aws.com`)
- `volumeBindingMode`: `Immediate` (bind right away) vs `WaitForFirstConsumer` (wait for pod)
- `allowVolumeExpansion`: whether PVCs can be resized

---

## RBAC & Security

### RBAC Components

| Resource | Scope | Purpose |
|----------|-------|---------|
| Role | Namespace | Permissions within a namespace |
| ClusterRole | Cluster | Permissions cluster-wide or across namespaces |
| RoleBinding | Namespace | Grants a Role/ClusterRole to users within a namespace |
| ClusterRoleBinding | Cluster | Grants a ClusterRole to users cluster-wide |

### How KubeBolt Handles RBAC
- On connection, probes 22 resource types via SelfSubjectAccessReview
- Two-phase: cluster-wide first, then namespace-level fallback
- Informers only started for permitted resources
- Namespace-scoped SAs get per-namespace informer factories
- Frontend adapts: restricted resources dimmed, "Limited access" banner

### Common Permission Issues
- "Forbidden" on Secrets: many SAs have read access to everything except secrets
- Namespace-scoped SA can't list cluster-scoped resources (nodes, PVs, ClusterRoles)
- Metrics Server access may be restricted separately from regular API access

---

## Scheduling

### Why Pods Stay Pending

1. **Insufficient resources**: No node has enough allocatable CPU/memory
2. **Node affinity**: Pod requires labels no node has
3. **Taints**: All nodes tainted without matching tolerations
4. **Pod topology spread**: Can't satisfy spread constraints
5. **PVC binding**: `WaitForFirstConsumer` PVC in wrong zone
6. **Resource quota**: Namespace quota exceeded

### Node Selection Priority
1. Filter: remove nodes that can't run the pod (resources, taints, affinity)
2. Score: rank remaining nodes (spread, affinity preferences, resource balance)
3. Bind: assign pod to highest-scored node

### Taints and Tolerations
- Taints on nodes repel pods unless pods tolerate them
- Effects: `NoSchedule` (hard), `PreferNoSchedule` (soft), `NoExecute` (evict existing)
- Control plane nodes are tainted `NoSchedule` by default

---

## Gateway API

### Overview
Gateway API is the successor to Ingress, providing more expressive routing.

### Resources

| Resource | Purpose |
|----------|---------|
| GatewayClass | Defines which controller handles gateways (like IngressClass) |
| Gateway | Declares listeners (ports, protocols, TLS) on a load balancer |
| HTTPRoute | HTTP routing rules: host matching, path matching, backend refs |
| TCPRoute / TLSRoute / GRPCRoute | Protocol-specific routing |

### Relationships
- Gateway references a GatewayClass
- HTTPRoute references a Gateway via `parentRefs`
- HTTPRoute references Services via `backendRefs`

### In KubeBolt
- Discovered via dynamic client (unstructured API, no typed clients)
- Gateway API CRDs must be installed in the cluster
- 5-second timeout on CRD discovery — gracefully skipped if not installed
- Shown in both resource list views and the Cluster Map topology

---

## Autoscaling

### HPA (Horizontal Pod Autoscaler)

- Scales replicas based on metrics (CPU, memory, custom)
- `targetCPUUtilizationPercentage`: percentage of CPU request (not limit)
- Scaling happens in steps: evaluates every 15s, scales every ~1-2 min
- Cooldown: 5 min after scale-up, 5 min after scale-down (configurable in v2)

**HPA issues:**
- Maxed out (current == max): workload needs more capacity
- Flapping (scaling up/down rapidly): tighten stabilization window
- Not scaling: check if Metrics Server is running, check metric value vs target

### VPA (Vertical Pod Autoscaler)
- Adjusts resource requests and limits automatically
- Can't run simultaneously with HPA on the same metric
- Modes: Off (recommend only), Auto (apply), Initial (set on creation only)

---

## Common Failure Patterns

### Pattern: Cascading Failure
1. One pod OOMKilled → traffic shifts to remaining pods
2. Remaining pods overloaded → more OOMs or timeouts
3. HPA tries to scale but hits max → all pods degraded
**Fix:** Right-size resources, set PodDisruptionBudgets, don't set HPA max too low

### Pattern: Thundering Herd on Deploy
1. New deployment rolls out → old pods terminating, new pods starting
2. All new pods hit dependencies simultaneously (DB, cache, external APIs)
3. Dependencies overloaded → new pods fail readiness → rollout stalls
**Fix:** Stagger startup (readiness gates), implement connection pooling, set `maxSurge`/`maxUnavailable`

### Pattern: Node Pressure Cascade
1. Node runs low on disk/memory → evicts BestEffort pods
2. Evicted pods reschedule to other nodes → those nodes get pressured
3. Cascade continues until cluster stabilizes or runs out of capacity
**Fix:** Set proper resource requests (QoS: Guaranteed/Burstable), monitor node resources, add capacity buffer

### Pattern: DNS Resolution Failure
1. CoreDNS pods unhealthy or overloaded
2. Pods can't resolve service names → connection failures
3. All inter-service communication breaks
**Fix:** Monitor CoreDNS pod health, scale CoreDNS with cluster size, check `ndots` configuration

### Pattern: Certificate Expiry
1. TLS cert on Ingress/Gateway expires
2. External traffic gets cert errors → service appears down
3. Internal mTLS certs (Istio) expire → pod-to-pod communication fails
**Fix:** Use cert-manager with auto-renewal, monitor cert expiry dates, set alerts
