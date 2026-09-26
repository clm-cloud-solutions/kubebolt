# Agent Scraping — vmagent sidecar

The kubebolt-agent chart ships an opt-in `vmagent` sidecar inside the
agent DaemonSet pod. When enabled, each agent pod scrapes
Prom-compatible `/metrics` endpoints on its own node and ships the
samples to the KubeBolt backend's remote_write receiver.

The result: operators get the depth of a Prometheus stack
(kube-state-metrics, node-exporter, app-level metrics from any pod
that carries `prometheus.io/scrape: "true"`) **without running their
own Prometheus**. KubeBolt's backend already runs VictoriaMetrics; the
vmagent sidecar just feeds it.

> **Default off.** The feature is gated by `scrape.enabled: true` in
> the agent helm values AND the backend's remote_write receiver
> (`metrics.remoteWrite.enabled: true` in the kubebolt chart, i.e.
> `KUBEBOLT_REMOTE_WRITE_ENABLED=true`). Both must be on.
>
> **Auth.** The receiver has its own bearer-token gate,
> `metrics.remoteWrite.authMode` (`KUBEBOLT_REMOTE_WRITE_AUTH_MODE`):
> `disabled` (default — the bearer is ignored), `permissive` (a bearer
> is validated when present; missing or bad ones are logged and
> accepted) or `enforced` (a valid ingest token is required, otherwise
> `401`). Tokens come from the same store as the agent's gRPC ingest
> tokens: with `auth.mode=ingest-token` on the agent chart, vmagent
> sends the same mounted token (`-remoteWrite.bearerTokenFile`), so one
> token covers both paths. Use `enforced` in production. See
> [`integrations/prometheus.md`](integrations/prometheus.md) for the
> full receiver reference.

---

## TL;DR — turn it on

```bash
# 1. Backend: enable the receiver
export KUBEBOLT_REMOTE_WRITE_ENABLED=true
make dev   # or: helm upgrade kubebolt ... --set metrics.remoteWrite.enabled=true

# 2. Agent: deploy with the sidecar enabled
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
    --version 1.4.0 \
    -n kubebolt-agent --reset-then-reuse-values \
    --set scrape.enabled=true \
    --set scrape.remoteWriteUrl=http://kubebolt.kubebolt.svc.cluster.local/api/v1/prom/write

# 3. Verify in the UI: Cluster Overview shows a Coverage banner with
#    one chip per source. After ~60s of scraping you should see
#    kubebolt-agent ✓ + node-exporter ✓ + kube-state-metrics ✓ + hubble ✓
#    (the last one only if Cilium is installed).
```

---

## What gets scraped by default

The chart wires three independent scrape jobs, each toggleable:

| Job | Toggle (default) | Discovery method | What it gives you |
|---|---|---|---|
| `kubernetes-pods` | `scrape.discovery.pods.enabled: true` | Annotation: `prometheus.io/scrape: "true"` on any pod | App-level metrics. The official kube-state-metrics chart and most production exporters carry the annotation by default. |
| `node-exporter` | `scrape.discovery.nodeExporter.enabled: false` | Label: `app.kubernetes.io/name=node-exporter` | Per-node OS metrics (CPU, memory, filesystem at the kernel level). |
| `kube-state-metrics` | `scrape.discovery.kubeStateMetrics.enabled: false` | Label: `app.kubernetes.io/name=kube-state-metrics` | Cluster-state metrics (`kube_pod_*`, `kube_deployment_*`, etc.). |

**Most operators only need `pods` enabled** — kube-state-metrics
ships with the `prometheus.io/scrape` annotation and gets picked up
automatically through the annotation-driven job. The dedicated
`kube-state-metrics` toggle is a fallback for KSM deployments that
don't carry the annotation.

### Running against kube-prometheus-stack (or any ServiceMonitor-driven Prom)

The default path above assumes the operator's KSM and node-exporter
pods carry `prometheus.io/scrape: "true"` annotations. The official
[`kube-prometheus-stack`](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
chart, and any install that uses Prometheus Operator's
`ServiceMonitor` / `PodMonitor` CRDs, **does not add those
annotations** — it relies entirely on the operator's selector
machinery. Plain annotation-driven discovery silently picks up
nothing.

For these clusters, enable both dedicated jobs explicitly and
optionally rename the chart's node-exporter so the agent's
`labelSelector` defaults match:

```bash
# 1. Install kube-prometheus-stack with the node-exporter name
#    aligned to what the agent searches for (saves overriding
#    labelSelector below). This step is the customer's normal
#    install — the rename is the only kubebolt-specific tweak.
helm install kube-prom prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  --set prometheus-node-exporter.nameOverride=node-exporter

# 2. Install the kubebolt-agent with both dedicated scrape jobs on.
helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt-agent --create-namespace \
  --set scrape.enabled=true \
  --set scrape.discovery.nodeExporter.enabled=true \
  --set scrape.discovery.kubeStateMetrics.enabled=true \
  --set scrape.remoteWriteUrl=http://kubebolt.kubebolt.svc.cluster.local/api/v1/prom/write
```

**KSM is scraped once, not once-per-node.** kube-state-metrics is a single
cluster-scoped pod, so a naive DaemonSet would scrape it from every node and
ship the same `kube_*` series N times. The dedicated KSM job carries the same
per-node `node_name == %{NODE_NAME}` keep filter the pods and node-exporter jobs
use, so **only the agent co-located with KSM scrapes it — 1× ingest, not N×** —
and it still returns the full cluster-wide `kube_*` set (KSM is cluster-scoped,
so one scrape covers everything). The duplication is prevented at scrape time,
so no VictoriaMetrics `--dedup` is required to deduplicate KSM. The bundled VM
still ships `--dedup.minScrapeInterval=30s` as a general safety net, and if you
point KubeBolt at an **external** TSDB (`metrics.storage.externalUrl`) you should
keep dedup enabled there for other sources.

If you can't modify the customer's `kube-prometheus-stack` install
(common in audited environments), override the agent-side selector
to whatever name the upstream chart used:

```bash
--set scrape.discovery.nodeExporter.labelSelector="app.kubernetes.io/name=prometheus-node-exporter"
```

### Why a per-pod sidecar (not a cluster-wide deployment)

Each agent pod scrapes only the targets that sit on its own node. The `kubernetes-pods` job has a relabel rule
that filters discovered pods to the local node:

```yaml
- source_labels: [__meta_kubernetes_pod_node_name]
  action: keep
  regex: "%{NODE_NAME}"
```

In a cluster with N nodes, N vmagent sidecars exist; each one scrapes
1/N of the targets. Same coverage, no double-scraping, distributes
load linearly with cluster size. Annotation-discovered pods that have
multiple replicas across nodes get scraped exactly once each — by
whichever vmagent shares their node.

> **The trailing-percent gotcha.** vmagent's env-var expansion uses
> `%{ENV_NAME}` (no closing percent). The first revision of the
> chart had `"%{NODE_NAME}%"` and the literal trailing `%`
> survived expansion, leaving every relabel regex matching an
> impossible value (`kubebolt-dev-control-plane%`). vmagent
> discovered targets but dropped them all. Caught only at in-vivo
> validation. Fixed; documented here so it doesn't slip back.

---

## Configuration reference

All knobs live under `.Values.scrape` in the agent helm chart. Defaults
shipped in the chart's `values.yaml`:

```yaml
scrape:
  enabled: false                  # Master switch.
  image:
    repository: victoriametrics/vmagent
    tag: v1.148.0-scratch         # Pinned to the same VM line as the
                                  # bundled VictoriaMetrics in the kubebolt
                                  # chart. Bump in lockstep on upgrade.
    pullPolicy: IfNotPresent
  remoteWriteUrl: ""              # Empty = run the sidecar but drop samples.
  resources:
    requests: { cpu: 10m,  memory: 64Mi }
    limits:   { cpu: 200m, memory: 256Mi }
  extraArgs: {}                   # Extra vmagent CLI flags as -key=value.

  # Cardinality limits — defensive caps that protect VM from a
  # misconfigured target.
  limits:
    maxScrapeSize:      16777216  # 16 MiB per scrape body (vmagent default).
    maxSeriesPerTarget: 30000     # Series limit per (job, target).
    maxSeriesGlobal:    1000000   # Soft cap on total active series.

  # Sample relabeling applied to every scrape job. Defaults to a
  # family-level allowlist (keep only KubeBolt-consumed metric names)
  # + label drops, so the annotation scrape doesn't sweep in app metrics.
  # See "Cardinality protection" below. Set to [] to keep everything.
  metricRelabelConfigs:
    - source_labels: [__name__]
      action: keep
      regex: "kube_.*|node_.*|container_.*|kubelet_.*|pod_flow_.*|pod_dns_.*|hubble_.*|kubebolt_.*|up|process_.*"
    - action: labeldrop
      regex: "endpoint_id|container_id"

  # Built-in scrape jobs.
  discovery:
    pods:
      enabled: true               # Annotation-driven pod scraping.
    nodeExporter:
      enabled: false
      labelSelector: "app.kubernetes.io/name=node-exporter"
      port: 9100
      path: /metrics
    kubeStateMetrics:
      enabled: false              # Use annotation-driven instead (see above).
      labelSelector: "app.kubernetes.io/name=kube-state-metrics"
      port: 8080
      path: /metrics
```

### Cluster identity (`cluster_id` external label)

Every series shipped by vmagent carries a `cluster_id` external
label. The chart resolves the value at install time:

1. `.Values.cluster.id` if explicitly set
2. The `kube-system` namespace UID (via Helm's `lookup` function)
3. `.Values.cluster.name` (last-resort fallback for dry-run /
   restricted RBAC where lookup returns nil)

Step 2 is what matters. It mirrors the kubebolt-agent's own
`cluster_id` discovery (see [`packages/agent/cmd/agent/main.go`](../packages/agent/cmd/agent/main.go)
`resolveClusterIdent`), so the agent and vmagent stamp the same
identifier. Without this matching, the backend filters by the
agent's UID and the vmagent samples become invisible to the UI.

> **The receiver trusts `cluster_id`.** Bearer auth
> (`metrics.remoteWrite.authMode`) validates the token and the
> `tenant_id` label against the token's tenant, but `cluster_id` is
> still taken from the label vmagent stamps — so it must match the
> agent's own identity.

### Cardinality protection

**The default allowlist is the primary control.** `scrape.metricRelabelConfigs`
ships a family-level keep-rule by default (shown above): it keeps only the metric
families KubeBolt queries and drops everything else — chiefly the app metrics an
annotation scrape would otherwise sweep in (GitLab, sidekiq, client-library
histograms). Validated on a production cluster: **96k → 19k active series
(−80%)**, zero KubeBolt metrics lost. It also drops two unused high-cardinality
labels (`endpoint_id`, `container_id`). If you extend it, keep every family
KubeBolt queries (see the agent chart README's
[Metric footprint](../deploy/helm/kubebolt-agent/README.md#metric-footprint--cardinality-is-controlled-by-default)
section). Set `scrape.metricRelabelConfigs: []` to keep everything.

**Defensive caps.** On top of the allowlist, the `limits` block rejects samples
beyond a hard bound with a vmagent log line — operators see the rejection and can
fix the target, raise the cap, or drop the offending label.

To drop an additional runaway label, **re-include the default keep-rule** and
append your own (overriding the value replaces the default, so leaving it out
re-opens the annotation firehose):

```yaml
scrape:
  metricRelabelConfigs:
    - source_labels: [__name__]        # keep the default allowlist…
      action: keep
      regex: "kube_.*|node_.*|container_.*|kubelet_.*|pod_flow_.*|pod_dns_.*|hubble_.*|kubebolt_.*|up|process_.*"
    - action: labeldrop                # …and add your own drop
      regex: "endpoint_id|container_id|request_id"
```

The chart wires `metricRelabelConfigs` into every scrape job via a shared helper,
so a rule applies to kubernetes-pods, node-exporter, and kube-state-metrics in
one shot.

**`container_network_*` interfaces are filtered in the agent, not here.** The
per-interface `container_network_*` counters ride the gRPC/cadvisor path (Mode
A), which `metricRelabelConfigs` never sees. The agent drops always-zero kernel
tunnel interfaces (`sit0`, `gre0`, `tunl0`, …) via
`collectors.dropNetworkInterfaces` (default set; no-op on cloud CNIs where those
devices don't exist). See the chart README's "Metric footprint" section.

---

## Operating playbook

### Verifying the pipeline

The dashboard's **Coverage banner** (top of the Cluster Overview
page) shows one chip per source: kubebolt-agent, node-exporter,
kube-state-metrics, hubble. Active sources show a green checkmark;
inactive sources show a dash. The banner is informational — UI
panels themselves have their own empty-state copy.

For CLI-side verification, query the backend's `/api/v1/coverage`
endpoint (the same data the banner renders) with an authenticated
session, or count series per source in VictoriaMetrics directly, for
example `count(kube_pod_info)` for kube-state-metrics and
`count(node_load1)` for node-exporter.

### Inspecting vmagent's targets

vmagent exposes Prom-style introspection on port 8429. From inside
the cluster:

```bash
POD=$(kubectl get pods -n kubebolt-agent -l app.kubernetes.io/name=kubebolt-agent \
  -o jsonpath='{.items[0].metadata.name}')

# Active targets
kubectl port-forward -n kubebolt-agent "$POD" 18429:8429 &
curl -s http://localhost:18429/api/v1/targets | jq '.data.activeTargets[] | {job: .labels.job, pod: .labels.pod, health, lastError}'

# Rendered scrape config (post-env-var-expansion)
curl -s http://localhost:18429/api/v1/status/config | jq -r '.data.yaml'
```

`activeTargets` should list the targets vmagent is scraping;
`droppedTargets` shows pods that were considered but rejected by
relabel rules (most commonly the `prometheus.io/scrape != "true"`
keep rule on the kubernetes-pods job).

### Adding a custom scrape target

Two paths:

**Path 1 — annotate the pod.** The kubernetes-pods job picks up any
pod that carries:

```yaml
metadata:
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "8080"        # Defaults to first containerPort.
    prometheus.io/path: "/metrics"    # Defaults to /metrics.
```

This is the recommended approach for application metrics. The pod's
namespace, pod name, and node carry through into Prom labels
automatically.

**Path 2 — add a scrape job via extraArgs.** vmagent reads its config
from a single ConfigMap-backed file. For now, this means editing
the chart's `templates/configmap-vmagent.yaml` directly. A
`scrape.extraScrapeConfigs` knob is planned for a future release.

---

## Troubleshooting

Eight failure modes encountered during in-vivo testing, with the
canonical diagnosis. If you hit any of these, this is where to look.

### 1. Helm upgrade fails: `nil pointer evaluating .scrape.image.repository`

**Symptom:** Upgrading from a pre-1.0.0 chart to 1.0.0+ with
`--reuse-values` fails template rendering with a nil pointer error
on `.Values.scrape.*` fields.

**Cause:** `--reuse-values` reuses the values snapshot Helm stored
during the *last* release — which predates the `scrape:` section in
`values.yaml`. The new chart defaults aren't merged in.

**Fix:** Use `--reset-then-reuse-values` (Helm 3.13+):

```bash
helm upgrade kubebolt-agent ... --reset-then-reuse-values --set scrape.enabled=true
```

The flag re-reads chart defaults FIRST and then overlays user values.
Right semantic for any chart that grew new value blocks between
releases.

### 2. vmagent runs but scrapes zero targets

**Symptom:** `vmagent_remotewrite_bytes_sent_total = 0`. Vmagent's
`/api/v1/targets` shows 100+ dropped targets and 0 active.

**Cause:** Misconfigured env-var expansion. vmagent's syntax is
`%{ENV_NAME}` — single percent at the start, NO closing percent.
A trailing `%` survives expansion as a literal in the relabel regex,
making the keep rule never match.

**Fix:** Verify the rendered config:

```bash
curl -s http://localhost:18429/api/v1/status/config | grep -E 'regex.*node'
# Should show: regex: <actual node name>
# NOT:         regex: <node name>%
```

The chart in v1.0.0+ has the right syntax. If you write a custom
scrape config, follow the same convention and pass `-envflag.enable=true`
to vmagent.

### 3. ConfigMap update doesn't take effect

**Symptom:** You changed `.Values.scrape.metricRelabelConfigs` (or
similar), helm reports the upgrade succeeded, but vmagent is still
running the old scrape config.

**Cause:** The chart's `podAnnotationChecksum: true` setting hashes
`.Values` to force a rolling restart on values changes. But the
ConfigMap content itself doesn't always change `.Values` — and even
when it does, the projected ConfigMap volume on a running pod
reflects the latest content while vmagent only reads the file at
startup.

**Fix:** Force a rollout restart:

```bash
kubectl rollout restart ds/kubebolt-agent -n kubebolt-agent
```

### 4. KSM samples appear duplicated / "skipping duplicate scrape target" warning

**Symptom:** vmagent log emits per-cycle:

```
skipping duplicate scrape target with identical labels;
endpoint=http://10.244.1.143:8080/metrics, ...
```

**Cause:** kube-state-metrics 2.x declares two `containerPort`s
(`http` 8080 and `metrics`/`telemetry` 8081). vmagent's pod SD
generates one target per containerPort; the relabel that rewrites
`__address__` to a single configured port collapses both into one
target with identical labels. vmagent drops the duplicate but logs
the warning every cycle.

**Fix in current chart:** the `kubernetes-pods`,
`node-exporter`, and `kube-state-metrics` scrape jobs all ship with
a `keep` filter on `__meta_kubernetes_pod_container_port_number`
that drops non-matching containerPorts before the address rewrite,
so vmagent never sees the duplicate target. If you're hitting this
on a current chart version, check that:

- The rendered ConfigMap actually carries the filter:
  ```bash
  kubectl get cm -n kubebolt-agent <release>-vmagent \
    -o jsonpath='{.data.scrape\.yml}' | grep container_port_number
  ```
  Should show one `keep` line per dedicated job. If empty, upgrade
  the agent chart.
- The values `scrape.discovery.{nodeExporter,kubeStateMetrics}.port`
  match the actual containerPort the metrics endpoint listens on
  (defaults `9100` and `8080`; some custom builds differ).

**Fallback for older chart versions or unusual builds:** drop the
extra containerPort from the KSM manifest, or add a vmagent relabel
rule of your own filtering by port name (`http` for KSM, `metrics`
for node-exporter).

### 5. /api/v1/prom/write returns 401 from vmagent

**Symptom:** Backend log shows a 401 retry storm:

```
unexpected status code received after sending a block ... :
401; response body="{\"error\":\"authentication required\"}\n"
```

**Cause:** Earlier revisions of the backend put `/api/v1/prom/write`
inside the JWT-protected route group. vmagent doesn't carry a user
session JWT.

**Fix:** Already fixed since the v1.10.0 backend — the route lives
outside the user-session (JWT) group. On a current release a 401
comes from the receiver's own bearer gate: with
`metrics.remoteWrite.authMode=enforced`, vmagent must send a valid
ingest token (agent chart `auth.mode=ingest-token`). Check both env
vars on the backend:

```bash
kubectl exec -n kubebolt deployment/kubebolt-api -- env | grep REMOTE_WRITE
# KUBEBOLT_REMOTE_WRITE_ENABLED=true
# KUBEBOLT_REMOTE_WRITE_AUTH_MODE=disabled|permissive|enforced
```

### 6. vmagent's scrape works but the UI shows the source as inactive

**Symptom:** vmagent's `/api/v1/targets` shows the source as `up` and
samples land in VictoriaMetrics, but the Coverage banner says
INACTIVE.

**Cause:** `cluster_id` mismatch between the agent (uses
kube-system namespace UID) and vmagent (used `cluster.name` in
pre-1.0 chart). Backend filters by the agent's UID; vmagent's
samples become invisible.

**Fix:** Already fixed in v1.0.0+ — the chart uses Helm's `lookup`
function to resolve `cluster_id` from kube-system at install time,
matching the agent. Verify:

```bash
kubectl get cm -n kubebolt-agent kubebolt-agent-vmagent -o yaml | grep cluster_id
# Should show the kube-system UID, not the cluster.name string.
```

If you set `.Values.cluster.id` explicitly, it wins over the lookup
— make sure it matches your kube-system namespace UID:

```bash
kubectl get namespace kube-system -o jsonpath='{.metadata.uid}'
```

### 7. Some scraped metrics show up in VM but the dashboard doesn't filter by cluster

**Symptom:** `kube_pod_info` appears in VM with samples from multiple
clusters mixed together. Dashboards show wrong counts.

**Cause:** Pre-v1.10 backend had a regex (`bareMetricRE` in
`metrics_query.go`) that scoped queries by `cluster_id` only for
metrics whose names start with `node|pod|container|kubebolt|hubble`.
`kube_*` (kube-state-metrics) and `kubelet_*` were missed → queries
went to VM unscoped.

**Fix:** Already fixed in v1.10.0+ — the regex now covers all of
`node|pod|container|kubebolt|kubelet|kube|hubble`. Bumping the
backend resolves it.

### 8. CoverageBanner doesn't render even though the API works

**Symptom:** `curl /api/v1/coverage` returns the right JSON but the
banner doesn't appear on the dashboard.

**Cause:** Earlier visibility heuristic hid the banner when all
sources were active ("no nag"). Made post-install validation
confusing.

**Fix:** Already fixed in v1.0.0+ — the banner renders whenever the
endpoint reports any sources, regardless of their status. If you
still don't see it, the `useCoverage` hook may be showing stale
data from cache: hard-refresh the browser (Cmd+Shift+R).

---

## What this feature is NOT

The sidecar scrapes and ships; it does NOT cover:

- **OTLP ingestion** — there is no OTLP endpoint; metrics arrive via
  the agent's gRPC channel or Prometheus `remote_write`.
- **Pushgateway pattern** — short-lived Jobs/CronJobs metrics aren't
  captured. kube-state-metrics covers their state.
- **Cross-cluster federation** — each agent's sidecar scrapes only its
  own cluster.
- **Reading an existing Prometheus** — if the cluster already runs a
  Prometheus (including managed ones such as AMP, Azure Managed
  Prometheus or GMP), either point its `remote_write` at KubeBolt
  ([`integrations/prometheus.md`](integrations/prometheus.md)) or use
  the agent's read mode, `agent.promRead.enabled=true`
  ([`integrations/self-managed-prom-readonly.md`](integrations/self-managed-prom-readonly.md)).
  `scrape.enabled` and `agent.promRead.enabled` are mutually exclusive.

---

## Reference

- Compatibility matrix: [`docs/COMPATIBILITY.md`](COMPATIBILITY.md)
- Agent CHANGELOG: [`packages/agent/CHANGELOG.md`](../packages/agent/CHANGELOG.md)
- Helm chart values: [`deploy/helm/kubebolt-agent/values.yaml`](../deploy/helm/kubebolt-agent/values.yaml)
- Backend handler source: [`apps/api/internal/api/prom_write.go`](../apps/api/internal/api/prom_write.go),
  [`apps/api/internal/api/coverage.go`](../apps/api/internal/api/coverage.go)
- vmagent docs (upstream): https://docs.victoriametrics.com/vmagent.html
