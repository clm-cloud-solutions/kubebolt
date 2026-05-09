# Agent Scraping — vmagent sidecar

Phase 2 of the [Universal Data Plane Plan](../internal/agent-universal-data-plane-plan.md)
ships an opt-in `vmagent` sidecar inside the kubebolt-agent
DaemonSet pod. When enabled, each agent pod scrapes
Prom-compatible `/metrics` endpoints on its own node and ships the
samples to the KubeBolt backend's remote_write receiver.

The result: operators get the depth of a Prometheus stack
(kube-state-metrics, node-exporter, app-level metrics from any pod
that carries `prometheus.io/scrape: "true"`) **without running their
own Prometheus**. KubeBolt's backend already runs VictoriaMetrics; the
vmagent sidecar just feeds it.

> **Default off.** The feature is gated by `scrape.enabled: true` in
> the agent helm values AND `KUBEBOLT_REMOTE_WRITE_ENABLED=true` on
> the backend. Both must be on. Phase 3 of the plan replaces the
> env-var gate with a bearer-token middleware specific to this
> ingest path.

---

## TL;DR — turn it on

```bash
# 1. Backend: enable the receiver
export KUBEBOLT_REMOTE_WRITE_ENABLED=true
make dev   # or: helm upgrade kubebolt ... --set metrics.remoteWrite.enabled=true

# 2. Agent: deploy with the sidecar enabled
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
    --version 1.0.0 \
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

### Why a per-pod sidecar (not a cluster-wide deployment)

[ADR-003](../internal/agent-universal-data-plane-plan.md#adr-003-sidecar-per-pod-no-cluster-wide)
in the data plane plan: each agent pod scrapes only the targets that
sit on its own node. The `kubernetes-pods` job has a relabel rule
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
    tag: v1.142.0                 # Pinned to the same VM line as the
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

  # List of Prom relabel rules applied per scrape job.
  metricRelabelConfigs: []

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

> **Phase 3 changes this.** When bearer-token auth lands at the
> receiver, the backend will assert `cluster_id` from the token's
> tenant scope rather than trusting the relabel-applied label.

### Cardinality protection

Each cap rejects samples beyond the limit with a vmagent log line
— operators see the rejection and can either fix the target, raise
the cap, or drop the offending label via `metricRelabelConfigs`.

To drop a runaway label cluster-wide:

```yaml
scrape:
  metricRelabelConfigs:
    - source_labels: [request_id]
      action: labeldrop
      regex: request_id
```

The chart wires `metricRelabelConfigs` into every scrape job via a
shared helper, so adding a rule applies to kubernetes-pods,
node-exporter, and kube-state-metrics in one shot.

---

## Operating playbook

### Verifying the pipeline

The dashboard's **Coverage banner** (top of the Cluster Overview
page) shows one chip per source: kubebolt-agent, node-exporter,
kube-state-metrics, hubble. Active sources show a green checkmark;
inactive sources show a dash. The banner is informational — UI
panels themselves have their own empty-state copy.

For CLI-side verification, the testbed includes a `verify.sh`
script that probes VictoriaMetrics directly and the backend's
`/api/v1/coverage` endpoint:

```bash
internal/testbed-extras/phase2/verify.sh
# Output:
#   ── VictoriaMetrics probes (http://localhost:8428) ──
#     kubebolt-agent (self)         ACTIVE   (count=2)
#     kubelet (cAdvisor stats)      ACTIVE   (count=40)
#     node-exporter                 ACTIVE   (count=448)
#     kube-state-metrics            ACTIVE   (count=47)
#     Hubble flows                  ACTIVE   (count=25)
```

The full testbed (`prom-stack.yaml` + verify.sh + walkthrough) lives
under `internal/testbed-extras/phase2/` (gitignored — local-only dev
tooling).

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

**Cause:** kube-state-metrics declares two `containerPort`s
(`http-metrics` 8080 and `telemetry` 8081). vmagent's pod SD
generates one target per containerPort; the relabel rule rewrites
both to address `:8080` (from the `prometheus.io/port` annotation),
collapsing them into one target with identical labels. vmagent
correctly drops the duplicate but logs a warning every cycle.

**Fix:** Drop the telemetry port from the KSM manifest if you don't
scrape it. Production KSM installs that need the telemetry port
back can re-add it with a separate `prometheus.io/scrape: "false"`
annotation on a sidecar Service, or with a vmagent relabel rule
that drops the second port.

### 5. /api/v1/prom/write returns 401 from vmagent

**Symptom:** Backend log shows a 401 retry storm:

```
unexpected status code received after sending a block ... :
401; response body="{\"error\":\"authentication required\"}\n"
```

**Cause:** Earlier revisions of the backend put `/api/v1/prom/write`
inside the JWT-protected route group. vmagent doesn't carry a user
session JWT.

**Fix:** Already fixed in the v1.10.0 backend — the route lives in
the public-routes block, gated only by the
`KUBEBOLT_REMOTE_WRITE_ENABLED` env var. If you see this on a newer
release, double-check the env var:

```bash
kubectl exec -n kubebolt deployment/kubebolt -- env | grep REMOTE_WRITE
# Should show: KUBEBOLT_REMOTE_WRITE_ENABLED=true
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

Phase 2 of the data plane plan covers vmagent + receiver. It does
NOT cover:

- **Bearer-token auth on the receiver** — Phase 3.
  `KUBEBOLT_REMOTE_WRITE_ENABLED` is the only gate today.
- **OTLP ingestion** — Phase 4 adds an OTLP HTTP/gRPC endpoint and
  translates OTel semantic conventions to the canonical Prom schema.
- **Helm chart split** — Phase 5 splits the bundled `kubebolt` chart
  into `kubebolt` (control plane) + `kubebolt-agent` (already its
  own chart).
- **Pushgateway pattern** — short-lived Jobs/CronJobs metrics aren't
  captured. kube-state-metrics covers their state.
- **Federation cross-cluster** — Etapa 2+ of the plan.

For the strategic context and roadmap, see
[`internal/agent-universal-data-plane-plan.md`](../internal/agent-universal-data-plane-plan.md).

---

## Reference

- Compatibility matrix: [`docs/COMPATIBILITY.md`](COMPATIBILITY.md)
- Agent CHANGELOG: [`packages/agent/CHANGELOG.md`](../packages/agent/CHANGELOG.md)
- Helm chart values: [`deploy/helm/kubebolt-agent/values.yaml`](../deploy/helm/kubebolt-agent/values.yaml)
- Backend handler source: [`apps/api/internal/api/prom_write.go`](../apps/api/internal/api/prom_write.go),
  [`apps/api/internal/api/coverage.go`](../apps/api/internal/api/coverage.go)
- vmagent docs (upstream): https://docs.victoriametrics.com/vmagent.html
