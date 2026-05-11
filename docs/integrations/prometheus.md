# Prometheus → KubeBolt remote_write

Ship samples from an existing Prometheus (or any Prom-compatible
agent like `vmagent`, OpenTelemetry Collector with the Prom
exporter, Grafana Agent, etc.) into KubeBolt's backend. Phase 3
of the [Universal Data Plane Plan](../../internal/agent-universal-data-plane-plan.md)
makes the receiver production-ready: per-tenant bearer auth,
per-tenant rate limiting, per-tenant cardinality caps, and
`/metrics` observability so you can see exactly what's being
accepted, throttled, or rejected.

The result: operators with an existing Prom stack don't have to
swap it out — they point `remote_write` at KubeBolt and start
seeing their workloads in the UI within a scrape cycle.

> **Available from KubeBolt 1.10.0+.** Earlier releases shipped a
> Phase 2 receiver gated only by an env var (no auth, no
> per-tenant limits). Phase 3 supersedes it; the endpoint URL is
> unchanged so existing clients keep working through the upgrade.

---

## TL;DR — point an existing Prometheus at KubeBolt

```bash
# 1. Issue an ingest token (Admin UI → Agent Tokens, or curl below)
TOKEN=$(curl -s -X POST http://kubebolt.example.com/api/v1/admin/tenants/<TENANT_ID>/tokens \
  -H "Authorization: Bearer <ADMIN_JWT>" \
  -H "Content-Type: application/json" \
  -d '{"label":"prod-prometheus"}' | jq -r .token)

# 2. Drop into your prometheus.yml
cat >> prometheus.yml <<EOF
remote_write:
  - url: http://kubebolt.example.com/api/v1/prom/write
    authorization:
      credentials: ${TOKEN}
    write_relabel_configs:
      # Required when the receiver runs in enforced mode (recommended).
      # Stamps every sample with the tenant_id label so the receiver
      # can validate it against the bearer's tenant.
      - target_label: tenant_id
        replacement: <TENANT_ID>
EOF

# 3. Reload Prometheus + verify
curl -X POST http://your-prometheus:9090/-/reload
curl -s http://kubebolt.example.com/metrics \
  | grep kubebolt_prom_write_samples_accepted_total
```

---

## Endpoint

| Field | Value |
|---|---|
| URL | `POST /api/v1/prom/write` |
| Wire format | Snappy-compressed Prometheus `WriteRequest` protobuf (stock remote_write) |
| Auth | Bearer token in `Authorization` header (mode-dependent — see below) |
| Body cap | 16 MiB compressed |
| Success | `204 No Content` |

---

## Auth modes

The receiver supports three enforcement modes, selected via
`KUBEBOLT_PROM_WRITE_AUTH_MODE` on the backend. Pick based on
where the client lives and your trust posture:

| Mode | Bearer required | Tenant validation | When to use |
|---|---|---|---|
| `disabled` | ignored | none | Single-cluster OSS, trusted internal network. Default for backwards compatibility. |
| `permissive` | optional | validated when present, otherwise auto-stamped as `tenant_id="anonymous"` | Rollout window — letting legacy unauthenticated clients keep working while you migrate them. |
| `enforced` | required | anti-spoof: the `tenant_id` label on samples MUST match the bearer's tenant | Production / SaaS / multi-tenant. Reject ambiguous traffic. |

In `enforced` mode, requests are rejected with `401 Unauthorized`
when:
- the `Authorization` header is missing, empty, or carries an
  invalid token, OR
- the request body has no `tenant_id` label, OR
- the request body carries a `tenant_id` that doesn't match the
  bearer's tenant (spoof attempt).

In `permissive` mode the same conditions log a single
`WARN msg="prom remote_write permissive-fallback engaged"` per
process (subsequent fallbacks → DEBUG) and accept the request
under the synthetic `tenant_id="anonymous"` identity. Track the
ongoing rate via `kubebolt_prom_write_requests_total{tenant_id="anonymous"}`
on `/metrics` rather than the log.

---

## Multi-tenant deployment

In SaaS or shared-backend topologies, issue one ingest token per
tenant. Each Prometheus instance carries its own bearer AND
stamps every sample with its `tenant_id` label, so the receiver
can validate one against the other:

```yaml
# customer-A's prometheus.yml
global:
  external_labels:
    tenant_id: aaaa-1111-aaaa-1111   # customer A's tenant UUID

remote_write:
  - url: https://kubebolt.example.com/api/v1/prom/write
    authorization:
      credentials_file: /etc/prometheus/kubebolt-token
```

`external_labels` is preferred over `write_relabel_configs`
because it stamps every series cluster-wide, including alerting
and recording rule output. The agent's stock `prometheus_remote_storage_*`
metrics still ship out without that label, but the receiver
auto-stamps them on accept (Day 4.1 of Phase 3) so they end up
correctly attributed.

Anti-spoofing: if a client tries `tenant_id: bbbb-2222-bbbb-2222`
with a bearer that authenticates as customer A, the receiver
returns `401` and logs `prom remote_write tenant_id mismatch`.

---

## Per-tenant limits

Defaults applied to every tenant when no per-tenant override is
configured:

| Knob | Default | Env var to change globally | Override per-tenant |
|---|---|---|---|
| Write rate (samples/s) | 10,000 | `KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC` | UI `/admin/ingest-limits` |
| Burst (samples) | 100,000 | `KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES` | same |
| Max active series | 1,000,000 | `KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES` | same |

Per-tenant overrides live in BoltDB and survive restarts. Use
them when a single tenant ships substantially more (or less)
than the fleet baseline. The UI form sends only the dirty fields,
so unchanged values inherit the system default automatically.

When a limit trips:

| Limit | HTTP response | Retry-After |
|---|---|---|
| Write rate | `429 Too Many Requests` | seconds until the bucket refills |
| Burst | `429 Too Many Requests` | seconds until the bucket refills |
| Max active series | `413 Payload Too Large` | 3600 (1h — series caps don't change quickly) |
| Body size (16 MiB) | `413 Payload Too Large` | — |

`vmagent` and recent Prometheus versions honor `Retry-After`
natively; older clients fall back to exponential backoff.

---

## Observability — `/metrics`

The backend exposes its own Prom-style metrics at `GET /metrics`
(no auth — firewall this port at the load balancer in SaaS).
Useful PromQL for each operator question:

```promql
# "Is this tenant being throttled?"
rate(kubebolt_prom_write_requests_total{tenant_id="<id>",status="rate_limit"}[5m])

# "Is this tenant near the cardinality cap?"
kubebolt_prom_write_active_series{tenant_id="<id>"}
  / on(tenant_id) group_left
kubebolt_prom_write_active_series_limit  # configured separately

# "How much data is each tenant shipping?"
sum by (tenant_id) (rate(kubebolt_prom_write_samples_accepted_total[5m]))

# "What's the rejection rate, and why?"
sum by (status) (rate(kubebolt_prom_write_requests_total{status!="accepted"}[5m]))
```

The `status` label takes one of: `accepted`, `rate_limit`,
`cardinality`, `auth`, `body_size`, `malformed`,
`tenant_id_mismatch`, `tenant_id_missing`, `injection_failed`,
`upstream_error`.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `204` but samples don't appear in UI | Series under a `cluster_id` you're not viewing | Check `count by (cluster_id) ({tenant_id="<id>"})` in VM. Set `external_labels.cluster_id` in Prom if the agent's auto-detection isn't reaching the same value. |
| `401 missing Bearer token` (enforced mode) | Prometheus has no `authorization:` block, or the file is empty | Confirm `authorization.credentials_file` resolves to a non-empty file. Tokens look like `kb_<base64>` and survive base64 decode. |
| `401 invalid ingest token` | Token revoked / rotated / from a different KubeBolt instance | Re-issue from `/admin/agent-tokens` and update the Prom config. |
| `401 tenant_id label does not match` | Client stamped a tenant other than its bearer's | Make sure `external_labels.tenant_id` matches the tenant the bearer authenticates as. Spoof attempts are intentionally rejected. |
| `401 tenant_id label required` (enforced mode) | No `tenant_id` external label set | Add `external_labels.tenant_id: <UUID>` to the Prom config. |
| `413 Payload Too Large` (body size) | Single batch exceeds 16 MiB compressed | Lower `queue_config.max_samples_per_send` (default 2000). Most operators see this only with very long-running Prometheus catching up after a network blip. |
| `413` with `Retry-After: 3600` | Cardinality cap exceeded | Series count is checked every 30s against VM. Bump `maxActiveSeries` via UI or scope your Prom config to fewer targets. |
| `429 Too Many Requests` | Rate limit tripped | Bump `writeSamplesPerSec`/`writeBurstSamples` via UI, or reduce scrape frequency. |
| `502 Bad Gateway` | VictoriaMetrics unreachable from the backend | Check `kubebolt-api` → VM connectivity. Pre-fix the underlying outage; client should retry. |

---

## Coexisting with the bundled kubebolt-agent

When `kubebolt-agent` is running alongside the external Prometheus
described above, three categories of metric overlap end up in
VictoriaMetrics:

1. **`node_network_*_bytes_total`** — agent reads them from
   kubelet's `/stats/summary`, Prom reads them from node-exporter
   (or its own kubelet scrape). Same metric name, same node label
   convention after the relabel fix above, so `sum(rate(...))`
   reads 2× their true value.
2. **`container_*`** (CPU, memory, network) — agent reads them
   from kubelet's `/stats/summary` + `/metrics/cadvisor`, Prom
   reads them from its kubelet ServiceMonitor. Same metric name,
   same `(pod, namespace, container)` labels.
3. **`container=""` pod-level rows** — Prom's kubelet scrape
   surfaces every cAdvisor pod-level entry (with empty container
   label) per cgroup hierarchy; the agent only emits rows with
   real container names from `/stats/summary`.

KubeBolt handles each overlap at a different layer:

### `node_network_*` — operator opts the agent out

Set the helm value `agent.deferNodeNetwork: true` on the
`kubebolt-agent` chart when you're running an external Prometheus
that already scrapes node-exporter:

```yaml
# values.yaml for kubebolt-agent
agent:
  deferNodeNetwork: true

# If you're shipping via external Prom you probably don't need
# the bundled vmagent sidecar either:
scrape:
  enabled: false
```

The agent's stats collector skips `node_network_receive_bytes_total`
and `node_network_transmit_bytes_total` so only node-exporter
emits them. Cluster-wide network panels (Capacity, Node Monitor)
read 1× their true value.

Everything else the agent emits (node CPU, memory, filesystem,
kubelet_volume_stats_*, agent self-metrics, Hubble flows) keeps
flowing — none of those overlap with what Prom scrapes.

### `container_*` — chart-side `job=""` filter (no operator action)

The Pod Monitor and Workload Monitor charts filter `job=""` in
their PromQL so they always read agent's emission, never Prom's.
This preserves the agent's `workload_kind` / `workload_name`
labels (PodsCache enrichment) that Workload Monitor needs to scope
its sums, and eliminates the duplicate from Prom's parallel
emission.

The trade-off: if you DON'T run kubebolt-agent at all (Prom-only
topology), Pod Monitor and Workload Monitor go blank because they
have no `job=""` series to query. **Future Phase 4+ work will
derive workload attribution from `kube_pod_owner` + 
`kube_replicaset_owner`** (kube-state-metrics standard names),
allowing those charts to function against Prom-only data.

### `container=""` pod-level rows — chart-side `container!=""` filter

The same Pod Monitor queries add `container!=""` to drop Prom's
pod-level cAdvisor entries that previously rendered as a phantom
"Series" legend entry. The agent didn't emit these, so the filter
is a no-op when the agent is the source, but it shields the chart
from Prom's higher cardinality when both sources coexist.

### Pseudo-interface noise

Linux network namespaces always carry pseudo-interfaces (`gre0`,
`sit0`, `ip6_vti0`, etc.) with all-zero counters. Prom's kubelet
scrape surfaces every one of them; the agent's `/stats/summary`
parser collapses by default. The Pod Monitor Network panel
filters `interface=~"eth.*|en.*|cilium_.*"` to show only the
interfaces that carry real traffic.

---

## Label compatibility — kube-prometheus-stack users

If you're shipping samples to KubeBolt from a kube-prometheus-stack
installation, **add this relabel to your node-exporter
ServiceMonitor** or two panels in the node detail view (Load
average + PSI pressure) and one Insights hook (`useNodeStress`)
will silently degrade. The Filesystem panel uses a built-in
fallback so it still renders coarse data, but you lose the
per-mountpoint breakdown.

### Why the relabel is needed

KubeBolt's frontend filters node-scoped charts by a `node` label
that matches the Kubernetes node name (e.g.
`node="kubebolt-dev-worker"`). The bundled `kubebolt-agent`
stamps this label on every kubelet-scoped metric it emits. But
kube-prometheus-stack's default ServiceMonitor for node-exporter
stamps `instance=<pod-name-or-ip>` instead of `node=<node-name>`,
so Prom-shipped node-exporter series arrive at the receiver
without the `node` label the chart queries expect.

Other ServiceMonitors in kube-prometheus-stack (kubelet,
kube-state-metrics) already propagate `node` correctly — only
node-exporter needs the fix.

### The fix — Helm values (recommended)

If you install kube-prometheus-stack via its Helm chart, add the
relabel under the node-exporter ServiceMonitor's `relabelings`:

```yaml
# values.yaml for kube-prometheus-stack
prometheus-node-exporter:
  prometheus:
    monitor:
      relabelings:
        - sourceLabels: [__meta_kubernetes_pod_node_name]
          targetLabel: node
```

`upgrade` the release and within ~60s (operator reconcile + Prom
config-reloader cycle) the new scrape cycle ships series with the
`node` label set.

### The fix — direct ServiceMonitor patch

If you don't manage kube-prometheus-stack via Helm (or want a
quick in-vivo verification without a full upgrade), patch the
ServiceMonitor directly:

```bash
kubectl patch servicemonitor <prefix>-node-exporter -n monitoring \
  --type json -p '[{
    "op": "add",
    "path": "/spec/endpoints/0/relabelings",
    "value": [{
      "sourceLabels": ["__meta_kubernetes_pod_node_name"],
      "targetLabel": "node"
    }]
  }]'
```

This patch is **not Helm-stable** — the next `helm upgrade` will
revert it. Use the values.yaml approach for durable installs.

### Verification

Two PromQL probes confirm the relabel took effect. Run them
against the KubeBolt backend's VictoriaMetrics endpoint
(`http://kubebolt:8428` in-cluster, or `:8428` port-forwarded):

```promql
# 1. Series count grouped by node — every row should have a
#    non-empty node value matching a real cluster node name.
count by (node) (node_filesystem_avail_bytes{job="node-exporter"})

# 2. Same for the metrics that previously went blank.
count by (node) (node_load1)
count by (node) (node_pressure_cpu_waiting_seconds_total)
```

Before the fix: `node="<empty>"` for every row.
After the fix: `node="<actual-node-name>"` per cluster node.

### Affected panels — full inventory

The relabel surfaces directly affect three frontend surfaces:

| Surface | Affected metric | Without relabel | With relabel |
|---|---|---|---|
| Node Monitor → Filesystem Usage | `node_filesystem_avail/size_bytes` | Coarse fallback (`node_fs_used_bytes` from the agent — a single bytes counter per node) | Per-mountpoint, multiple series |
| Node Monitor → Load average | `node_load1/5/15` | Panel empty | Three lines (1m / 5m / 15m) |
| Node Monitor → PSI pressure | `node_pressure_*_waiting_seconds_total` | Panel empty | Three lines (cpu / io / memory pressure) |
| `useNodeStress` hook → Insights / banner | `node_pressure_*_waiting_seconds_total` | False negative (banner suppressed) | Stress states correctly detected |

The following panels are **unaffected** because their queries
either don't filter by `node` (cluster-wide aggregations) or use
metrics the agent emits with `node` correctly stamped:

- CapacityPage cluster CPU / Memory / Filesystem / Network
- Node Monitor → CPU, Memory, Network panels
- NodesPage sparkline cards (CPU, Memory, Filesystem, Network)
- All pod / workload Monitor panels (container_* metrics filtered
  by `pod=` / `namespace=`, not `node=`)

---

## See also

- [`docs/agent-scraping.md`](../agent-scraping.md) — alternative path: run the bundled `vmagent` sidecar instead of standalone Prometheus
- [`internal/agent-universal-data-plane-plan.md`](../../internal/agent-universal-data-plane-plan.md) — design rationale for the multi-source ingest model
- KubeBolt admin UI: `/admin/agent-tokens` (issue/rotate/revoke), `/admin/ingest-limits` (per-tenant overrides)
