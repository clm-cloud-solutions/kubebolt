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
| **1.10.x** | **1.0.x** ✅ | v1.0 (Prom-canonical) | Current. Both sides consume / emit the canonical schema. |
| 1.10.x | 0.2.x ❌ | mismatch | Agent emits the legacy schema but kubebolt's queries look for canonical names → empty dashboards. Backend logs `WARN msg="agent below minimum version — legacy schema"` on registration. |
| 1.9.x and earlier | 1.0.x ❌ | mismatch | Agent emits canonical names but kubebolt's queries still look for legacy → empty dashboards. **Don't run this combination.** |
| 1.9.x and earlier | 0.2.x ✅ | legacy v0.x | Pre-canonical era. Both sides on the legacy schema. Supported as-is, but new features (right-sizing P95, network drops, external endpoints with FQDN) only land in the canonical pair. |
| 1.5.x | 0.1.x ✅ | early | Pre-3-tier-RBAC era. Stable but no agent-proxy, no Hubble flow visibility. |

Cells marked ❌ won't crash — both sides start, the agent registers,
samples flow into VM. The visible breakage is silent: the UI panels go
empty for the cluster that's mismatched.

---

## What changed in v1.10.0 / agent v1.0.0 (Phase 1)

The agent v1.0.0 release renames every metric and label to follow
**Prometheus convention K8s** — the de-facto schema of cAdvisor +
kube-state-metrics + node-exporter + Hubble. Same shape that future
Prom remote_write and OTLP receivers will normalize toward. There is
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

## What's new in 1.10.0-rc.3 (single-cluster duplicate-row fix)

RC3 adds one operator-visible fix on top of RC2:
**`fix(api): skip agent-proxy auto-register when cluster_id
matches backend's own (BUG-2)`**. In RC2, operators who installed
both the backend and the agent in the SAME cluster (the obvious
single-cluster self-hosted happy-path) saw that one cluster
appear TWICE in the UI selector — once as `in-cluster` and once
as `agent:<UUID> (via agent)`. Surfaced by the cluster-validation
campaign and was masked in RC1 because Bug-1 had collapsed every
agent to `cluster_id="local"`; the rc.2 cluster_hint fix
revealed the duplication.

RC3 short-circuits the auto-register path when the agent's
reported cluster_id matches the backend's own (kube-system
namespace UID, discovered at boot). Out-of-cluster dev runs are
unaffected — the self-skip gate is empty by default and only
fires when in-cluster discovery succeeds.

No schema movement vs RC2 — the matrix above stays valid for
v1.0.0-rc.3 / 1.10.0-rc.3.

---

## What's new in 1.10.0-rc.2 (Phase 3 + cluster-validation fixes)

RC2 supersedes RC1 — the RC1 → GA promise didn't hold once the
cluster-validation matrix surfaced four blockers, one critical.
All RC2 deltas are tagged at the same agent + backend generation
(v1.0.0-rc.2 / 1.10.0-rc.2 — same Phase 1 schema, no schema
movement) so the matrix above stays valid.

### Phase 3 — customer-facing Prom `remote_write` receiver

Operators with an existing Prometheus stack can point its
`remote_write` at KubeBolt instead of running the bundled
vmagent sidecar. Per-tenant bearer auth, token-bucket rate
limiting, cardinality cap (VM-authoritative), admin UI for
limit overrides at `/admin/ingest-limits`, `/metrics` per-tenant
observability surface. Operator guide:
[`docs/integrations/prometheus.md`](./integrations/prometheus.md).

### Operator-visible behavior changes from RC1

- **`Hello.cluster_hint` is now honored** (`fix(api)!:` —
  CRITICAL). RC1 collapsed every multi-cluster agent to
  `cluster_id="local"` in the backend's registry regardless of
  what the agent reported. RC2 derives `cluster_id` from the
  agent's auto-detected kube-system namespace UID via the Hello
  message. **Operators upgrading from RC1** will see backend
  log + UI cluster selector change from `"local"` to the real
  UUID per cluster. VM metric labels were already on the real
  UUID; only the backend's internal view changes. Pre-existing
  `local/`-keyed AgentRecords in BoltDB persist as zombies in
  the cluster selector until the 24h auto-prune horizon — see
  the commit message for the one-shot cleanup script.
- **Log quietude in permissive prom_write mode** — RC1's
  `WARN msg="prom remote_write permissive-fallback"` fired per
  request (12,880 lines/hour in one in-vivo observation). RC2
  logs one WARN per process; ongoing rate observable at
  `kubebolt_prom_write_requests_total{tenant_id="anonymous"}`
  on the `/metrics` scrape endpoint.
- **Helm upgrade from RC1 no longer panics** on the new
  `tenant.id` value reference. The agent chart's templates now
  nil-guard `.Values.tenant`, so `helm upgrade --reuse-values`
  from `1.0.0-rc.1` proceeds without a template error.
- **Filesystem panel renders for OSS-minimal installs** —
  P25-04 had silently traded the agent's `node_fs_used_bytes`
  for node-exporter's `node_filesystem_*`. RC2 adds a chart-side
  fallback so the panel shows agent's coarse metric when
  node-exporter isn't running.
- **kube-prometheus-stack coexistence** — Pod/Workload Monitor
  queries now filter `job=""` so agent-shipped series win over
  Prom's parallel kubelet scrape; Network panel drops kernel
  pseudo-interfaces (gre0, sit0, ip6_vti0, etc.). NodesPage
  Network legend labels each line with the node name instead of
  rendering "(2)" disambiguator. New helm value
  `agent.deferNodeNetwork: true` lets operators silence the
  agent's `node_network_*` emission when an external Prometheus
  is the canonical scraper of node-exporter.

### Build / toolchain hardening

The release pipeline's `preflight-third-party-scan` now also
Trivy-scans `victoriametrics/vmagent` (previously only scanned
`victoriametrics/victoria-metrics`), enforces drift between the
two pins, and the Go toolchain pin moved from `'1.25'` (floating
patch — picked stale 1.25.9 with 5 HIGH stdlib CVEs from the
runner cache) to explicit `'1.25.10'`. All Dockerfiles + go.mod
toolchain directives lockstep.

---

## Migration paths

### Greenfield install

Just install both at the latest matching version:

```bash
helm upgrade --install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
    --version 1.10.0-rc.3 \
    -n kubebolt --create-namespace

helm upgrade --install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
    --version 1.0.0-rc.3 \
    -n kubebolt-agent --create-namespace \
    --set backendUrl=<your-backend-grpc-host:9090>
```

### Existing install at v1.9.x + agent 0.2.x

Upgrade both in the same maintenance window. Order is operationally
forgiving (the WARN is logged, dashboards render empty, samples still
flow) but the canonical pattern is to roll the backend first since
the agent reconnects at registration:

```bash
# 1. Backend first
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
    --version 1.10.0-rc.3 \
    -n kubebolt --reuse-values

# 2. Agent next, in any cluster connected to it
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
    --version 1.0.0-rc.3 \
    -n kubebolt-agent --reuse-values

# 3. Verify the WARN is gone
kubectl logs -n kubebolt deployment/kubebolt --tail=20 | grep "agent below minimum"
# (no output expected; the WARN means the agent is still v0.x)
```

### Multi-cluster fleet

If the kubebolt backend serves multiple clusters via per-cluster
agents, **upgrade all the agents** before declaring the rollout
complete. A backend at v1.10 with a mix of v1.0 and v0.2 agents will
show empty dashboards for whichever clusters lag behind, and the
backend's log will carry one `WARN` per legacy agent on every
reconnect.

The backend doesn't reject legacy agents (samples still ingest into
VM, just under the wrong label set), so a partial rollout is
recoverable — finish the upgrade and the next refetch repopulates
the dashboards.

---

## Why the schema rename

Strategic context lives in
[`internal/agent-universal-data-plane-plan.md`](../internal/agent-universal-data-plane-plan.md)
(the doc is gitignored — read it locally). Short version: future
ingestion paths (Prom remote_write receiver, OTLP receiver, vmagent
sidecar scraping kube-state-metrics + node-exporter directly)
naturally land at the Prom convention. Pre-aligning the agent now
means a single canonical schema feeds every dashboard regardless of
ingest path, instead of three translation layers.

---

## Reading the WARN log

When a legacy agent connects to a v1.10 backend you'll see this exact
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
- [`docs/agent-scraping.md`](agent-scraping.md) — operator guide for the vmagent sidecar (Phase 2): quickstart, config reference, troubleshooting
- [`internal/kubebolt-agent-technical-spec.md`](../internal/kubebolt-agent-technical-spec.md) — authoritative metric / label catalog (v0.2 reflects v1.0 schema)
