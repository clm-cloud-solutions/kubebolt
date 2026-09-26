# Deployment Scenarios

This document maps **what the customer already has** in their cluster
to **what they need to install for KubeBolt**, and what features stay
available or get degraded based on each choice.

> **Three metric intake paths.** KubeBolt's VictoriaMetrics can be fed
> by (A) the kubebolt-agent — kubelet/cAdvisor + Hubble over gRPC, plus
> an optional vmagent scrape sidecar; (B) an existing Prometheus
> pushing `remote_write` to the backend's `/api/v1/prom/write` receiver
> ([`integrations/prometheus.md`](integrations/prometheus.md)); or
> (C) the agent reading an existing Prometheus, including managed ones
> (Scenario 5). The scenarios below pick the right mix for what the
> cluster already runs.

---

## How to use this document

1. Run the **Pre-flight checklist** against the target cluster.
   Those `kubectl` commands tell you which scenario you're in.
2. Cross-reference the **Feature availability matrix** to set
   expectations with the customer: what works out of the box,
   what requires extra components, what stays unavailable.
3. Follow the scenario-specific **Install recipe**. Each one lists
   helm commands, values overrides, and post-install validation.
4. Skim **Operational trade-offs** if the customer asks "why two
   scrapers?" or "what about cardinality?" — there's a one-line
   answer for each common pushback.

---

## The principle: what KubeBolt actually consumes

KubeBolt is a **data consumer + UI**. It has no opinion about how
the cluster is monitored before it's installed, and no opinion about
how the customer's existing monitoring stack stays in place after.

### Sources, ranked by criticality

| Source | Always required | Provided by | Lose if missing |
|---|---|---|---|
| **K8s apiserver** | ✅ yes | the cluster | Everything. KubeBolt is a Kubernetes UI. |
| **Metrics Server** | recommended | usually pre-installed on managed K8s (EKS, GKE, AKS); separate install on kubeadm/kind | Live commitment bars on Overview ("CPU 45%") show "no data" — historical CPU/Mem still works via the agent. |
| **kubebolt-agent gRPC channel** | ✅ when historical data is wanted | the agent chart itself, ships kubelet/cAdvisor + Hubble flow collector | Capacity trends (CPU/Mem/Network/Filesystem of workloads), Reliability L7 panels. The UI degrades to "live-only" — current resources still listable, just no history. |
| **kube-state-metrics (KSM)** | optional but high-value | customer's existing prom-stack, or `prometheus-community/kube-state-metrics` standalone | Pod restart history, OOMKill Capacity panel, Service endpoint health column, Namespace quota gauge — see matrix below. |
| **node-exporter** | optional | customer's existing prom-stack, or `prometheus-community/prometheus-node-exporter` standalone | Node load avg + PSI charts, per-mountpoint filesystem chart. The agent ships *one aggregate* `node_fs_used_bytes` — fallback exists, no full breakdown. |
| **Cilium + Hubble** | optional (gates one whole sub-tab) | customer's CNI choice | Reliability sub-tab on the Dashboard (error rate, traffic, latency, drops, hotspots). |
| **Prometheus server** | optional | customer's existing stack (self-managed, AMP, Azure Managed Prometheus, GMP) | Nothing — it's an alternative intake. It can `remote_write` into KubeBolt (no dual scrape in scenarios 1+2) or be read by the agent (Scenario 5). |

### What Grafana means for KubeBolt

**Nothing.** KubeBolt ships its own UI. Grafana is a parallel
visualization layer the customer can keep running alongside —
neither system reads or writes the other's storage. Mentioned in
scenarios below only to qualify the customer's existing setup, not
because it changes the KubeBolt install.

### Feature → data source map

The reference frame for every "what do I lose if I skip X" question:

| Category | Feature | Source |
|---|---|---|
| **A. Operational core** | Cluster Overview, all 23 resource list/detail views, YAML/describe, exec, logs, files, port-forward, Cluster Map, Insights engine (24 rules including service-no-endpoints + oom-killed + NetworkPolicy coverage gaps), namespace count etc. | apiserver (informer state) |
| **B. Live commitment** | Overview's "CPU 45% / Mem 65%" bars on cluster + node cards | Metrics Server (instant query) |
| **C. Workload trends** | Capacity page CPU/Mem/Network/Filesystem charts, TopWorkloadsCpu, RightSizingPanel | agent gRPC (cAdvisor + kubelet) → bundled VM |
| **D. L7 traffic** | Reliability sub-tab (error rate, top traffic, top latency, network drops, error hotspots) | agent gRPC (Hubble) → bundled VM |
| **E. Cluster-state enrichments** | Pod restart history sparkline, OOMKill Capacity panel, Service endpoint health column, Namespace quota gauge | KSM → vmagent → bundled VM |
| **F. Node OS enrichments** | Load average + PSI, per-mountpoint filesystem chart | node-exporter → vmagent → bundled VM. _Note:_ basic node CPU/Memory/Network/Filesystem (single aggregate per node) is **always available** via the agent's kubelet+cAdvisor path — node-exporter only adds Load+PSI and the per-mountpoint breakdown. The F category is "the extras," not "all node OS data." |
| **G. Recent deploys overlay** | Capacity charts deploy markers, Recent Deploys table | apiserver (informer ReplicaSet creation timestamps). _Note:_ **always populated** when any Deployment exists — even greenfield clusters. Empty only when the cluster has no Deployments at all. |
| **H. Resource actions** | Restart, scale, delete, edit YAML, set image, set resources, set env | apiserver (mutating verbs through agent or direct kubeconfig) |

A, B, G, H depend on KubeBolt's own install. C and D depend on the
agent (which is part of the KubeBolt install). E and F are what
this document is really about — the difference between scenarios.

---

## Pre-flight checklist

Run these against the target cluster **before** picking a scenario.
The output tells you which scenario the customer is actually in
(don't trust what they say — verify).

### 1. K8s version and capability

```bash
kubectl version --short 2>&1 | grep Server
# Expected: v1.24+ for full feature support (EndpointSlice GA,
# CronJob v1, etc.). v1.20+ works with feature degradation.

kubectl api-resources --verbs=list | grep -E "endpointslices|leases|customresourcedefinitions"
# Expected: discovery.k8s.io/v1/endpointslices (used by service-
# no-endpoints insight). Older clusters fall back to v1.Endpoints
# which KubeBolt doesn't currently watch — that insight degrades.
```

### 2. Metrics Server presence

```bash
kubectl get apiservice v1beta1.metrics.k8s.io -o jsonpath='{.status.conditions[?(@.type=="Available")].status}'
# Expected: "True" → Metrics Server reachable.
# If missing or False:
#   - Managed K8s (EKS, GKE, AKS): usually pre-installed. Re-enable
#     via cloud-specific path.
#   - kubeadm / kind / k3d: install separately:
#       kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
#       (kind/k3d may need --kubelet-insecure-tls flag)
```

Without Metrics Server the Overview live bars say "no data"; everything
else keeps working. Not a blocker but lose explainable real-time CPU/Mem
on cards.

### 3. kube-state-metrics presence + discovery convention

```bash
# Does KSM exist?
kubectl get pods -A -l app.kubernetes.io/name=kube-state-metrics
# Other naming conventions seen in the wild (some forks):
kubectl get pods -A -l 'app=kube-state-metrics'
```

If found, check WHICH discovery path the kubebolt-agent's vmagent
should use:

```bash
NS=$(kubectl get pods -A -l app.kubernetes.io/name=kube-state-metrics \
       -o jsonpath='{.items[0].metadata.namespace}')
POD=$(kubectl get pods -A -l app.kubernetes.io/name=kube-state-metrics \
        -o jsonpath='{.items[0].metadata.name}')

# Annotation-driven discovery — preferred path (single-scrape via
# kubernetes-pods job's per-node filter):
kubectl get pod -n $NS $POD -o jsonpath='{.metadata.annotations.prometheus\.io/scrape}'
# Expected: "true" → annotation-driven path works.
# Empty → ServiceMonitor-driven (no annotation) → use dedicated job.

# How many containerPorts does it expose (port-fanout risk):
kubectl get pod -n $NS $POD -o jsonpath='{range .spec.containers[*].ports[*]}{.name}={.containerPort} {end}'
# Default KSM 2.x: http=8080 metrics=8081 (two ports).
# The agent chart ships a containerPort filter so this won't produce
# duplicate-target warnings — but worth knowing the port for the
# dedicated job values (scrape.discovery.kubeStateMetrics.port).
```

### 4. node-exporter presence + label convention

```bash
# Default convention — bundled or prometheus-community standalone:
kubectl get pods -A -l app.kubernetes.io/name=node-exporter -o wide

# kube-prometheus-stack convention (different name, same workload):
kubectl get pods -A -l app.kubernetes.io/name=prometheus-node-exporter -o wide

# If the install uses a non-default name, override the agent's
# selector via:
#   scrape.discovery.nodeExporter.labelSelector="app.kubernetes.io/name=<actual-name>"
```

If neither labelset matches: there's no node-exporter installed,
and **category F features are unavailable** unless one is added.

### 5. Cilium / Hubble presence (Reliability sub-tab gate)

```bash
kubectl get pods -n kube-system -l k8s-app=cilium 2>&1 | head -3
kubectl get pods -n kube-system -l k8s-app=hubble-relay 2>&1 | head -3
```

- Cilium without Hubble Relay: agent still works for everything but
  flow events. Reliability sub-tab hidden.
- Hubble Relay running: agent automatically collects flows. The UI
  detects via `useHubbleAvailable` probe (`count(pod_flow_http_requests_total{source="hubble"})`)
  and shows the sub-tab when samples arrive.

### 6. Prometheus Operator CRDs (ServiceMonitor) — proxy for scenario type

```bash
kubectl get crd | grep monitoring.coreos.com
# Expected output (any of these means Operator-driven Prom):
#   servicemonitors.monitoring.coreos.com
#   podmonitors.monitoring.coreos.com
#   prometheuses.monitoring.coreos.com
```

If present → customer's Prom uses ServiceMonitor/PodMonitor instead of
annotations. The agent must use **dedicated scrape jobs** for KSM
and node-exporter (no annotation-driven path).

If absent and customer has Prom → they're hand-rolling configs. KSM/NE
likely carry annotations. Confirm via step 3.

### 7. Agent RBAC tier needed

```bash
# Will the agent's ServiceAccount be allowed cluster-admin (operator
# mode), read-only (reader mode), or metrics-only (metrics mode)?
# Ask the customer. Defaults vary:
#   metrics  — get/list pods+nodes, kubelet stats; no resource changes
#   reader   — full list/get on all resources (read-only)
#   operator — adds the mutating verbs needed for restart/scale/edit
#              and the SPDY exec/portforward for terminal+files
```

The choice gates **category H (resource actions)**. Reader mode hides
the action buttons in the UI; metrics mode hides those plus
exec/portforward/files. None of A-G are affected.

See [`docs/agent-scraping.md`](agent-scraping.md) for the agent's full
permission model.

---

## Scenario 1 — Customer has Prometheus + Grafana

**Profile:** typical `kube-prometheus-stack` install. Customer has:
- Prometheus server (ServiceMonitor-driven)
- kube-state-metrics
- node-exporter (probably named `prometheus-node-exporter`)
- Alertmanager
- Grafana (irrelevant to KubeBolt)
- Prometheus Operator + CRDs

### Pre-flight confirmation
- [ ] Step 6 returns CRDs → ServiceMonitor-driven. Use dedicated jobs.
- [ ] Step 3 KSM annotation `prometheus.io/scrape` is empty → confirm dedicated job.
- [ ] Step 4 node-exporter labeled `prometheus-node-exporter` → labelSelector override required.

### Install recipe
```bash
# 1. Backend (UI + bundled VictoriaMetrics)
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  -n kubebolt --create-namespace \
  --set metrics.remoteWrite.enabled=true \
  --set metrics.remoteWrite.authMode=disabled
  # Production: switch authMode to permissive/enforced + provision
  # an ingest token via the integration's "Generate token" flow.

# 2. Agent with both dedicated scrape jobs, ServiceMonitor-compatible
helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt-agent --create-namespace \
  --set scrape.enabled=true \
  --set scrape.discovery.nodeExporter.enabled=true \
  --set scrape.discovery.nodeExporter.labelSelector="app.kubernetes.io/name=prometheus-node-exporter" \
  --set scrape.discovery.kubeStateMetrics.enabled=true \
  --set scrape.remoteWriteUrl=http://kubebolt.kubebolt.svc.cluster.local/api/v1/prom/write \
  --set backendUrl=kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090 \
  --set rbac.mode=operator   # or reader / metrics per step 7
```

`rbac.mode=operator` makes the agent's ServiceAccount effectively
cluster-admin. Pair it with channel auth — for example
`auth.mode=tokenreview` on the agent and `agentIngest.authMode=enforced`
on the backend — see the agent chart README's
[Auth section](../deploy/helm/kubebolt-agent/README.md#auth-authmode).
The same applies to the other recipes below.

### What's enabled
**100%** of KubeBolt features (categories A through H assuming
operator RBAC mode and Cilium present for D).

### What's lost
Nothing functional. Operational cost: two scrapers (customer's Prom
+ kubebolt-agent vmagent) pull from the same KSM and node-exporter
endpoints. The agent scrapes **KSM exactly once** (a per-node keep
filter on the dedicated job — only the vmagent sharing a node with the
KSM pod scrapes it, no N× duplication from the DaemonSet) and
node-exporter per-node (each agent scrapes its own node's copy). The
bundled VM dedups the agent-vs-customer-Prom overlap at write time via
`--dedup.minScrapeInterval=30s`.

### Alternative: let the customer's Prometheus push
Instead of the agent's scrape sidecar, the customer can add a
`remote_write` block to their Prometheus pointing at KubeBolt's
receiver (`/api/v1/prom/write`, with `metrics.remoteWrite.enabled=true`
on the backend) and keep `scrape.enabled=false` on the agent.
Single-source intake, zero scrape duplication, customer's Prom
retention preserved. Use `metrics.remoteWrite.authMode=enforced` with
an ingest token in production. Full recipe:
[`integrations/prometheus.md`](integrations/prometheus.md). If the
customer can't edit their Prometheus config, use Scenario 5 instead.

With the sidecar path, the extra scrape adds one `/metrics` request per
target per cycle (KSM once, from its node; node-exporter once per node).
Negligible; worth flagging only if the customer asks.

---

## Scenario 2 — Customer has Prometheus only

Two sub-variants by setup style:

### 2a. Annotation-driven Prom (hand-rolled)

**Profile:** customer wrote their own Prom config with
`kubernetes_sd_configs` and `prometheus.io/scrape` annotations on
target pods. No Prometheus Operator.

#### Pre-flight confirmation
- [ ] Step 6 returns no CRDs.
- [ ] Step 3 KSM annotation is `"true"`.
- [ ] Step 4 node-exporter labeled `node-exporter` (default).

#### Install recipe
```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  -n kubebolt --create-namespace \
  --set metrics.remoteWrite.enabled=true \
  --set metrics.remoteWrite.authMode=disabled

helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt-agent --create-namespace \
  --set scrape.enabled=true \
  # Dedicated jobs OFF — kubernetes-pods job will discover KSM and
  # node-exporter via their prometheus.io/scrape annotation.
  --set scrape.remoteWriteUrl=http://kubebolt.kubebolt.svc.cluster.local/api/v1/prom/write \
  --set backendUrl=kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090 \
  --set rbac.mode=operator
```

#### What's enabled / lost
Same as scenario 1: 100% of features. **Better operationally** —
the `kubernetes-pods` job's per-node filter scrapes KSM exactly once
(only the vmagent sharing a node with the KSM pod scrapes it); no
need for VM-side dedup to collapse duplicates.

### 2b. Partial Prom (no KSM, or no node-exporter, or both)

**Profile:** customer has Prom but didn't install KSM and/or node-exporter
because their use case didn't need them.

#### Pre-flight confirmation
- [ ] Step 3 OR step 4 returns no pods.

#### Install recipe (customer chooses)

Option A — install the missing piece as a sidecar to KubeBolt:
```bash
# Add KSM if missing:
helm install kube-state-metrics prometheus-community/kube-state-metrics \
  -n kubebolt --reuse-values
# Add node-exporter if missing:
helm install node-exporter prometheus-community/prometheus-node-exporter \
  -n kubebolt --set nameOverride=node-exporter
# Then install the agent as scenario 2a (annotation discovery works
# because the upstream charts include the prometheus.io/scrape
# annotation by default).
```

Option B — skip and accept the degraded view. Install the agent with
`scrape.enabled=false` (only the gRPC channel runs) plus all the
non-scrape features still work.

#### What's enabled / lost
- Categories A, B, C, D (if Cilium), G, H — all unaffected.
- Without KSM: lose category **E** entirely (restart history, OOMKill
  Capacity panel, Service endpoint column, Namespace quota gauge).
  → Note: the OOMKill *badge* on Pod overview still works (uses informer
  state), only the Capacity-page panel needs KSM.
  → The service-no-endpoints *insight rule* still works (uses the
  informer's EndpointSlices), only the UI column needs KSM.
- Without node-exporter: lose category **F** (load + PSI,
  per-mountpoint filesystem).
  → The agent's basic `node_fs_used_bytes` (one aggregate) keeps a
  fallback panel on the Node detail Monitor tab.

---

## Scenario 3 — Customer has neither Prometheus nor Grafana

**Profile:** greenfield cluster, or one whose monitoring story has
been "kubectl logs and prayers." Often a fresh managed-K8s deployment
about to grow.

### Pre-flight confirmation
- [ ] Step 3 returns no pods.
- [ ] Step 4 returns no pods.
- [ ] Step 6 returns no CRDs.
- [ ] Step 2 — verify Metrics Server (often pre-installed on managed
      K8s but not on kind/kubeadm).

### Minimal install (operational core only)
```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  -n kubebolt --create-namespace
  # Note: remoteWrite stays OFF — nothing is going to send to it.

helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt-agent --create-namespace \
  --set backendUrl=kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090 \
  --set rbac.mode=operator
  # scrape.enabled stays false — there's nothing to scrape yet.
```

### Full install (all features)
Add KSM + node-exporter as sidecar charts:
```bash
# 1. KSM
helm install kube-state-metrics prometheus-community/kube-state-metrics \
  -n kubebolt --create-namespace

# 2. node-exporter
helm install node-exporter prometheus-community/prometheus-node-exporter \
  -n kubebolt --set nameOverride=node-exporter

# 3. Backend + agent — same as scenario 2a recipe with
# scrape.enabled=true. Annotation discovery picks both up.
```

### What's enabled / lost — minimal install
- ✅ A: Operational core complete (lists, details, Map, Insights with all 24 rules, exec, logs, files, port-forward).
- ⚠️ B: Live bars depend on Metrics Server existing.
- ✅ C: Capacity workload trends (CPU/Mem/Network/Filesystem) — the agent ships kubelet/cAdvisor pull built-in, no Prom needed.
- ✅ D: Reliability if Cilium+Hubble is installed.
- ❌ E: No KSM → lose all 4 cluster-state enrichments listed in scenario 2b above.
- ⚠️ F: No node-exporter → keep **F.0** (single-aggregate node CPU/Mem/Net/Filesystem from the agent's kubelet path), lose **F.1** (load + PSI) and **F.2** (per-mountpoint breakdown).
- ✅ G: Recent deploys overlay — populated as soon as the cluster runs any Deployment (apiserver ReplicaSet history, no Prom).
- ✅ H: Resource actions (operator RBAC).

### What's enabled / lost — full install
**100%** of features. Same as scenarios 1 / 2a.

---

## Feature availability matrix

A glance-level summary. ✅ = works; ⚠️ = partial / degraded;
❌ = unavailable.

| Feature | Scen 1 | Scen 2a | Scen 2b (no KSM) | Scen 2b (no NE) | Scen 3 minimal | Scen 3 full |
|---|:---:|:---:|:---:|:---:|:---:|:---:|
| **A.** Overview, list/detail, Map, Insights, exec, logs, files, portfwd | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **B.** Overview live commitment bars (Metrics Server) | ✅ | ✅ | ✅ | ✅ | ⚠️ | ⚠️ |
| **C.** Capacity workload trends | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **D.** Reliability L7 (gated on Hubble) | ✅* | ✅* | ✅* | ✅* | ✅* | ✅* |
| **E.1.** Pod restart history sparkline | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ |
| **E.2.** OOMKill Capacity panel | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ |
| **E.3.** OOMKill Pod overview badge | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **E.4.** Service endpoint UI column | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ |
| **E.5.** Service no-endpoints insight rule | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **E.6.** Namespace quota gauge | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ |
| **F.0.** Basic node CPU/Mem/Net/Filesystem (single aggregate) | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **F.1.** Node load avg + PSI | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ |
| **F.2.** Per-mountpoint filesystem chart | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ |
| **G.** Recent deploys overlay (any Deployment present) | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **H.** Resource actions (operator RBAC) | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

\* Reliability sub-tab appears only when Cilium+Hubble is installed
and emitting flow samples.

---

## Scenario 4 — Single-container CLI (evaluation / demo / dev)

**Profile:** evaluator or operator who wants to **try KubeBolt without
deploying it to a cluster**. Use case: a 30-second `docker run`
to point at any cluster reachable from your laptop via `~/.kube/config`.
Not a production deployment pattern — no agent, no TSDB, no
auth-by-default — but the right path when "just show me the UI"
beats "stand up the full stack."

### Pre-flight confirmation
- [ ] `~/.kube/config` resolves to at least one cluster you have RBAC
      to read from.
- [ ] Docker daemon running on your laptop.

### Install recipe

```bash
# Pulls the published OCI single-container image (api + web + embedded
# frontend, served on :3000). Each release has a matching
# single-container image tagged with its version (e.g. 2.1.0);
# `:latest` tracks the most recent stable.
docker run --rm -p 3000:3000 \
  -v ~/.kube/config:/kubeconfig:ro -e KUBECONFIG=/kubeconfig \
  -e KUBEBOLT_AUTH_ENABLED=false \
  ghcr.io/clm-cloud-solutions/kubebolt:latest

# Open http://localhost:3000
```

The container runs as a non-root user and reads the kubeconfig named by
`KUBECONFIG` at boot (on Linux, the mounted file must be readable by that
user), switches between
contexts via the UI (every kubeconfig context shows up in the cluster
selector), and uses your local kubeconfig credentials for all
apiserver calls.

### What's enabled
- ✅ **A** Operational core — lists, details, Map, Insights (24 rules),
  exec / logs / port-forward / files (all run through your local
  kubeconfig).
- ⚠️ **B** Live commitment bars — depend on Metrics Server in the
  target cluster.
- ❌ **C** Capacity workload trends (CPU/Mem/Network/Filesystem
  history) — no TSDB bundled, no agent shipping samples.
- ❌ **D** Reliability sub-tab — no agent, no Hubble flow ingest.
- ❌ **E** Cluster-state enrichments — no KSM scrape, no VM
  to store the time series.
- ❌ **F** Node OS enrichments — no node-exporter scrape, no VM.
- ✅ **G** Recent deploys overlay — apiserver-only, no TSDB needed.
- ✅ **H** Resource actions — your local kubeconfig credentials.

In short: A + G + H work full-speed. B partial. C/D/E/F unavailable
because there's no TSDB.

### When to graduate to Scenario 1 / 2 / 3
The moment you want historical CPU/Mem/Network trends or the
Reliability sub-tab, the single-container path stops being
sufficient — that's the moment to switch to a Helm install per
Scenarios 1-3 above. The `docker run` evaluator workflow is
designed as the "before" of a longer journey, not the destination.

### Limitations
- **No multi-user auth** (the recipe sets `KUBEBOLT_AUTH_ENABLED=false`
  for zero-config UX — anyone with access to `localhost:3000` is
  effectively cluster-admin against your kubeconfig).
- **No persistence** unless you mount a volume at `/data` — that's
  where the embedded BoltDB (users, Copilot conversations, insight
  history) lives, and the `--rm` recipe above discards it.
- **Performance ceiling** — single binary, single-process, embedded
  frontend; fine for evaluating against clusters with hundreds of pods
  but not the right shape for fleet-scale production use.

---

## Scenario 5 — Customer has a Prometheus KubeBolt can only read (Mode C)

**Profile:** the cluster's metrics already live in a Prometheus that
can't push to KubeBolt, or whose config the customer can't change:

- a **managed, query-only Prometheus** — Amazon Managed Service for
  Prometheus (AMP), Azure Monitor managed Prometheus, Google Managed
  Prometheus (GMP);
- a **self-managed Prometheus** whose change management blocks adding a
  `remote_write` block.

### Pre-flight confirmation
- [ ] You have the Prometheus query URL and a credential the agent can
      use (none / basic / bearer, or the cloud identity: AWS SigV4,
      Azure Workload Identity, GCP IAM).
- [ ] That Prometheus actually holds kube-state-metrics and
      node-exporter series (step 3 and step 4, or query it directly).

### Install recipe
The backend install is the same as Scenario 3 (remote_write can stay
off). The agent gets `agent.promRead.enabled=true`, which renders a
**separate single-replica Deployment** that polls the Prometheus
(`/api/v1/query_range`) and forwards samples over the agent's gRPC
channel. The agent DaemonSet (kubelet/cAdvisor + Hubble) keeps running
next to it.

```bash
helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt-agent --create-namespace \
  --set backendUrl=kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090 \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url=<prometheus-query-url> \
  --set agent.promRead.auth.mode=none   # basicAuth | bearer | awsSigV4 | azureWorkloadIdentity | gcpIam
```

`scrape.enabled` and `agent.promRead.enabled` are mutually exclusive —
the chart refuses to render with both on. Per-source setup, including
the cloud identity wiring:

- AWS AMP: [`integrations/aws-amp.md`](integrations/aws-amp.md)
- Azure Monitor managed Prometheus: [`integrations/azure-managed-prometheus.md`](integrations/azure-managed-prometheus.md)
- Google Managed Prometheus: [`integrations/gcp-managed-prometheus.md`](integrations/gcp-managed-prometheus.md)
- Self-managed Prometheus, read-only: [`integrations/self-managed-prom-readonly.md`](integrations/self-managed-prom-readonly.md)

### What's enabled / lost
- A, B, C, D (if Cilium), G, H — same as Scenario 3; they don't depend
  on the Prometheus.
- The default `agent.promRead.matchers` list is deliberately narrow: KSM
  core state (pods, deployments, statefulsets, daemonsets, nodes),
  node load + PSI, disk I/O, network errors, `up` / `process_*`. That
  covers **F.1** (load + PSI).
- **E** panels and **F.2** (per-mountpoint filesystem) query series
  that are not in the default list — `kube_pod_container_status_restarts_total`,
  `kube_endpoint*` / `kube_endpointslice*`, `kube_resourcequota`,
  `node_filesystem_*`. If the source Prometheus has them, add them to
  `agent.promRead.matchers` — overriding the value replaces the list,
  so copy the defaults from the chart's `values.yaml` and append.

---

## Decision tree (quick path for new customer)

```
Just trying it out / 30-second demo with no cluster deploy?
└── YES → Scenario 4 (docker run, single container)

Committing to a deploy:

  Does the customer's cluster already have Prometheus running?
  ├── YES
  │   ├── Managed/query-only (AMP, Azure, GMP) or config can't change?
  │   │   └── YES → Scenario 5 (agent reads it)
  │   ├── Is it kube-prometheus-stack / Operator-driven?  (CRDs present)
  │   │   └── YES → Scenario 1
  │   └── NO (hand-rolled or annotation-driven Prom)
  │       ├── KSM AND node-exporter installed?  → Scenario 2a
  │       └── One or both missing                → Scenario 2b
  └── NO
      └── Want full enrichments?
          ├── YES → Scenario 3 full (add KSM + node-exporter sidechart)
          └── NO  → Scenario 3 minimal (only kubebolt + agent)
```

---

## Operational trade-offs

### Scrape duplication (scenarios 1, 2)

The customer's Prom and the kubebolt-agent vmagent both pull from
KSM/node-exporter. Two scrapers per target.
- Bandwidth cost: ~one extra `/metrics` HTTP request per target per
  30s. Negligible in any cluster.
- KSM cardinality: the agent scrapes KSM exactly once (a per-node keep filter
  on the dedicated job — no N× duplication from the DaemonSet), so the customer's
  Prom is the only other source. The bundled VM dedups those two at write time
  via `--dedup.minScrapeInterval=30s`. No inflation.
- Customer's Prom retention is untouched — they keep their own
  history independent of KubeBolt's VM.

**Avoidable** by having the customer's Prom `remote_write` to KubeBolt
and disabling the agent's scrape sidecar (see Scenario 1's alternative),
or by reading their Prom instead (Scenario 5).

### Series cardinality on bundled VM

VM footprint per scenario, **with the default metric-family allowlist**
(`scrape.metricRelabelConfigs`, on by default — it keeps only KubeBolt-consumed
families and drops the app metrics an annotation scrape would sweep in; see the
agent chart's "Metric footprint" section). Validated on a production cluster:
the allowlist took a real workload from **96k → 19k active series (−80%)** with
zero KubeBolt metrics lost.
- Scenario 1 / 2a / 2b-full: a few thousand active series for a mid-size cluster
  after filtering. Disabling the allowlist (`metricRelabelConfigs: []`) re-opens
  the annotation firehose and can multiply this several-fold.
- Scenario 3 minimal: agent kubelet/cAdvisor only (no scrape sidecar) — a few
  thousand series.
- Bundled VM defaults (10 GiB PVC, 30-day retention) cover up to ~500-node
  clusters comfortably. Bigger clusters → bump
  `metrics.storage.embedded.persistence.size` and/or move to an external VM via
  `metrics.storage.externalUrl`.

### External TSDB caveat

The agent's dedicated KSM job is now **single-scrape** by default — a
`node_name == %{NODE_NAME}` keep filter means only the agent co-located with KSM
scrapes it (see the agent chart's "Metric footprint" section), so the agent no
longer inflates KSM series regardless of TSDB. `--dedup` is only relevant if a
**second** scraper (e.g. the customer's own Prometheus in scenarios 1/2) also
pulls the same targets: the bundled VM handles that 2-source case with the
default `--dedup.minScrapeInterval=30s`; on an external TSDB
(`metrics.storage.externalUrl`) enable dedup there yourself for that case. See
[`deploy/helm/kubebolt/README.md`](../deploy/helm/kubebolt/README.md) Metrics
Storage section.

### Cilium / Hubble version requirements

Hubble flow collection needs:
- Cilium 1.13+ (older versions may need agent compatibility flags)
- `hubble.enabled: true` in Cilium values
- `hubble.relay.enabled: true` (the agent connects to the Relay)

Without Hubble, **the Reliability sub-tab is hidden entirely** — the
UI doesn't show an empty panel inviting the operator to install
something. This is the gated-empty-state pattern from
`useHubbleAvailable`.

---

## Not available today

- **OTLP ingestion** — apps can't push OpenTelemetry metrics directly;
  use Prometheus `remote_write` or the agent.
- **Namespace quota insight rule** — the quota gauge (category E) shows
  usage, but no insight fires when a quota runs close to full.

---

## Reference

- Agent scrape config operator guide: [`docs/agent-scraping.md`](agent-scraping.md) — vmagent sidecar config, troubleshooting
- Bundled chart README: [`deploy/helm/kubebolt/README.md`](../deploy/helm/kubebolt/README.md) — Metrics Storage, BYO TSDB caveat
- Agent chart README: [`deploy/helm/kubebolt-agent/README.md`](../deploy/helm/kubebolt-agent/README.md) — RBAC tiers, auth modes, scrape sidecar
- Remote_write receiver: [`docs/integrations/prometheus.md`](integrations/prometheus.md)
- Compatibility and supported platforms: [`docs/COMPATIBILITY.md`](COMPATIBILITY.md)
- Product overview and feature list: [`README.md`](../README.md) and https://kubebolt.io/docs
