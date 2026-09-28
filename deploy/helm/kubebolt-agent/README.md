# kubebolt-agent

The agent that connects a Kubernetes cluster to
[KubeBolt](https://kubebolt.io), the open-source Kubernetes operations
platform. It only dials **out** to the KubeBolt backend over one gRPC
channel, so it works for clusters whose API server the backend cannot
reach (private networks, on-prem behind NAT, or a remote backend such as
KubeBolt Cloud).

The chart deploys:

- **A DaemonSet (Mode A)** — one pod per node that collects:
  - **kubelet `/stats/summary`** — per-pod / per-container CPU, memory,
    network and filesystem counters, polled every 30 s.
  - **cAdvisor fallback** — covers kubelets that don't populate the
    pod-level network block (e.g. docker-desktop).
  - **Cilium Hubble flow events** *(opt-in, `hubble.enabled=true`)* — L4
    counters, L7 HTTP status + latency, and DNS resolutions. Collected by
    a single leader-elected pod so the relay isn't counted N times.
  - **Kubernetes API proxy** *(`rbac.mode=reader` or `operator`)* — lets
    the backend read (and, in `operator` mode, change) the cluster through
    the agent's tunnel.
- **A `promread` Deployment (Mode C, opt-in)** — a single pod that reads
  from a Prometheus you already run (self-managed, AWS AMP, Azure Monitor
  managed Prometheus, Google Managed Prometheus). Enabled with
  `agent.promRead.enabled=true`. See
  [Topology — Mode A vs Mode C](#topology--mode-a-vs-mode-c).

User documentation: [kubebolt.io/docs/agent](https://kubebolt.io/docs/agent)
· [connect clusters](https://kubebolt.io/docs/connect-clusters) ·
[remote clusters](https://kubebolt.io/docs/remote-clusters) ·
[compatibility](https://kubebolt.io/docs/compatibility).

When the backend already reaches the cluster directly (in-cluster install or
kubeconfig), the agent is optional. Without it KubeBolt still works — you lose
historical metrics, network / disk observability, and live traffic flows, but
everything else (inventory, insights, YAML edit, exec, port-forward, logs) is
unchanged. Clusters the backend can't reach directly need the agent in
`reader` or `operator` mode.

This chart version (1.4.x) emits the Prometheus-canonical metric schema and
pairs with KubeBolt ≥ 1.10 (including 2.x); see the
[compatibility matrix](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/COMPATIBILITY.md).

For clusters that also run Prometheus-compatible exporters
(node-exporter, kube-state-metrics, or app pods carrying
`prometheus.io/scrape`), the agent can additionally run an **opt-in
vmagent sidecar** that scrapes those `/metrics` endpoints and
`remote_write`s the samples to the same backend — so the dashboard's
node OS, kube-state, and app-metric panels light up. It's **off unless
you set `scrape.enabled=true`**, and it ships with cardinality controls
(a metric-family allowlist + single-scrape kube-state-metrics) on by
default so it stays lean. If you already run a Prometheus you'd rather
reuse, point it at KubeBolt instead of scraping twice — see
[Choosing your coverage](#choosing-your-coverage--simplest-to-fullest).

## Installation methods

There are three ways to install the agent. All produce the agent
DaemonSet; they differ in who owns it and which tool can later
modify or remove it. The ownership signal is the
`app.kubernetes.io/managed-by` label on the DaemonSet, which
KubeBolt reads on every poll.

| Method | `managed-by` label | Install command | When to use | Who can modify |
|---|---|---|---|---|
| **This chart** | `Helm` | `helm install kubebolt-agent oci://...` | Production. Full value surface including `affinity`, custom `tolerations`, `podAnnotations`, etc. | Helm (`helm upgrade`), KubeBolt UI with force |
| **KubeBolt UI** | `kubebolt` | Administration → Agents & Ingest → Integrations → KubeBolt Agent → Install | Quickest path when you already have KubeBolt running. Opinionated value set covering 90% of installs. | KubeBolt UI (Configure / Uninstall) |
| **Raw manifest** | _(unset)_ | `helm template` output from this chart, applied with `kubectl apply -f` (see [`deploy/agent/README.md`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/deploy/agent/README.md) — the checked-in `deploy/agent/*.yaml` files are legacy 0.2.x manifests) | Air-gapped clusters, GitOps flows that manage their own manifests, RBAC-conscious shops that want the SA's permission tier explicit in the manifest | The tool that applied it; KubeBolt UI with force |

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
(`:9090`) is reachable from inside the cluster. The example above is the
in-cluster Service of the `kubebolt` chart (`<release>-agent-ingest.<ns>`,
here release `kubebolt` in namespace `kubebolt`). See
[Connecting to the backend](#connecting-to-the-backend) for other
topologies, including a remote backend such as KubeBolt Cloud.

### Choosing your coverage — simplest to fullest

The command above is all most clusters need to start. It lights up **Mode A**
(kubelet CPU/memory and container metrics; add `hubble.enabled=true` for
Hubble flows if you run Cilium) —
already with **cardinality controls on by default** (see
[Metric footprint](#metric-footprint--cardinality-is-controlled-by-default)).
Scale up only when you want more:

| You want… | Add to the install | Notes |
|---|---|---|
| **Just the essentials** (greenfield, no Prometheus) | nothing — the command above | Mode A only. CPU/mem/container metrics. Add `--set hubble.enabled=true` on Cilium for flows. |
| **Full node + cluster coverage** (node-exporter + kube-state-metrics) | `--set scrape.enabled=true --set scrape.discovery.nodeExporter.enabled=true --set scrape.discovery.kubeStateMetrics.enabled=true` | Bundled vmagent sidecar scrapes them. **Duplication is prevented automatically** (see [Scrape sidecar](#scrape-sidecar-scrapeenabled)). `kube-prometheus-stack` needs a label override — see [`docs/agent-scraping.md`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/agent-scraping.md). |
| **You already run Prometheus** | Point it at KubeBolt instead of scraping twice — see the integration guides below | Mode C (promread) or `remote_write`. No double collection. |
| **Cost data** (OpenCost) | `--set opencost.enabled=true`, or list your own OpenCost under `collectors.exporters` | The agent scrapes OpenCost's `/metrics`. See [`docs/integrations/opencost.md`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/opencost.md). |

**Integration guides** — when the cluster already has a Prometheus you want to
reuse (pick the one that matches your setup):

- [Self-managed Prometheus → `remote_write`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/prometheus.md)
- [Self-managed Prometheus, read-only (Mode C)](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/self-managed-prom-readonly.md)
- [AWS Managed Prometheus (AMP)](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/aws-amp.md)
- [Azure Managed Prometheus (AMW)](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/azure-managed-prometheus.md)
- [Google Managed Prometheus (GMP)](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/gcp-managed-prometheus.md)

## Permission tier — `rbac.mode`

The chart applies a 3-tier permission model. Pick the one that
matches what you want the dashboard to be able to do in this
cluster:

| `rbac.mode` | What the agent SA can do | Proxy | Auth | When to pick |
|---|---|---|---|---|
| `metrics` | Kubelet stats + pods list/watch + namespaces get | OFF | optional | Privacy-conscious. Only kubelet metrics + Hubble flows leave the cluster. The dashboard shows historical CPU / memory / flows but no inventory through this agent. |
| `reader` (default) | Cluster-wide `get`/`list`/`watch` on `*/*` | ON (mandatory) | optional but recommended | "I want to see everything but not change it." Backend renders inventory + YAML + describe + logs through the agent's tunnel. Write attempts come back 403. |
| `operator` | Wildcard `get/list/watch/create/update/patch/delete` on `*/*` | ON (mandatory) | **REQUIRED** | Full UI parity through the agent — exec, scale, restart, delete, YAML edit. Effectively cluster-admin scoped to the SA; auth on the gRPC channel is the only thing keeping random network probers from pivoting to admin. |

The chart derives `proxy.enabled` from `rbac.mode`; setting
`proxy.enabled=false` with `reader` or `operator` fails the render.

## Auth (`auth.mode`)

Independent toggle from `rbac.mode`. Three values:

- `disabled` (default for self-hosted lab): no credentials. The
  agent sends no token. Only accepted by a backend whose agent auth
  mode is `disabled` or `permissive`.
- `ingest-token`: long-lived bearer token. The operator generates
  it in the KubeBolt UI (**Administration → Agents & Ingest → Agent
  Tokens**) or via REST (`POST /api/v1/admin/tenants/{id}/tokens`),
  then either:
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
  `auth.tokenReview.audience=kubebolt-backend` (the default; must match
  the backend chart's `agentIngest.tokenAudience`).

The backend decides whether credentials are required through its agent
auth mode (`agentIngest.authMode` in the `kubebolt` chart, or
Administration → Agents & Ingest → Configuration): `disabled`,
`permissive` or `enforced`.

## Topology — Mode A vs Mode C

The chart can run the agent in two complementary topologies:

| Topology | What it does | When it renders |
|---|---|---|
| **Mode A — DaemonSet** *(always)* | One pod per node. Scrapes the local kubelet (`/stats/summary` + `/metrics/cadvisor`) for the metrics the UI's curated panels consume (`node_fs_used_bytes`, `container_cpu_usage_seconds_total`, `pod_*`), plus load average / PSI from `/proc`. Optionally runs the leader-elected Hubble flow collector, the exporter scraper (`collectors.exporters`, OpenCost) and the vmagent scrape sidecar. | Always. |
| **Mode C — Deployment (replicas=1)** *(opt-in)* | Single cluster-wide pod that polls the customer's existing Prometheus via `/api/v1/query_range` and forwards the converted samples through the same AgentChannel as Mode A. Adds metrics Mode A doesn't synthesize: full kube-state-metrics, `node_load*`, PSI pressure, disk I/O detail, network errors. | When `agent.promRead.enabled=true`. |

The two run **in separate pods** — Mode C does NOT piggy-back on the DaemonSet. The split was introduced in 1.13 after a multi-node validation showed that running both pipelines on the same leader pod overflowed its shared buffer and silently dropped its own kubelet samples. Each pod has dedicated buffer + shipper; no contention.

**Picking your install shape:**

| Customer profile | Values to set |
|---|---|
| Greenfield / no existing Prom | `agent.promRead.enabled=false` (default) — Mode A only. Optionally `scrape.enabled=true` for the bundled vmagent sidecar. |
| Has Prom that supports `remote_write` outbound (self-managed Prom, GCP GMP customer-deployed) | Mode A + customer's Prom `remote_write` into the KubeBolt backend's `/prom/write` endpoint. `agent.promRead.enabled=false`. |
| Has managed Prom that's query-only (AWS AMP, Azure Monitor managed Prom, GCP GMP managed) OR change-management blocks editing the customer's Prom config | `agent.promRead.enabled=true` + `agent.promRead.url=<prom-url>` + the matching `agent.promRead.auth.mode`: `none`, `basicAuth`, `bearer`, `awsSigV4` (AMP via IRSA, needs `awsRegion`), `azureWorkloadIdentity` (Azure Monitor via Workload Identity) or `gcpIam` (GMP via Workload Identity). Mode A keeps running in parallel for the KubeBolt-named metrics. |

**Mutual exclusion enforced at render time:** if `scrape.enabled=true` AND `agent.promRead.enabled=true`, the chart hard-fails with a clear message — pick one (the scrape sidecar and the customer-Prom reader would duplicate work for no UI gain).

**Setting up Mode C or `remote_write`?** The [integration guides](#choosing-your-coverage--simplest-to-fullest) (AWS AMP, Azure Monitor, GMP, self-managed Prometheus) walk through the cloud identity + auth wiring end-to-end.

**Default Mode C matchers are surgical.** They pull ONLY metrics Mode A doesn't already produce, to avoid 2× storage on overlapping data. They are **explicit metric names**, not `__name__=~` regexes, because Google Managed Prometheus rejects regex matching on `__name__`:

| Group | Default metric names |
|---|---|
| kube-state-metrics core | `kube_pod_info`, `kube_pod_status_phase`, `kube_pod_status_ready`, `kube_pod_container_status_{waiting,terminated}_reason`, `kube_pod_container_resource_{requests,limits}`, `kube_deployment_status_replicas{,_available,_unavailable}`, `kube_statefulset_status_replicas{,_ready}`, `kube_daemonset_status_number_ready`, `kube_daemonset_status_desired_number_scheduled`, `kube_node_info`, `kube_node_status_{capacity,allocatable,condition}` |
| node-exporter | `node_load{1,5,15}`, `node_pressure_{cpu,io,memory}_waiting_seconds_total`, `node_disk_{read_bytes,written_bytes,io_time_seconds}_total`, `node_network_{receive,transmit}_errs_total` |
| target health | `up`, `process_resident_memory_bytes`, `process_cpu_seconds_total` |

The full list is in [`values.yaml`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/deploy/helm/kubebolt-agent/values.yaml) (`agent.promRead.matchers`). Append app-custom metrics or a wider node-exporter surface in your values override; an empty list falls back to the same defaults in code. `agent.promRead.cost.enabled=true` appends the OpenCost metric families (requires an OpenCost scraped by that Prometheus).

With `agent.promRead.auth.mode=azureWorkloadIdentity`, the DaemonSet automatically stops emitting node network and load/PSI metrics, because Azure Monitor's own node-exporter scrape already provides them.

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

> 💡 **Resource requests.** Default requests are intentionally small
> (`10m` CPU / `64Mi` memory) so the agent fits even on busy nodes.
> Don't raise these unless you have a specific reason; raising them
> makes Pending pods more likely.

## Upgrade

```bash
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system \
  --reuse-values
```

Cilium users upgrading from a 1.12.x chart: `hubble.enabled` now defaults to
`false`, so add `--set hubble.enabled=true` to keep collecting flows.

## Uninstall

```bash
helm uninstall kubebolt-agent -n kubebolt-system
```

This removes every resource the chart created AND the Helm release
metadata, so `helm list -n kubebolt-system` no longer shows it.
The release namespace itself is preserved.

If the agent was installed through the KubeBolt UI instead of this
chart, `helm uninstall` won't find anything — remove it from the
UI (Administration → Agents & Ingest → Integrations → KubeBolt Agent →
Uninstall) or delete
the resources by name. The other direction works too: KubeBolt's
UI can force-uninstall a chart-installed agent, though the Helm
release metadata will linger until you also run `helm uninstall`
with `--keep-history=false` or delete the release Secret directly.

## Key values

The values below are the ones most commonly set. The full reference, with
comments for every knob, is [`values.yaml`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/deploy/helm/kubebolt-agent/values.yaml).

### Required

| Value | Default | Purpose |
|-------|---------|---------|
| `backendUrl` | _(required)_ | `host:port` of the KubeBolt backend's agent gRPC channel. Rendering fails without it. See [Connecting to the backend](#connecting-to-the-backend). |

### Identity

| Value | Default | Purpose |
|-------|---------|---------|
| `cluster.name` | `""` | Display name for the cluster, sent with the agent's registration (used when the cluster is auto-registered in the cluster switcher). |
| `cluster.id` | `""` | Override the auto-discovered `cluster_id` (kube-system namespace UID). Leave empty unless migrating legacy data. |
| `tenant.id` | `""` | Tenant UUID stamped on every sample (returned when an ingest token is issued). Empty is allowed: the backend stamps the tenant from the ingest token. |

### Image

| Value | Default | Purpose |
|-------|---------|---------|
| `image.repository` | `ghcr.io/clm-cloud-solutions/kubebolt/agent` | Registry path. Override for mirrors or private registries. |
| `image.tag` | `""` | Falls back to `Chart.appVersion`. Pin explicitly in prod. |
| `image.pullPolicy` | `IfNotPresent` | `Always` / `IfNotPresent` / `Never`. Use `Never` for kind/dev where the image is pre-loaded with `kind load`. |
| `imagePullSecrets` | _(unset)_ | For private registries (applied to the DaemonSet). |

### Backend connection: auth and TLS

| Value | Default | Purpose |
|-------|---------|---------|
| `auth.mode` | `disabled` | `disabled`, `tokenreview` (projected ServiceAccount token; backend in the same cluster) or `ingest-token` (bearer token; required for a remote backend). See [Auth](#auth-authmode). |
| `auth.tokenReview.audience` | `kubebolt-backend` | Audience of the projected token; must match the backend's `agentIngest.tokenAudience`. |
| `auth.tokenReview.expirationSeconds` | `3600` | Projected token lifetime (the kubelet renews it). |
| `auth.ingestToken.existingSecret` | `""` | Secret holding the ingest token (required when `auth.mode=ingest-token`). |
| `auth.ingestToken.key` | `token` | Key inside that Secret. |
| `tls.enabled` | `false` | Use TLS on the gRPC dial. Required for a remote backend such as KubeBolt Cloud. |
| `tls.caSecret` / `tls.caKey` | `""` / `ca.crt` | CA bundle to verify the backend certificate. Empty = system trust store (right for public CAs). |
| `tls.clientCertSecret` / `tls.clientCertKey` / `tls.clientKeyKey` | `""` / `tls.crt` / `tls.key` | Client certificate for mTLS. |
| `tls.serverName` | `""` | SNI / verification name override. Empty = host part of `backendUrl`. |

### Permissions and API proxy

| Value | Default | Purpose |
|-------|---------|---------|
| `rbac.mode` | `reader` | `metrics`, `reader` or `operator` — see [Permission tier](#permission-tier--rbacmode). |
| `rbac.create` | `true` | Creates the ClusterRole / Role tiers + bindings and the leader-election Role. Set `false` when RBAC is provisioned externally. |
| `proxy.enabled` | _(derived)_ | Kubernetes API proxy through the agent tunnel. Derived from `rbac.mode` (off for `metrics`, on otherwise); only set it to force it on in `metrics` mode. |
| `serviceAccount.create` | `true` | |
| `serviceAccount.name` | `""` | Empty = derive from release name. Set explicitly when `serviceAccount.create=false`. |
| `serviceAccount.annotations` | `{}` | Cloud identity for Mode C: IRSA (`eks.amazonaws.com/role-arn`), GKE Workload Identity (`iam.gke.io/gcp-service-account`), Azure Workload Identity (`azure.workload.identity/client-id`). |

### Hubble flow collector

| Value | Default | Purpose |
|-------|---------|---------|
| `hubble.enabled` | `false` | Opt-in flow collector for clusters running Cilium with Hubble Relay. When off, the Reliability panels that need flows stay hidden. Check for Cilium with `kubectl -n kube-system get pods -l k8s-app=cilium`. |
| `hubble.relay.address` | `""` | Override the relay target. Default: `hubble-relay.kube-system.svc.cluster.local:80`. |
| `hubble.relay.tls.existingSecret` | `""` | Pre-existing Secret in the release namespace with `ca.crt` (TLS) + optional `tls.crt`/`tls.key` (mTLS). |
| `hubble.relay.tls.serverName` | `""` | SNI / verification hostname. Override when the relay's cert uses a CN/SAN distinct from the dial target. |

### In-agent collectors, exporters and OpenCost

| Value | Default | Purpose |
|-------|---------|---------|
| `collectors.dropNetworkInterfaces` | kernel tunnel devices, `azv*`, `veth*`, `eni*`, `cali*`, `lo` | Interfaces excluded from `container_network_*`. Entries ending in `*` are prefix matches. `[]` keeps every interface. See [Metric footprint](#metric-footprint--cardinality-is-controlled-by-default). |
| `collectors.exporters` | `{}` | Extra Prometheus exporters to scrape, as `name: /metrics URL` (one leader-elected pod scrapes each). Use it for an OpenCost you installed yourself. |
| `agent.deferNodeNetwork` | `false` | Stop emitting `node_network_{receive,transmit}_bytes_total` when an external Prometheus already scrapes node-exporter (avoids 2× counts). Set automatically when the bundled sidecar scrapes node-exporter. |
| `agent.deferNodeStress` | `false` | Stop emitting load average and PSI (`node_load*`, `node_pressure_*`) when node-exporter already ships them — typical with kube-prometheus-stack. Set automatically when the bundled sidecar scrapes node-exporter. |
| `opencost.enabled` | `false` | Install the bundled [OpenCost](https://www.opencost.io/) sub-chart (exporter only, UI off) and point the agent's exporter scraper at it. Run `helm dependency update` first when installing from a source checkout. |
| `opencost.exporterUrl` | `""` | Override the auto-derived scrape URL (`http://<release>-opencost.<ns>.svc:9003/metrics`). |
| `opencost.opencost.*` | `ui.enabled: false` | Pass-through to the upstream OpenCost chart — e.g. `opencost.opencost.prometheus.*` to point it at your Prometheus (needed for complete allocation data). See [`docs/integrations/opencost.md`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/opencost.md). |

### Mode C — read an existing Prometheus (`agent.promRead`)

| Value | Default | Purpose |
|-------|---------|---------|
| `agent.promRead.enabled` | `false` | Deploy the single-replica `promread` Deployment. Mutually exclusive with `scrape.enabled`. |
| `agent.promRead.url` | `""` | Prometheus base URL, e.g. `http://prometheus.monitoring:9090` (required when enabled). |
| `agent.promRead.auth.mode` | `none` | `none`, `basicAuth`, `bearer`, `awsSigV4`, `azureWorkloadIdentity`, `gcpIam`. |
| `agent.promRead.auth.basicAuthUsername` / `basicAuthPassword` | `""` | For `basicAuth`. For production, inject credentials from a Secret via `extraEnv`. |
| `agent.promRead.auth.bearerToken` | `""` | For `bearer`. |
| `agent.promRead.auth.awsRegion` | `""` | AMP workspace region (required for `awsSigV4`). |
| `agent.promRead.pollInterval` / `step` / `lookback` | `30s` / `30s` / `60s` | Query cadence, `query_range` resolution and window. |
| `agent.promRead.matchers` | KSM, load/PSI, disk, network errors, `up`, `process_*` | Metric names to read. See [Topology](#topology--mode-a-vs-mode-c). |
| `agent.promRead.cost.enabled` | `false` | Also read OpenCost metric families from that Prometheus. |
| `agent.promRead.deployment.resources` | `50m`/`64Mi` – `1000m`/`256Mi` | Requests / limits of the `promread` pod. |
| `agent.promRead.deployment.nodeSelector` / `tolerations` / `affinity` | `{}` / `[]` / `{}` | Scheduling of the `promread` pod. |

Setup guides: [self-managed read-only](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/self-managed-prom-readonly.md),
[AWS AMP](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/aws-amp.md),
[Azure Monitor](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/azure-managed-prometheus.md),
[Google Managed Prometheus](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/gcp-managed-prometheus.md).

### Scrape sidecar (`scrape.*`)

| Value | Default | Purpose |
|-------|---------|---------|
| `scrape.enabled` | `false` | Add a vmagent sidecar to each DaemonSet pod. See [Scrape sidecar](#scrape-sidecar-scrapeenabled). |
| `scrape.remoteWriteUrl` | `""` | Where vmagent sends samples — the backend's receiver, e.g. `http://kubebolt-api.kubebolt.svc.cluster.local:8080/api/v1/prom/write` (the backend needs `metrics.remoteWrite.enabled=true`). Required in practice: vmagent crash-loops without a remote_write URL. |
| `scrape.image.repository` / `tag` / `pullPolicy` | `victoriametrics/vmagent` / `v1.153.0-scratch` / `IfNotPresent` | vmagent image (kept on the same VictoriaMetrics release as the backend chart). |
| `scrape.resources` | `10m`/`64Mi` – `200m`/`256Mi` | Sidecar requests / limits. |
| `scrape.extraArgs` | `{}` | Extra vmagent flags, rendered as `-key=value`. |
| `scrape.limits.maxScrapeSize` | `16777216` (16 MiB) | Max scrape body size. |
| `scrape.limits.maxSeriesPerTarget` | `30000` | Max unique series per target per scrape. |
| `scrape.limits.maxSeriesGlobal` | `1000000` | Cap on total active series; `0` disables it. |
| `scrape.metricRelabelConfigs` | keep-list of KubeBolt families + drop `endpoint_id`, `container_id` | Cardinality guardrail; `[]` keeps everything. |
| `scrape.discovery.pods.enabled` | `true` | Scrape pods annotated `prometheus.io/scrape: "true"` on the local node. |
| `scrape.discovery.nodeExporter.enabled` / `labelSelector` / `port` / `path` | `false` / `app.kubernetes.io/name=node-exporter` / `9100` / `/metrics` | Dedicated node-exporter job. kube-prometheus-stack needs `labelSelector=app.kubernetes.io/name=prometheus-node-exporter`. |
| `scrape.discovery.kubeStateMetrics.enabled` / `labelSelector` / `port` / `path` | `false` / `app.kubernetes.io/name=kube-state-metrics` / `8080` / `/metrics` | Dedicated kube-state-metrics job (scraped once, by the agent on KSM's node). |

### Scheduling

| Value | Default | Purpose |
|-------|---------|---------|
| `tolerations` | `[{operator: Exists}]` | Tolerates every taint so the agent lands on every node, including control-plane. Trim if you want to exclude some. |
| `nodeSelector` | `{}` | Pin the agent to specific nodes, e.g. `{kubernetes.io/os: linux}`. |
| `affinity` | `{}` | Full affinity object. Most installs don't need this. |
| `priority.enabled` | `false` | Give the agent a PriorityClass so it can preempt lower-priority pods on full nodes. See [Achieving full node coverage](#achieving-full-node-coverage). |
| `priority.className` | `""` | Existing PriorityClass to use. Empty (with `priority.enabled=true`) = the chart creates `<release>-priority`. |
| `priority.value` | `999999000` | Value of the chart-managed PriorityClass. |
| `priorityClassName` | `""` | Legacy single-string knob; when set it wins over `priority.*`. |

### Resources

| Value | Default | Purpose |
|-------|---------|---------|
| `resources.requests.cpu` | `10m` | DaemonSet agent container. |
| `resources.requests.memory` | `64Mi` | Sized for steady state with Hubble + API proxy + scrape sidecar active. |
| `resources.limits.cpu` | `100m` | |
| `resources.limits.memory` | `128Mi` | Headroom for Hubble flow-parsing bursts. See "Memory and observability" below. |
| `gomemlimit` | `""` _(derived)_ | Overrides the auto-derived `GOMEMLIMIT` (90% of each Go container's `limits.memory`). Set e.g. `"200MiB"` to pin it across all containers. |

The `promread` pod and the vmagent sidecar have their own resources
(`agent.promRead.deployment.resources`, `scrape.resources`).

### Memory and observability

The chart **derives** `GOMEMLIMIT` as 90% of each Go container's
`limits.memory` — the DaemonSet agent and the promread Deployment each
get their own, computed by the `kubebolt-agent.gomemlimit` template
helper. The Go runtime targets this value as a soft total-memory cap —
as the process approaches it, GC runs more frequently and the page
scavenger becomes more aggressive about returning idle heap pages to the
OS. This addresses a memory-retention pattern surfaced during the 1.10
validation campaign: high allocation churn from Hubble flow proto
parsing (~200 MB/min steady-state on an active node) made `HeapSys`
ratchet up without `GOMEMLIMIT` pressure. CPU cost: ~5-10% more time in
GC under load — immaterial against the agent's 5-10m CPU baseline.

Deriving it from the limit keeps the two coupled. A hardcoded
`GOMEMLIMIT` (the chart's earlier 100MiB default) silently *exceeds* the
container limit — and OOM-loops, because the scavenger never triggers
before the cgroup killer — the moment an operator lowers
`resources.limits.memory` below it. To override the derivation, pin an
explicit value across all containers with the `gomemlimit` value:

```yaml
gomemlimit: "200MiB"
```

The agent emits a deep set of `kubebolt_agent_heap_*` and
`kubebolt_agent_*_sys_bytes` gauges so the same investigation pattern
is repeatable in the field. For live heap profiling, enable the pprof
endpoint:

```yaml
extraEnv:
  - name: KUBEBOLT_AGENT_PPROF_ADDR
    value: "127.0.0.1:6060"  # loopback only — port-forward to access
```

Then:

```bash
kubectl port-forward -n <ns> <agent-pod> 6060:6060
go tool pprof http://localhost:6060/debug/pprof/heap
```

### Pod settings and extras

| Value | Default | Purpose |
|-------|---------|---------|
| `logLevel` | `info` | `debug` / `info` / `warn` / `error`. |
| `extraEnv` | `[]` | Additional env vars for the agent containers (DaemonSet and `promread`). `GOMEMLIMIT` is set via `gomemlimit`, not here. |
| `podAnnotations` | `{}` | Extra pod annotations. |
| `podLabels` | `{}` | Extra pod labels. |
| `podAnnotationChecksum` | `true` | Add a checksum of the values to the DaemonSet pod template so `helm upgrade` rolls the pods whenever values change. |
| `podSecurityContext` | `runAsNonRoot: true`, `runAsUser: 65532` | Pod security context. |
| `containerSecurityContext` | read-only root FS, no privilege escalation, all capabilities dropped | Container security context. |
| `nameOverride` / `fullnameOverride` | `""` | Override chart-derived resource names. |

## Connecting to the backend

| Topology | `backendUrl` |
|----------|---------------|
| Backend in Docker Compose on your laptop, agent in Docker Desktop K8s | `host.docker.internal:9090` |
| Backend in-cluster via the main chart (release `kubebolt` in namespace `kubebolt`) | `kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090` |
| Backend behind an internal LoadBalancer | that LB's IP:9090 |
| Backend on a VM reachable from the cluster | that host:9090 |
| Backend exposed through the `kubebolt` chart's Gateway API agent listener | `agent.<your-domain>:443` with `tls.enabled=true` |
| Remote backend, for example [KubeBolt Cloud](https://kubebolt.io) | `agent.kubebolt.io:443` with `tls.enabled=true` and `auth.mode=ingest-token` |

For a remote backend, create the ingest-token Secret first (the token comes
from the backend's **Administration → Agents & Ingest → Agent Tokens**, or from
the Add cluster wizard), then install:

```bash
kubectl create namespace kubebolt-system
kubectl -n kubebolt-system create secret generic kubebolt-agent-token \
  --from-literal=token=<paste-token>

helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system \
  --set backendUrl=agent.kubebolt.io:443 \
  --set tls.enabled=true \
  --set auth.mode=ingest-token \
  --set auth.ingestToken.existingSecret=kubebolt-agent-token \
  --set cluster.name=my-cluster
```

The backend's Add cluster wizard (**Clusters → Add cluster**) builds an
equivalent `helm install` command from your choices.

**Resilience:** the agent reconnects automatically if the backend closes the
stream — a backend restart, a rolling upgrade, a network blip, or the backend's
own stuck-agent recovery. It does not stop shipping, and a saturated send buffer
triggers a reconnect rather than a silent drop, so a transient backend outage
self-heals without touching the agent.

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

Two pieces have to be in place before the agent can talk to the
relay:

1. The agent's `hubble.relay` values pointing at the GKE service +
   mTLS Secret (one helm upgrade — same shape for every path below).
2. A copy of the relay's client-certs Secret living in **the agent's
   release namespace**. K8s won't let a pod mount a Secret from a
   different namespace, so something has to put it there. Pick a
   path based on your install lifecycle:

| Path | Best for | Cert rotation safe? |
|---|---|---|
| **A. Manual `kubectl` mirror** | One-shot demos / quick proofs | ❌ Stale after GKE rotates the certs (manual re-run needed) |
| **B. External Secrets Operator** | Production installs where you already run ESO for other secrets | ✅ Picks up cert rotation automatically |
| **C. Reflector** | Lightweight production installs where you want the controller as a single annotation on the source Secret | ✅ Picks up cert rotation automatically |

### Path A — Manual mirror with `kubectl`

The fastest path for a demo or to confirm KubeBolt can read flows
from the relay. Re-run the mirror command if GKE rotates the certs
on its own cadence.

```bash
# 1. Mirror the relay client-certs Secret into the agent's namespace.
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

### Path B — External Secrets Operator

Recommended when you already run [External Secrets Operator (ESO)](https://external-secrets.io/)
in the cluster (most production clusters managing secrets at scale
do). ESO's `ClusterSecretStore` pointed at the Kubernetes API as a
source can pull the cert into the agent's namespace and keep it in
sync with the upstream automatically.

```yaml
# 1. ClusterSecretStore — reads from the same cluster's own
#    apiserver (no external vault needed, just RBAC).
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: in-cluster-kubernetes
spec:
  provider:
    kubernetes:
      remoteNamespace: gke-managed-dpv2-observability
      server:
        caProvider:
          type: ConfigMap
          name: kube-root-ca.crt
          namespace: default
          key: ca.crt
      auth:
        serviceAccount:
          name: external-secrets-sa  # ESO SA, must have get/list/watch on Secrets in the source ns
---
# 2. ExternalSecret — declares "I want this Secret mirrored here".
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: hubble-relay-client-certs
  namespace: kubebolt-system
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: in-cluster-kubernetes
    kind: ClusterSecretStore
  target:
    name: hubble-relay-client-certs
  dataFrom:
    - extract:
        key: hubble-relay-client-certs
```

The ESO SA needs `get / list / watch` on the `Secret`
`hubble-relay-client-certs` in `gke-managed-dpv2-observability` —
add a `Role` + `RoleBinding` for that namespace. The agent helm
install is unchanged from Path A's step 2.

### Path C — Reflector

[Reflector](https://github.com/emberstack/kubernetes-reflector) is a
~10MB controller that watches Secrets/ConfigMaps with specific
annotations and pushes copies into target namespaces. Lighter than
ESO if Hubble's relay cert is the only thing you need to mirror.

```bash
# 1. Install Reflector (one-time, cluster-wide).
helm repo add emberstack https://emberstack.github.io/helm-charts
helm upgrade --install reflector emberstack/reflector \
  --namespace kube-system

# 2. Annotate the source Secret so Reflector picks it up.
#    Allowed-namespaces uses a regex; pin to the agent's ns or
#    widen the pattern if you run the agent in multiple namespaces.
kubectl annotate secret -n gke-managed-dpv2-observability \
  hubble-relay-client-certs \
  reflector.v1.k8s.emberstack.com/reflection-allowed=true \
  reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces=kubebolt-system \
  reflector.v1.k8s.emberstack.com/reflection-auto-enabled=true \
  reflector.v1.k8s.emberstack.com/reflection-auto-namespaces=kubebolt-system
```

The agent helm install is unchanged from Path A's step 2. Reflector
keeps the mirrored Secret in sync with the source whenever GKE
rotates the certs.

### Picking between B and C

| | ESO (Path B) | Reflector (Path C) |
|---|---|---|
| Already-in-cluster runtime cost | Free if ESO already runs | ~10 MB controller, single deployment |
| Config surface | 2 YAML objects (ClusterSecretStore + ExternalSecret) | 4 annotations on the source Secret |
| Source choice | Pluggable (Vault, AWS Secrets Manager, GCP Secret Manager, K8s itself) | Kubernetes Secrets only |
| Best fit | You already use ESO for other workloads | Hubble cert is your only cross-namespace mirror need |

For one-off Hubble mirroring without other ESO use cases, Reflector
is the smaller lift. For environments that already have ESO running,
Path B avoids adding a second secret-mirror controller.

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
KubeBolt backend. Default off. It needs two settings besides
`scrape.enabled=true`: `scrape.remoteWriteUrl` pointing at the backend's
receiver (for example
`http://kubebolt-api.kubebolt.svc.cluster.local:8080/api/v1/prom/write`), and
`metrics.remoteWrite.enabled=true` on the `kubebolt` chart (or the
remote_write toggle in Administration → Agents & Ingest → Configuration). With
`auth.mode=ingest-token`, vmagent reuses the same token. It cannot be combined
with `agent.promRead.enabled=true`.

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
which match by `app.kubernetes.io/name=...` label. **Duplication is
handled for you:** kube-state-metrics is a single cluster-wide pod, so a
naive DaemonSet would scrape it from every node. The dedicated KSM job
carries a `node_name == %{NODE_NAME}` keep filter, so **only the agent
co-located with KSM scrapes it — 1× ingest, not N×** — and it still
returns the full cluster's `kube_*` set (KSM is cluster-scoped, one scrape
covers everything). No VictoriaMetrics `--dedup` flag required; the
duplication is prevented at scrape time. node-exporter is a DaemonSet, so
each agent scrapes only its own node's copy (same node filter). See
[Metric footprint](#metric-footprint--cardinality-is-controlled-by-default)
for how the agent keeps its series count low overall.

Quick start + full config reference + troubleshooting (including the
`kube-prometheus-stack` recipe with `prometheus-node-exporter.nameOverride`):
[`docs/agent-scraping.md`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/agent-scraping.md).

## Metric footprint — cardinality is controlled by default

Active series is the unit that drives storage and cost, so the agent ships with
three cardinality guards **on by default**. There's nothing to configure for
them to work — they're documented here so you know what's happening and how to
tune them.

**1. Family allowlist (scrape path).** When the vmagent sidecar is enabled,
`scrape.metricRelabelConfigs` defaults to a keep-rule for the metric families
KubeBolt actually queries — `kube_*`, `node_*`, `container_*`, `kubelet_*`,
`pod_flow_*`, `pod_dns_*`, `hubble_*`, `kubebolt_*`, `up`, `process_*` — and
drops everything else, chiefly the app metrics a `prometheus.io/scrape`
annotation would otherwise sweep in (GitLab, sidekiq, client-library
histograms). Validated on a production cluster: **96k → 19k active series
(−80%)**, zero KubeBolt metrics lost. It also drops two high-cardinality unused
labels (`endpoint_id`, `container_id`). Set `scrape.metricRelabelConfigs: []` to
keep everything.

**2. KSM single-scrape.** kube-state-metrics is one cluster-wide pod; a per-node
`node_name == %{NODE_NAME}` keep filter on the dedicated KSM job means only the
co-located agent scrapes it — **1× ingest, not N×** — with no VictoriaMetrics
`--dedup` needed (see the Scrape sidecar section above).

**3. Network-interface filter (Mode A path).** cAdvisor reports
`container_network_*` per interface, and every pod's network namespace carries
at least one veth peer (`azv<hash>` on Azure CNI, `veth<hash>` on kind and many
others, `eni<hash>` on AWS VPC CNI, `cali<hash>` on Calico) plus loopback —
each a distinct `interface` label value. On a 2k-pod cluster the label set
exceeds 5,000 unique values and dominates active series (~85% measured on AKS).
Kernels that load the tunnel modules (local kind, some bare-metal) add the
always-zero `sit0`/`gre0`/`tunl0` family on top. `collectors.dropNetworkInterfaces`
supports **exact match** and **prefix match** (entries ending in `*`); the
defaults cover the mainstream CNI patterns, the kernel tunnel devices, and
loopback. Per-pod traffic stays visible through the pod's own `eth0`, and the
node-level view (`enP*`, `eth0`, `cilium_host`, …) is untouched. Set it to `[]`
to keep every interface.

The net effect: **a fresh install consumes far fewer series than a stock
Prometheus scrape would** — the annotation firehose, the KSM N× duplication, and
the phantom-interface bloat are all handled without any tuning on your part.
