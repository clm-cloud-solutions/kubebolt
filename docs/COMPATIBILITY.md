# Compatibility Matrix

KubeBolt and `kubebolt-agent` ship as **independently versioned** OCI
artifacts (the agent has its own helm chart, image, and release tag).
They are coupled through a single contract: the **metric/label schema**
the agent emits and that the kubebolt backend's queries consume.

When the schema changes, both sides must move in lockstep — running an
agent older than what kubebolt expects (or kubebolt older than what
the agent ships) produces the same symptom: the dashboards render
empty, even though the agent is online and samples are reaching VictoriaMetrics.

> **TL;DR** — the rule is simple: keep them on the same generation.
> See the matrix below for which agent works with which kubebolt.

---

## Compatibility matrix

| KubeBolt | kubebolt-agent | Schema | Notes |
|---|---|---|---|
| **1.10.x → 2.1.x** | **1.0.x → 1.4.x** ✅ | v1.0 (Prom-canonical) | Current. 2.0.x and 2.1.0 change nothing on the wire — the agent channel, the metric names and the Falco/Prom ingest paths are unchanged, so any 1.x agent ≥ 1.0 pairs with KubeBolt 2.0. Both sides consume / emit the canonical schema. Agent 1.1–1.4 are reliability / cardinality / OpenCost-sourcing releases on the same schema (1.4.0 only widens the default `dropNetworkInterfaces` list); pair any 1.x agent ≥ 1.0 with any KubeBolt ≥ 1.10. |
| 1.10.x | 0.2.x ❌ | mismatch | Agent emits the legacy schema but kubebolt's queries look for canonical names → empty dashboards. Backend logs `WARN msg="agent below minimum version — legacy schema"` on registration. |
| 1.9.x and earlier | 1.0.x ❌ | mismatch | Agent emits canonical names but kubebolt's queries still look for legacy → empty dashboards. **Don't run this combination.** |
| 1.9.x and earlier | 0.2.x ✅ | legacy v0.x | Pre-canonical era. Both sides on the legacy schema. Supported as-is, but new features (right-sizing P95, network drops, external endpoints with FQDN) only land in the canonical pair. |
| 1.5.x | 0.1.x ✅ | early | Pre-3-tier-RBAC era. Stable but no agent-proxy, no Hubble flow visibility. |

Cells marked ❌ won't crash — both sides start, the agent registers,
samples flow into VM. The visible breakage is silent: the UI panels go
empty for the cluster that's mismatched.

---

## What changed in v1.10.0 / agent v1.0.0

The agent v1.0.0 release renames every metric and label to follow
**Prometheus convention K8s** — the de-facto schema of cAdvisor +
kube-state-metrics + node-exporter + Hubble, and the same shape the
Prometheus `remote_write` receiver ingests. There is
no dual emission — the agent ships only the canonical names.

Highlights of the rename (full table in
[`packages/agent/CHANGELOG.md`](../packages/agent/CHANGELOG.md)):

- Pod-scope labels: `pod_namespace`/`pod_name` → `namespace`/`pod`
- CPU usage: derived gauges (`*_usage_cores`) removed — UI uses
  `rate(*_seconds_total[Xm])`
- Network: `pod_network_*_bytes_total` →
  `container_network_*_bytes_total` (with `container=""` for
  pod-level rows)
- Memory: `container_memory_rss_bytes` → `container_memory_rss`;
  page faults collapse into `container_memory_failures_total`
  with `failure_type` label
- Volumes: PVCs only, named `kubelet_volume_stats_*`
- Hubble flow labels: `src_*`/`dst_*` → `source_*`/`destination_*`
  (matches Hubble exporter, Istio, Linkerd convention)
- New self-metrics: `kubebolt_agent_*` for agent observability

KubeBolt v1.10.0's UI queries (Cluster Map, Reliability tab,
Capacity, Right-Sizing, every Resource Detail Monitor tab,
Insights backend's metric paths) all consult these canonical names.

---

## Upgrade notes for 1.10.0

- 1.10.0 fixed multi-cluster agent registration: agents now honour the
  cluster hint they send instead of collapsing to `cluster_id="local"`.
  If you upgraded from a pre-1.10 backend, old `local/`-keyed agent
  records linger until the 24h auto-prune horizon, then disappear.
- A backend running in-cluster no longer lists its own cluster twice
  when an agent connects from that same cluster.

---

## Kubernetes & platforms

- **Kubernetes version:** 1.24+ for full support (EndpointSlice v1,
  CronJob v1). 1.20–1.23 work with degraded features — for example the
  `service-no-endpoints` insight needs `discovery.k8s.io/v1`
  EndpointSlices.
- **Distributions:** any conformant cluster, including
  Amazon EKS ([guide](guides/eks.md)), Google GKE ([guide](guides/gke.md)),
  Azure AKS ([guide](guides/aks.md)), Red Hat OpenShift
  ([guide](guides/openshift.md) — read it first, the default install needs
  workarounds there), k3s / k0s, kubeadm, Docker Desktop, Minikube and
  kind / k3d.
- **Metrics Server:** recommended, not required. Without it the live
  CPU/Memory bars show "no data"; everything else works, and historical
  CPU/Memory comes from the agent.
- **Upgrade order:** upgrade the backend (`kubebolt` chart) first, then
  the agents (`kubebolt-agent` chart) in every connected cluster.

See [`deployment-scenarios.md`](deployment-scenarios.md) for which
optional components (kube-state-metrics, node-exporter, Cilium/Hubble,
an existing Prometheus) unlock which features.

---

## Migration paths

### Greenfield install

Just install both at the latest matching version:

```bash
helm upgrade --install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
    --version 2.1.0 \
    -n kubebolt --create-namespace

helm upgrade --install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
    --version 1.4.0 \
    -n kubebolt-agent --create-namespace \
    --set backendUrl=<your-backend-grpc-host:9090>
```

### Existing install at v1.9.x or earlier + agent 0.2.x

Upgrade both in the same maintenance window. Order is operationally
forgiving (the WARN is logged, dashboards render empty, samples still
flow) but the canonical pattern is to roll the backend first since
the agent reconnects at registration:

```bash
# --reset-then-reuse-values (Helm 3.13+) merges the new chart defaults
# under your existing overrides; plain --reuse-values can fail to render
# when the chart gained value blocks since your last install.

# 1. Backend first
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
    --version 2.1.0 \
    -n kubebolt --reset-then-reuse-values

# 2. Agent next, in any cluster connected to it
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
    --version 1.4.0 \
    -n kubebolt-agent --reset-then-reuse-values

# 3. Verify the WARN is gone
kubectl logs -n kubebolt deployment/kubebolt-api --tail=200 | grep "agent below minimum"
# (no output expected; the WARN means the agent is still v0.x)
```

### Multi-cluster fleet

If the kubebolt backend serves multiple clusters via per-cluster
agents, **upgrade all the agents** before declaring the rollout
complete. A backend at 1.10 or later with a mix of v1.0 and v0.2 agents will
show empty dashboards for whichever clusters lag behind, and the
backend's log will carry one `WARN` per legacy agent on every
reconnect.

The backend doesn't reject legacy agents (samples still ingest into
VM, just under the wrong label set), so a partial rollout is
recoverable — finish the upgrade and the next refetch repopulates
the dashboards.

---

## Why the schema rename

Every other ingestion path — the Prometheus `remote_write` receiver,
the agent's vmagent scrape sidecar (kube-state-metrics, node-exporter)
and the agent's read-from-Prometheus mode — naturally lands on the
Prometheus convention. Aligning the agent with it means a single
canonical schema feeds every dashboard regardless of ingest path,
instead of one translation layer per source.

---

## Reading the WARN log

When a legacy agent connects to a 1.10+ backend you'll see this exact
shape in the API logs:

```json
{
  "level": "WARN",
  "msg": "agent below minimum version — legacy schema",
  "agent_id": "...",
  "cluster_id": "...",
  "agent_version": "0.2.2",
  "min_agent_version": "1.0.0",
  "hint": "upgrade kubebolt-agent helm chart to >=1.0.0; v0.x emits the legacy schema and dashboards will render empty"
}
```

Empty / unparseable `agent_version` is silent — not all clients set
it (test mocks, third-party forks). The check is **fail-soft**:
the agent still connects and ships samples, the WARN exists only to
tell the operator what's happening.

---

## Future-proofing

Major schema changes after v1.0 will:
1. Bump **agent major version** (v1 → v2).
2. Bump **kubebolt major version** in lockstep.
3. Update this matrix with the new compatibility window.
4. Document the rename + migration in both CHANGELOGs.

Minor / patch agent releases (v1.0.x, v1.1.x) stay backward-compatible
with the current kubebolt minor — additive metrics or label
additions only, never renames.

---

## Links

- [`packages/agent/CHANGELOG.md`](../packages/agent/CHANGELOG.md) — agent release history with full schema migration table
- [`docs/releases/v1.10.0.md`](releases/v1.10.0.md) — kubebolt 1.10.0 release notes
- [`docs/releases/v2.1.0.md`](releases/v2.1.0.md) — kubebolt 2.1.0 release notes
- [`docs/agent-scraping.md`](agent-scraping.md) — operator guide for the vmagent sidecar: quickstart, config reference, troubleshooting
- [`deploy/helm/kubebolt-agent/README.md`](../deploy/helm/kubebolt-agent/README.md) — agent chart: RBAC tiers, auth modes, metric footprint
