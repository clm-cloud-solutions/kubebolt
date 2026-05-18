# kubebolt-agent

Node agent for [KubeBolt](https://github.com/clm-cloud-solutions/kubebolt).
Ships as a DaemonSet and collects three streams of data from each
Kubernetes node, then forwards them to the KubeBolt backend:

- **kubelet `/stats/summary`** — per-pod / per-container CPU, memory,
  and network counters sampled every 15 s.
- **cAdvisor fallback** — covers kubelets that don't populate the
  pod-level network block (e.g. docker-desktop).
- **Cilium Hubble flow events** — L4 counters, L7 HTTP status +
  latency, and DNS resolutions. Collected by a single leader-elected
  pod so the relay isn't N-times-counted.

The agent is optional. Without it KubeBolt still works — you lose
historical metrics, network / disk observability, and live traffic
flows, but everything else (inventory, insights, YAML edit, exec,
port-forward, logs) is unchanged.

## Installation methods

There are three ways to install the agent. All produce the same
DaemonSet; they differ in who owns it and which tool can later
modify or remove it. The ownership signal is the
`app.kubernetes.io/managed-by` label on the DaemonSet, which
KubeBolt reads on every poll.

| Method | `managed-by` label | Install command | When to use | Who can modify |
|---|---|---|---|---|
| **This chart** | `Helm` | `helm install kubebolt-agent oci://...` | Production. Full value surface including `affinity`, custom `tolerations`, `podAnnotations`, etc. | Helm (`helm upgrade`), KubeBolt UI with force |
| **KubeBolt UI** | `kubebolt` | Administration → Integrations → Install | Quickest path when you already have KubeBolt running. Opinionated value set covering 90% of installs. | KubeBolt UI (Configure / Uninstall) |
| **Raw manifest** | _(unset)_ | `kubectl apply -f deploy/agent/kubebolt-agent-dev.yaml` | Dev loops, air-gapped clusters, GitOps flows that manage their own manifests | The tool that applied it; KubeBolt UI with force |

**Mixing paths:** KubeBolt's UI refuses to modify DaemonSets without
the `managed-by=kubebolt` label by default. Uninstall has a
Force option that removes the workload by name regardless — useful
when migrating between methods. See the Uninstall section below.

## Install

```bash
helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system --create-namespace \
  --set backendUrl=kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090
```

Replace `backendUrl` with wherever your KubeBolt backend's gRPC port
(`:9090`) is reachable from inside the cluster. See the "Connecting
to the backend" section below for concrete examples.

## Permission tier — `rbac.mode`

The chart applies a 3-tier permission model. Pick the one that
matches what you want the dashboard to be able to do in this
cluster:

| `rbac.mode` | What the agent SA can do | Proxy | Auth | When to pick |
|---|---|---|---|---|
| `metrics` | Kubelet stats + pods list/watch + namespaces get | OFF | optional | Privacy-conscious. Only kubelet metrics + Hubble flows leave the cluster. The dashboard shows historical CPU / memory / flows but no inventory through this agent. |
| `reader` (default) | Cluster-wide `get`/`list`/`watch` on `*/*` | ON (mandatory) | optional but recommended | "I want to see everything but not change it." Backend renders inventory + YAML + describe + logs through the agent's tunnel. Write attempts come back 403. |
| `operator` | Wildcard `get/list/watch/create/update/patch/delete` on `*/*` | ON (mandatory) | **REQUIRED** | Full UI parity through the agent — exec, scale, restart, delete, YAML edit. Effectively cluster-admin scoped to the SA; auth on the gRPC channel is the only thing keeping random network probers from pivoting to admin. |

**Pre-0.2.0 readers**: this replaces the binary
`proxy.enabled` + (apply-out-of-band) operator-tier ClusterRole
overlay. Wire-compat is preserved on the backend's install
endpoint, but the chart only speaks `rbac.mode` — pick one of the
three values.

## Auth (`auth.mode`)

Independent toggle from `rbac.mode`. Three values:

- `disabled` (default for self-hosted lab): no credentials. The
  agent dials in plaintext + no token. Only valid against a backend
  that runs in `KUBEBOLT_AGENT_AUTH_MODE=disabled`.
- `ingest-token`: long-lived bearer token. The operator generates
  it via the KubeBolt backend's admin UI ("Agent Tokens") or
  REST (`POST /admin/tenants/{id}/tokens`), then either:
   - creates the Secret manually
     (`kubectl create secret generic kubebolt-agent-token -n
     kubebolt-system --from-literal=token=<paste>`), OR
   - uses the dashboard's
     "Generate token + create Secret" button which does both in one
     request and pre-fills the chart's `auth.ingestToken
     .existingSecret` for you.
- `tokenreview`: projected ServiceAccount token validated by the
  backend via `apiserver TokenReview`. Requires the backend in
  the same cluster as the agent. Set
  `auth.tokenReview.audience=kubebolt-backend` (matches the
  backend's expected audience).

## Achieving full node coverage

The agent runs as a DaemonSet — one pod per node. When a pod can't
be scheduled (node CPU/memory pressure, NoSchedule taint without a
matching toleration, namespace-scoped agent, etc.) KubeBolt loses
visibility for the workloads on that node. The Workload → Monitor
view in the dashboard shows an amber **"Partial coverage"** banner
when this happens, so it's never a silent failure.

The cleanest fix is to give the agent enough scheduling priority to
preempt lower-priority pods on saturated nodes. Three install modes
trade off coverage vs cluster policy strictness:

| Your situation | Knobs | Trade-off |
|---|---|---|
| **Cloud cluster, no strict admission policy** *(default)* | `priority.enabled=false` | Agent only runs where there's room at scheduling time. Coverage banner alerts you when gaps appear; you can flip the knob then. |
| **Want full coverage; OPA/Kyverno policy flags `system-*` PriorityClasses outside `kube-system`** | `priority.enabled=true` *(empty `className`)* | Chart creates a managed PriorityClass `<release>-priority` at value `999999000`. High enough to preempt user workloads with no PriorityClass (priority=0), but **outside the `system-*` range** policies typically guard. |
| **Permissive cluster, want maximum coverage** | `priority.enabled=true`, `priority.className=system-cluster-critical` | Reuses the kubelet-tier PriorityClass. Some auditors flag it outside `kube-system`. |
| **Custom workload taints on certain nodes** *(GPU pools, dedicated worker pools)* | (current default) `tolerations: [{operator: Exists}]` | Agent already runs on every node. To exclude specific nodes, override `tolerations` to a more restrictive list. |

> ⚠️ **About PriorityClass and preemption.** Increasing the agent's
> priority means the scheduler can evict pods with priority `0` (the
> default for any user workload without an explicit `priorityClassName`)
> on tight nodes to make room. This is a deliberate trade-off — it's
> the only way to guarantee an agent on every node when nodes are
> saturated. If your shop is policy-strict, leave `priority.enabled=false`
> and accept that pods on saturated nodes may not have metrics. The
> coverage banner ensures the gap is visible, not silent.

> ⚠️ **About tolerations.** The chart defaults to `[{operator: Exists}]`
> which tolerates **every** taint, including custom ones used to dedicate
> nodes to specific workloads. If you have GPU pools or dedicated
> worker pools you don't want the agent on, override:
>
> ```yaml
> tolerations:
>   - key: node-role.kubernetes.io/control-plane
>     operator: Exists
>     effect: NoSchedule
> ```

> 💡 **Resource requests.** Default requests are intentionally tiny
> (`10m` CPU / `30Mi` memory) so the agent fits even on busy nodes.
> Don't raise these unless you have a specific reason; raising them
> makes Pending pods more likely.

## Upgrade

```bash
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system \
  --reuse-values
```

## Uninstall

```bash
helm uninstall kubebolt-agent -n kubebolt-system
```

This removes every resource the chart created AND the Helm release
metadata, so `helm list -n kubebolt-system` no longer shows it.
The release namespace itself is preserved.

If the agent was installed through the KubeBolt UI instead of this
chart, `helm uninstall` won't find anything — remove it from the
UI (Administration → Integrations → Agent → Uninstall) or delete
the resources by name. The other direction works too: KubeBolt's
UI can force-uninstall a chart-installed agent, though the Helm
release metadata will linger until you also run `helm uninstall`
with `--keep-history=false` or delete the release Secret directly.

## Key values

The values below are the ones most commonly set in production.
Full reference with every knob the chart exposes is in
[`values.yaml`](./values.yaml).

### Required

| Value | Default | Purpose |
|-------|---------|---------|
| `backendUrl` | _(required)_ | Host:port of the KubeBolt API gRPC. See "Connecting to the backend" below. |

### Cluster identity

| Value | Default | Purpose |
|-------|---------|---------|
| `cluster.name` | `""` | Human-readable cluster label emitted alongside `cluster_id`. Empty is fine — only surfaces when querying VictoriaMetrics directly. |
| `cluster.id` | `""` | Override the auto-discovered `cluster_id` (kube-system namespace UID). Leave empty unless migrating legacy data. |

### Image

| Value | Default | Purpose |
|-------|---------|---------|
| `image.repository` | `ghcr.io/clm-cloud-solutions/kubebolt/agent` | Registry path. Override for mirrors or private registries. |
| `image.tag` | `""` | Falls back to `Chart.appVersion`. Pin explicitly in prod. |
| `image.pullPolicy` | `IfNotPresent` | `Always` / `IfNotPresent` / `Never`. Use `Never` for kind/dev where the image is pre-loaded with `kind load`. |
| `imagePullSecrets` | `[]` | For private registries. |

### Hubble flow collector

| Value | Default | Purpose |
|-------|---------|---------|
| `hubble.enabled` | `true` | Toggle the flow collector. Silent no-op when Cilium isn't installed — safe to leave on. |
| `hubble.relay.address` | `""` | Override the relay target. Default: `hubble-relay.kube-system.svc.cluster.local:80`. |
| `hubble.relay.tls.existingSecret` | `""` | Pre-existing Secret in the release namespace with `ca.crt` (TLS) + optional `tls.crt`/`tls.key` (mTLS). |
| `hubble.relay.tls.serverName` | `""` | SNI / verification hostname. Override when the relay's cert uses a CN/SAN distinct from the dial target. |

### Scheduling

| Value | Default | Purpose |
|-------|---------|---------|
| `tolerations` | `[{operator: Exists}]` | Tolerates every taint so the agent lands on control-plane nodes. Trim if you want to exclude some. |
| `nodeSelector` | `{}` | Pin the agent to specific nodes, e.g. `{kubernetes.io/os: linux}`. |
| `affinity` | `{}` | Full affinity object. Most installs don't need this. |
| `priorityClassName` | `""` | Set to `system-cluster-critical` (or your own PriorityClass) in prod to avoid preemption. |

### Resources

| Value | Default | Purpose |
|-------|---------|---------|
| `resources.requests.cpu` | `10m` | Agent is a light Go binary; defaults are sized for small clusters. |
| `resources.requests.memory` | `64Mi` | Bumped from `30Mi` in 1.10.0 to match realistic steady-state with Hubble + agent-proxy + scrape sidecar active. Original sizing was for a kubelet-only Phase 1 agent. |
| `resources.limits.cpu` | `100m` | |
| `resources.limits.memory` | `128Mi` | Bumped from `80Mi` in 1.10.0 to give headroom against Hubble flow parsing burst allocation patterns. See "Memory and observability" below for the rationale and tuning knobs. |

### Memory and observability

The chart sets `GOMEMLIMIT=100MiB` as a default `extraEnv` entry. The
Go runtime targets this value as a soft total-memory cap — when the
process approaches it, GC runs more frequently and the page scavenger
becomes more aggressive about returning idle heap pages to the OS.
This addresses a memory-retention pattern surfaced during the 1.10
validation campaign: high allocation churn from Hubble flow proto
parsing (~200 MB/min steady-state on an active node) made `HeapSys`
ratchet up without `GOMEMLIMIT` pressure. CPU cost: ~5-10% more time
in GC under load — immaterial against the agent's 5-10m CPU baseline.

Operators who bump `resources.limits.memory` significantly higher (e.g.
above 256Mi for very large clusters) should remove or raise the
`GOMEMLIMIT` entry in `extraEnv` so the scavenger pressure doesn't
become counterproductive.

The agent emits a deep set of `kubebolt_agent_heap_*` and
`kubebolt_agent_*_sys_bytes` gauges so the same investigation pattern
is repeatable in the field. For live heap profiling, enable the pprof
endpoint:

```yaml
extraEnv:
  - name: GOMEMLIMIT
    value: "100MiB"
  - name: KUBEBOLT_AGENT_PPROF_ADDR
    value: "127.0.0.1:6060"  # loopback only — port-forward to access
```

Then:

```bash
kubectl port-forward -n <ns> <agent-pod> 6060:6060
go tool pprof http://localhost:6060/debug/pprof/heap
```

### RBAC + ServiceAccount

| Value | Default | Purpose |
|-------|---------|---------|
| `rbac.create` | `true` | Creates ClusterRole/Role + bindings. Set `false` when RBAC is provisioned externally (e.g. Rancher, platform operators). |
| `serviceAccount.create` | `true` | |
| `serviceAccount.name` | `""` | Empty = derive from release name. Set explicitly when `serviceAccount.create=false`. |
| `serviceAccount.annotations` | `{}` | Useful for IRSA (EKS) / Workload Identity (GKE). |

### Extras

| Value | Default | Purpose |
|-------|---------|---------|
| `logLevel` | `info` | `debug` / `info` / `warn` / `error`. |
| `agent.deferNodeNetwork` | `false` | Suppress agent's `node_network_*` emission when an external Prometheus is the canonical scraper of node-exporter. Avoids 2× counts on `sum(rate(node_network_*[1m]))` queries (kubelet `/stats/summary` and node-exporter emit the same metric names from the same `/proc/net/dev` counters). The bundled `scrape.discovery.nodeExporter.enabled=true` path auto-sets this when the chart's vmagent sidecar scrapes node-exporter; flip it explicitly to `true` when a SEPARATE external Prom does the scraping. |
| `extraEnv` | _GOMEMLIMIT preset_ | List of additional env vars. The chart's default already includes `GOMEMLIMIT=100MiB` — see "Memory and observability" above. To add operator-provided env vars, set the list directly with the default re-included: `extraEnv: [{name: GOMEMLIMIT, value: "100MiB"}, {name: KUBEBOLT_AGENT_LOG_LEVEL, value: debug}]`. |
| `podAnnotations` | `{}` | Useful for external scrapers or policy engines. |
| `podLabels` | `{}` | |

## Connecting to the backend

| Topology | `backendUrl` |
|----------|---------------|
| Backend in Docker Compose on your laptop, agent in Docker Desktop K8s | `host.docker.internal:9090` |
| Backend in-cluster via the main chart (release `kubebolt` in namespace `kubebolt`) | `kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090` |
| Backend behind an internal LoadBalancer | that LB's IP:9090 |
| Backend on a VM reachable from the cluster | that host:9090 |

## Hubble + mTLS

When your Cilium install requires mTLS on Hubble Relay, mount a
Secret with the cert material and reference it:

```bash
kubectl -n kubebolt-system create secret generic hubble-client-tls \
  --from-file=ca.crt=path/to/ca.crt \
  --from-file=tls.crt=path/to/client.crt \
  --from-file=tls.key=path/to/client.key

helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system \
  --reuse-values \
  --set hubble.relay.tls.existingSecret=hubble-client-tls \
  --set hubble.relay.tls.serverName='*.hubble-relay.cilium.io'
```

A present `ca.crt` alone enables TLS (relay authenticated, client
anonymous). Adding `tls.crt` + `tls.key` turns on mTLS.

### Deploying on GKE managed Dataplane V2

Google Kubernetes Engine's managed Dataplane V2 ships Hubble Relay
in a different namespace and on a different port than the upstream
Cilium chart — and the relay enforces mTLS by default. The agent's
chart defaults (`hubble-relay.kube-system.svc.cluster.local:80`,
plaintext) silently fail against this setup with
`stream ended, will retry` and no flows surface in the UI.

| Field | Upstream Cilium default | GKE managed DPv2 actual |
|---|---|---|
| Relay namespace | `kube-system` | `gke-managed-dpv2-observability` |
| Relay service port | `80` (plaintext) | `443` (mTLS) |
| Client cert | _(none)_ | required, in Secret `hubble-relay-client-certs` |
| CA bundle | _(system trust)_ | `ca.crt` key of that same Secret |

Two steps to make the agent talk to it:

```bash
# 1. Mirror the relay client-certs Secret into the agent's namespace.
#    GKE creates it in gke-managed-dpv2-observability; the agent's
#    chart can only mount a Secret living in its own release namespace.
kubectl get secret -n gke-managed-dpv2-observability hubble-relay-client-certs -o yaml \
  | sed 's/namespace: gke-managed-dpv2-observability/namespace: kubebolt-system/' \
  | sed '/uid:/d; /resourceVersion:/d; /creationTimestamp:/d; /ownerReferences:/,/^  [a-z]/d' \
  | kubectl apply -f -

# 2. Install / upgrade the agent with the GKE-specific Hubble overrides.
helm upgrade --install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system --create-namespace \
  --set backendUrl=<your-backend>:9090 \
  --set hubble.relay.address=hubble-relay.gke-managed-dpv2-observability.svc.cluster.local:443 \
  --set hubble.relay.tls.existingSecret=hubble-relay-client-certs \
  --set hubble.relay.tls.serverName=hubble-relay.gke-managed-dpv2-observability.svc
```

> ⚠️ **The cross-namespace mirror is not Helm-stable.** If GKE rotates
> the relay certs (it does, on its own cadence), the mirrored Secret
> goes stale and the agent will retry-loop until you re-run step 1.
> For long-running installs prefer a controller that watches the
> source Secret — `External Secrets Operator` and `Reflector` both
> work for this. A built-in chart-side mirror is on the roadmap as
> a follow-up to this section.

#### L7 visibility caveat

GKE managed Dataplane V2 emits **L3/L4 flows only**. Google has not
exposed the `enable-l7-proxy` toggle through the managed cluster API,
so the agent can connect to the Relay via mTLS, see flow events, and
**still leave the Reliability tab in the dashboard hidden** — that
tab is gated on the presence of L7 metrics (HTTP status codes,
latencies), not on Relay reachability.

Verify with one PromQL against the bundled VictoriaMetrics:

```promql
count by (__name__) ({__name__=~"pod_flow_http.*"})
```

If this returns zero rows after the agent has been running for >5
minutes, you're on managed DPv2 (or any L3/L4-only Hubble). The L4
panels (Network Drops, top traffic by bytes) keep working; only the
HTTP-status-aware panels stay hidden.

Operators who need full L7 visibility on GKE must either run
self-managed Cilium on GKE Standard (with `enable-l7-proxy=true` +
`hubble.metrics.enabled=true` in cilium-config) or wait for Google
to expose the L7 toggle.

## Scrape sidecar (`scrape.enabled`)

The agent ships an opt-in vmagent sidecar that scrapes
Prom-compatible `/metrics` endpoints in the cluster
(kube-state-metrics, node-exporter, any pod with
`prometheus.io/scrape: "true"`) and remote_writes the samples to the
KubeBolt backend. Default off.

**For clusters running `kube-prometheus-stack`** (or any Prom install
driven by `ServiceMonitor` / `PodMonitor` CRDs rather than annotations),
the annotation-driven path picks up nothing — those charts don't
add `prometheus.io/scrape` to their pods. Enable both dedicated
scrape jobs explicitly:

```bash
--set scrape.discovery.nodeExporter.enabled=true \
--set scrape.discovery.kubeStateMetrics.enabled=true
```

This routes node-exporter + KSM through the agent's dedicated jobs,
which match by `app.kubernetes.io/name=...` label. Beware: the
dedicated KSM job runs in every agent DaemonSet pod, so KSM samples
are produced N× across an N-node cluster. The bundled KubeBolt
VictoriaMetrics collapses them at write time via
`--dedup.minScrapeInterval=30s`; external TSDBs need the same flag
configured on your side.

Quick start + full config reference + troubleshooting (including the
`kube-prometheus-stack` recipe with `prometheus-node-exporter.nameOverride`):
[`docs/agent-scraping.md`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/agent-scraping.md).
