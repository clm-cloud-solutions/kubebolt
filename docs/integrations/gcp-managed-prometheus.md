# Google Managed Prometheus (GMP) — read with kubebolt-agent (Mode C)

> **Available from KubeBolt 1.13.0+.** Earlier releases only supported
> Mode A (agent scrapes targets directly) and Mode B (customer's Prom
> pushes to KubeBolt). Mode C — agent **reads** from a customer-managed
> Prometheus — landed in 1.13 to cover the three managed Prom services
> where outbound `remote_write` is either impossible (AMP) or
> change-management-restricted (often the case with GMP).
>
> See the [mode matrix in `prometheus.md`](./prometheus.md#which-ingest-mode-fits-your-cluster)
> for when to pick Mode C over A or B.

This page is the **GCP-specific recipe** for Mode C. It assumes you
already understand the topology described in the Prometheus parent
doc; here we only cover what's GCP-flavored: Workload Identity
binding, the GMP query endpoint shape, and the IAM role the agent
needs.

---

## What you'll end up with

```
GMP (managed Prom in GCP)
      ▲ query_range every 30s
      │  GKE Workload Identity → KSA → GSA → roles/monitoring.viewer
      │
  ┌───┴───────────────────────────────────────┐
  │ kubebolt-agent (Deployment, replicas=1)   │
  │ KUBEBOLT_AGENT_MODE=promread              │
  │ Lease-elected leader: kubebolt-promread   │
  └───┬───────────────────────────────────────┘
      │ gRPC AgentChannel
      ▼
  KubeBolt backend → VictoriaMetrics
```

The agent runs as a **Deployment with replicas=1** (separate from the
Mode A DaemonSet — see [topology rationale in the agent's CLAUDE.md
section](../../CLAUDE.md#packagesagent)). One leader polls GMP; if
the pod dies the next scheduled pod takes over via Kubernetes Lease.

---

## Prerequisites

Four buckets to confirm before running any command in the steps
below. Order matters — each later bucket assumes the earlier ones
are green.

### A. Local toolchain (on your laptop)

| Tool | Min version | Verify |
|---|---|---|
| `gcloud` CLI | latest stable | `gcloud auth list` shows an `ACTIVE` account; `gcloud config get-value project` returns your target project ID |
| `kubectl` | within 1 minor version of your GKE control plane | `kubectl version --client` |
| `helm` | 3.x | `helm version --short` |

> **Tip:** if you need to authenticate gcloud now, run `gcloud auth
> login` (interactive). The rest of this doc assumes commands run
> under that authenticated account.

### B. GCP project + GKE cluster

Pick the path that matches your starting point:

**B1 — Greenfield (no cluster yet):**

```bash
PROJECT_ID=$(gcloud config get-value project)
LOCATION=us-east1-b               # zonal (1 node, cheaper) — or pass --region=us-east1 for regional (3 nodes, HA)
LOCATION_FLAG="--zone=$LOCATION"  # change to --region=$LOCATION for regional
CLUSTER=my-kubebolt-cluster       # pick a name

gcloud container clusters create $CLUSTER \
  $LOCATION_FLAG \
  --num-nodes=1 \
  --machine-type=e2-medium \
  --enable-managed-prometheus \
  --workload-pool=${PROJECT_ID}.svc.id.goog \
  --workload-metadata=GKE_METADATA \
  --release-channel=regular
```

The four flags that matter for this integration:

- `--enable-managed-prometheus` — provisions GMP + auto-deploys the
  managed collector DaemonSet.
- `--workload-pool=...` — enables the Workload Identity pool at the
  **cluster control-plane** level (used in Step 2 below).
- `--workload-metadata=GKE_METADATA` — enables the WI metadata
  server on the **initial node pool**. **Required separately** from
  `--workload-pool` — without it, pods get the node's Compute Engine
  default service account instead of their KSA-bound GSA, and Step 3
  will fail with `403 PERMISSION_DENIED on monitoring.timeSeries.list`
  at runtime.

> **⚠️ Verify both WI layers landed** (we've seen GKE 1.35 silently
> drop `--workload-pool` from cluster create under some conditions —
> always check):
>
> ```bash
> # Cluster-level WI must be set:
> gcloud container clusters describe $CLUSTER $LOCATION_FLAG \
>   --format='value(workloadIdentityConfig.workloadPool)'
> # Expected: <PROJECT_ID>.svc.id.goog  (NOT empty)
>
> # Node-pool WI metadata mode must be GKE_METADATA:
> gcloud container node-pools describe default-pool \
>   --cluster=$CLUSTER $LOCATION_FLAG \
>   --format='value(config.workloadMetadataConfig.mode)'
> # Expected: GKE_METADATA  (NOT empty, NOT GCE_METADATA)
> ```
>
> **If either is wrong**, fix with the B2 commands below — they're
> idempotent and apply equally to a half-configured greenfield
> cluster.

Both layers can also be retrofitted to an existing cluster — see
B2 / B3.

**B2 — Existing GKE cluster WITHOUT Workload Identity:**

```bash
# Enable WI at the cluster scope (control plane side).
gcloud container clusters update $CLUSTER \
  --region=$REGION \
  --workload-pool=${PROJECT_ID}.svc.id.goog

# Then enable GKE_METADATA on EACH node pool. WI requires the
# metadata server on every node — cluster-level enablement alone
# isn't enough; existing node pools need the explicit flip.
gcloud container node-pools list --cluster=$CLUSTER --region=$REGION \
  --format='value(name)' | while read pool; do
  gcloud container node-pools update $pool \
    --cluster=$CLUSTER --region=$REGION \
    --workload-metadata=GKE_METADATA
done
```

**B3 — Existing cluster with WI but WITHOUT managed Prometheus:**

```bash
gcloud container clusters update $CLUSTER \
  --region=$REGION \
  --enable-managed-prometheus
```

GMP starts scraping kubelet + cadvisor + a curated KSM subset
within ~5 min. No data loss for existing workloads.

**Verification (all paths):**

```bash
# APIs enabled. The grep-style check below is the safest cross-version
# form — gcloud's `--filter='name~...'` regex syntax doesn't always
# handle alternation cleanly across gcloud releases.
gcloud services list --enabled --project=$PROJECT_ID \
  --format='value(config.name)' \
  | grep -E '^(container|monitoring)\.googleapis\.com$'
# Expected: both lines printed.

# Your IAM (need one of these to run Step 2's IAM mutations)
gcloud projects get-iam-policy $PROJECT_ID \
  --flatten='bindings[].members' \
  --filter="bindings.members:user:$(gcloud config get-value account)" \
  --format='value(bindings.role)' \
  | grep -E '(owner|iam.serviceAccountAdmin|iam.workloadIdentityPoolAdmin)'
# Expected: at least one match. If empty, ask a project owner to grant.
```

### C. KubeBolt backend setup (BEFORE you run Step 3's helm install)

The agent needs three pieces of info about your KubeBolt backend
nailed down before Step 3 will work:

1. **Backend URL** — the host:port the agent dials via gRPC. Example:
   `kubebolt.example.com:443` for a TLS-terminated backend,
   `kubebolt-api.kubebolt.svc:9090` for in-cluster.
2. **TLS** — almost always yes for production. The chart's
   `--set tls.enabled=true` (Step 3) plus matching CA / serverName
   if your cert chain is non-public.
3. **Auth mode** — how the agent proves identity to the backend.
   Pick one:

| Mode | Best for | What you prepare here |
|---|---|---|
| **`ingest-token`** (recommended for SaaS / cross-cluster) | Backend is remote from the agent; multi-cluster operators | Issue a bearer token in the backend UI → `Admin` → `Agent tokens` → label it (e.g. `gke-prod`), keep the `kb_...` value handy |
| **`tokenreview`** | Backend runs in the SAME cluster as the agent (self-hosted single-cluster) | Backend chart already grants `tokenreviews/create`; no per-cluster prep |
| **`none`** | Dev only | Skip |

If you chose **`ingest-token`** (the common path), prepare the
Secret in the GKE cluster's `kubebolt` namespace BEFORE Step 3:

```bash
# Get GKE credentials if you haven't yet
gcloud container clusters get-credentials $CLUSTER \
  --region=$REGION --project=$PROJECT_ID

# Create the namespace + Secret
kubectl create namespace kubebolt
kubectl -n kubebolt create secret generic kubebolt-ingest-token \
  --from-literal=token='<paste-token-from-UI>'
```

The Secret name `kubebolt-ingest-token` matches the chart's default
expected key (`auth.ingestToken.existingSecret`). Override that
chart value if you used a different Secret name.

### D. Network egress from the cluster

The agent pods need outbound TCP to three destinations. On default
GKE networking (no custom egress NetworkPolicy, no VPC Service
Controls, no Private Google Access restrictions) these all work
out of the box.

| Destination | Port | Purpose |
|---|---|---|
| `metadata.google.internal` | 80 | Workload Identity token minting via the GKE metadata server |
| `monitoring.googleapis.com` | 443 | GMP query API — where the agent reads from |
| Your KubeBolt backend host | 443 (TLS) or 9090 (plain gRPC) | Where the agent ships samples |

If your cluster has restrictive egress controls:
- **NetworkPolicy** — add an egress rule in the `kubebolt` namespace
  allowing the three destinations above
- **VPC Service Controls** — `monitoring.googleapis.com` must be in
  your service perimeter's allowed services
- **Private cluster / Private Google Access** — verify the cluster
  has PGA enabled and the subnet routes traffic to Google's private
  endpoints

---

## Step 1 — Locate the GMP query endpoint

GMP's Prom-API-compatible endpoint is **global per project**, not
regional:

```bash
PROJECT_ID=$(gcloud config get-value project)
GMP_ENDPOINT="https://monitoring.googleapis.com/v1/projects/${PROJECT_ID}/location/global/prometheus"
echo "$GMP_ENDPOINT"
# → https://monitoring.googleapis.com/v1/projects/your-project/location/global/prometheus
```

The `location/global` segment is **fixed**. Unlike AWS AMP (whose
endpoint is region-scoped, `<workspace>.aps-workspaces.<region>...`),
you do **not** need to pass a region to the agent.

Smoke-test it from your laptop before going further:

```bash
TOKEN=$(gcloud auth print-access-token)
curl -s -H "Authorization: Bearer $TOKEN" \
  "${GMP_ENDPOINT}/api/v1/query?query=count(up)"
# → {"status":"success","data":{"resultType":"vector",
#    "result":[{"metric":{},"value":[<unix-ts>,"<N>"]}]}}
```

A `status:success` response means the endpoint, your project's GMP,
and your local credentials all work.

**Interpreting `<N>`** — the count is `nodes × scrape jobs`. A fresh
1-node cluster with GMP managed collection enabled has 3 targets
(kubelet, cadvisor, kube-state-metrics); a 3-node Standard cluster
typically lands between 7 and 10. The exact number doesn't matter —
**any non-zero value confirms GMP is scraping**.

**If `result:[]` (empty)**, one of:
- The GKE cluster is brand-new and the GMP `collector` DaemonSet
  hasn't completed its first scrape cycle. Wait ~60–90 seconds and
  retry. Verify with:
  ```bash
  kubectl -n gmp-system get pods
  # collector-xxxxx must be Running, 2/2 Ready
  ```
- The cluster doesn't have GMP enabled at all. Re-check
  Prerequisites B (`--enable-managed-prometheus`).
- You're querying a different `PROJECT_ID` than the one hosting
  the cluster.

> **403 Forbidden** means your local IAM lacks
> `roles/monitoring.viewer`. Project Owners and Editors inherit it
> automatically; standalone viewers need it granted explicitly.

> **The full API surface** of GMP's Prom-compatible endpoint
> includes `/api/v1/query`, `/api/v1/query_range`,
> `/api/v1/labels`, `/api/v1/label/<name>/values`,
> `/api/v1/series`. The agent uses `query_range` — `count(up)` here
> is just a one-shot canary.

> **GMP project ≠ cluster scope.** A single GMP project receives
> samples from every GKE cluster (and other Google Cloud services)
> in that project. To scope a query to one cluster, add
> `{cluster="<gke-cluster-name>"}` — see "What the agent sees"
> below for the full label set GMP auto-injects.

---

## Step 2 — Create the GSA and bind it to a KSA via Workload Identity

The agent does **not** use a service-account key file. It uses
GKE Workload Identity: a Kubernetes ServiceAccount (KSA) inside the
cluster gets impersonation rights on a Google ServiceAccount (GSA),
and the Go SDK's `DefaultTokenSource` mints short-lived tokens via
the GKE metadata server automatically.

```bash
PROJECT_ID=$(gcloud config get-value project)
GSA=kubebolt-promread
NAMESPACE=kubebolt
KSA=kubebolt-agent   # the chart's default; override with serviceAccount.name

# 1. Create the Google ServiceAccount.
#    Re-running is safe — `create` errors with "already exists" if
#    you re-run; pipe through grep to make this idempotent:
gcloud iam service-accounts create $GSA \
  --display-name="KubeBolt agent — reads from Google Managed Prometheus" \
  2>&1 | grep -v "already exists" || true

# 2. Grant it the minimum role to query GMP. monitoring.viewer is
#    read-only — does NOT grant ingest or admin privileges.
gcloud projects add-iam-policy-binding $PROJECT_ID \
  --member="serviceAccount:${GSA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/monitoring.viewer" \
  --condition=None

# 3. Allow the agent's KSA to impersonate the GSA. NOTE: the KSA
#    itself does NOT exist yet — it'll be created by helm in Step 3.
#    GKE resolves the binding lazily when the KSA appears, so this
#    succeeds now and "activates" later.
gcloud iam service-accounts add-iam-policy-binding \
  ${GSA}@${PROJECT_ID}.iam.gserviceaccount.com \
  --role="roles/iam.workloadIdentityUser" \
  --member="serviceAccount:${PROJECT_ID}.svc.id.goog[${NAMESPACE}/${KSA}]"

# 4. Verify the binding landed (recommended — Step 3 will fail with
#    cryptic auth errors at runtime if this is misconfigured):
gcloud iam service-accounts get-iam-policy \
  ${GSA}@${PROJECT_ID}.iam.gserviceaccount.com
# Expected: a `bindings` entry with
#   role: roles/iam.workloadIdentityUser
#   members: serviceAccount:<PROJECT_ID>.svc.id.goog[kubebolt/kubebolt-agent]
```

> **Prerequisite: Workload Identity must be enabled at BOTH the
> cluster AND node-pool layers.** Cluster-level enablement
> (`--workload-pool`) alone is not enough — every node pool that
> runs the agent must also have `--workload-metadata=GKE_METADATA`.
>
> If the cluster layer is missing, you'll see
> `unable to fetch token`. If the node-pool layer is missing, the
> pod silently inherits the node's Compute Engine default service
> account and you'll see `403 PERMISSION_DENIED on
> monitoring.timeSeries.list` — the GSA's role binding is correct
> but **nothing on the cluster is actually using the GSA**.
>
> See Prerequisite B's verification block to confirm both layers
> before continuing.

> **Note on KSA naming.** The chart's default `serviceAccount.name`
> resolves to `kubebolt-agent` (the fullname). If you override it
> (`--set serviceAccount.name=<custom>`), use that name in the
> `workloadIdentityUser` binding above. The agent's promread
> Deployment uses the same KSA as the DaemonSet.

> **You do NOT need to create the KSA manually.** The chart creates
> it during `helm install` (Step 3) and applies the
> `iam.gke.io/gcp-service-account` annotation that ties it to the
> GSA — via the `serviceAccount.annotations` value below.

---

## Step 3 — Install the agent with Mode C enabled

The KSA annotation telling GKE which GSA to impersonate is set via
the chart's `serviceAccount.annotations` field. The rest is the
standard Mode C install: disable the legacy scrape sidecar
(`scrape.enabled=false`), enable promread, point at the GMP
endpoint, pick `gcpIam` as the auth mode, and **wire the
ingest-token Secret you created in Prerequisite C2** so the agent
can authenticate to the KubeBolt backend.

```bash
PROJECT_ID=$(gcloud config get-value project)
GSA=kubebolt-promread
GMP_ENDPOINT="https://monitoring.googleapis.com/v1/projects/${PROJECT_ID}/location/global/prometheus"

helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set backendUrl=<your-kubebolt-host>:443 \
  --set cluster.name=<your-cluster-name> \
  --set auth.mode=ingest-token \
  --set auth.ingestToken.existingSecret=kubebolt-ingest-token \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url="${GMP_ENDPOINT}" \
  --set agent.promRead.auth.mode=gcpIam \
  --set serviceAccount.annotations."iam\.gke\.io/gcp-service-account"="${GSA}@${PROJECT_ID}.iam.gserviceaccount.com"
```

**Flag-by-flag reference:**

| Flag | What it does | Common mistake |
|------|--------------|----------------|
| `backendUrl` | host:port the agent dials over gRPC. | Don't include a `https://` scheme — the agent expects `host:port` only. Port `443` is correct for a Caddy/Ingress fronting the KubeBolt backend. |
| `cluster.name` | Operator-chosen label shown in the KubeBolt UI cluster selector. **Independent** of the `cluster=` label GMP auto-injects (which comes from the GKE cluster name). They can differ. |
| `auth.mode=ingest-token` + `auth.ingestToken.existingSecret` | Authenticates the agent → backend channel. Without this, the gRPC handshake is rejected with `unauthenticated` and the agent never registers. The Secret was created in Prerequisite C2. |
| `scrape.enabled=false` | Disables the legacy vmagent scrape sidecar (you don't want it scraping in parallel with promread). **Does NOT** disable Mode A's DaemonSet kubelet-stats collector — that still runs and complements GMP with KubeBolt-canonical metrics. |
| `agent.promRead.enabled=true` | Spawns the promread Deployment (single replica, Lease-elected). |
| `agent.promRead.url` | The base GMP endpoint **without** `/api/v1/query_range` — the agent appends the path internally. |
| `agent.promRead.auth.mode=gcpIam` | Tells the agent to mint tokens via the GKE metadata server (Workload Identity). No key file needed. |
| `serviceAccount.annotations."iam\.gke\.io/..."` | Links the KSA to the GSA from Step 2. The backslash-escaping is for Helm's `--set` parser, not for YAML. |

> **Escaping `iam.gke.io/gcp-service-account`.** Helm's `--set`
> syntax treats `.` as a path separator, so the annotation key has
> to be backslash-escaped. If you'd rather avoid the escaping
> dance, put the annotation in a values file and pass `-f`:
>
> ```yaml
> serviceAccount:
>   annotations:
>     iam.gke.io/gcp-service-account: kubebolt-promread@your-project.iam.gserviceaccount.com
> auth:
>   mode: ingest-token
>   ingestToken:
>     existingSecret: kubebolt-ingest-token
> ```

> **Default matchers are GMP-compatible.** The agent's default
> matcher set uses explicit metric names (`kube_pod_status_phase`,
> `node_load1`, etc.), not regex on `__name__` — because GMP
> rejects `{__name__=~"..."}` selectors. If you override
> `agent.promRead.matchers`, keep them as explicit name lists for
> GMP compatibility.

> **Do NOT set `agent.deferNodeStress=true` on GMP.** GMP managed
> collection does NOT scrape node-exporter — the agent's Mode A
> NodeStress collector (loadavg + PSI from `/proc/loadavg` directly)
> is the **only** source of `node_load1/5/15` and
> `node_pressure_*_waiting_seconds_total` on GKE. Disabling it
> leaves the UI's Load average panel empty. (This flag IS required
> on Azure Managed Prometheus, where `ama-metrics` always scrapes
> node-exporter — but the inverse on GMP.)

`cluster.name` is operator-chosen and shows up in the UI's cluster
selector. The chart also accepts `cluster.id` if you want to pin it;
when omitted, the agent derives it from the kube-system namespace
UID on first boot.

---

## Step 4 — Verify

Four things to check, in order:

```bash
# 1. The promread pod is Running.
kubectl -n kubebolt get pods -l kubebolt.dev/role=promread
# Expected: 1 pod, STATUS=Running.

# 2. The leader lease is held.
kubectl -n kubebolt get lease kubebolt-promread
# Expected: HOLDER=<pod-name>.

# 3. Logs show successful collection (no auth errors).
kubectl -n kubebolt logs -l kubebolt.dev/role=promread --tail=30
# Expected:
#   INFO msg="samples collected" collector=promread count=N   (N typically 50-100)
#   INFO msg="buffer stats" collected_total=… dropped_total=0
# RED FLAGS:
#   WARN msg="promread matcher failed" error="… 403 PERMISSION_DENIED …"
#     → Workload Identity isn't actually wired through. Re-verify
#       Prerequisite B's two WI layers (cluster + node-pool).
#   WARN msg="promread matcher failed" error="… 400 … __name__ …"
#     → A custom matcher used regex on __name__; GMP rejects that.
#       Switch the matcher to explicit metric names.

# 4. Round-trip to backend: the agent has registered.
kubectl -n kubebolt logs -l kubebolt.dev/role=promread --tail=50 \
  | grep -E "cluster identity|channel registered"
# Expected:
#   INFO msg="cluster identity" cluster_id=<uuid> cluster_name=<your cluster.name>
#   INFO msg="channel registered" agent_id=<id> cluster_id=<uuid>
# Absence of "channel registered" means the backend rejected the
# handshake — check the ingest-token Secret + auth.mode.
```

In the KubeBolt UI, the **Prometheus (read)** card under
`/admin/integrations` should flip from `Not installed` to `Installed`
within ~30-60 seconds. The wait is the lease handover (~5-15s) +
first poll cycle (~`scrapeInterval`, default 30s).

---

## Step 5 — (Optional) Choose the agent's RBAC tier

Step 3 deployed the agent at the default `reader` tier — read-only
inventory + metrics, no mutations. If you want the KubeBolt UI to
also support **exec into pods, scale, rollout-restart, delete, and
YAML edit**, you need the `operator` tier.

Three modes are available:

| Mode | Cluster permissions | UI capabilities | When to pick |
|------|---------------------|-----------------|--------------|
| `metrics` | kubelet stats + pods list/watch + namespaces. No apiserver tunnel. | Live CPU/Mem on Overview. **No** resource list/detail views, no logs, no exec. | Privacy-conscious orgs that want metrics only; no inspection of cluster contents from the backend. |
| `reader` *(default)* | Cluster-wide `get/list/watch` on `*/*` via SPDY tunnel. | Full read-only dashboard: resource lists, detail views, YAML view, logs, describe. Write actions return 403. | Default for production read-only observability. |
| `operator` | Wildcard read **+ write** on `*/*` — effectively cluster-admin scoped to the agent ServiceAccount. | Everything in `reader` **plus** exec / port-forward / scale / restart / delete / YAML apply. | Teams who want the dashboard as their primary day-2 K8s control plane. |

**Upgrade to operator (re-uses your Step 3 values):**

```bash
helm upgrade kubebolt-agent \
  ./deploy/helm/kubebolt-agent \
  -n kubebolt \
  --reuse-values \
  --set rbac.mode=operator
```

The promread + DaemonSet pods restart with the upgraded RBAC bound.
The Prometheus (read) integration card stays Installed; the new
write capabilities surface automatically in the UI (action buttons
on resource pages enable, the Pod terminal tab becomes interactive,
etc.).

> **⚠️ Security: operator mode requires backend auth.**
> An operator-tier agent gives the KubeBolt backend cluster-admin
> on this cluster. If the backend's gRPC ingest port is reachable
> without authentication, **anyone who can dial it can pivot to
> cluster-admin**. You're already safe in this guide because Step 3
> wires `auth.mode=ingest-token` with the Secret from Prerequisite
> C2 — the backend rejects unauthenticated channels. **Do not**
> switch to operator if you've disabled auth.

> **Downgrade is symmetric.** `--set rbac.mode=reader` (or
> `=metrics`) on a subsequent `helm upgrade --reuse-values` shrinks
> the ClusterRole back. K8s revokes the dropped verbs immediately.

---

## What the agent sees (vs Mode A)

GMP relabels several universal labels **at ingest time** that the
agent's `K8sNodeIndex` enrichment normally has to synthesize for
AMP and Azure. On GCP these come for free:

| Label | How it gets there |
|---|---|
| `node` | GMP auto-stamps from the scrape target's node assignment |
| `cluster` | GMP auto-stamps from the cluster name (`cluster=<GKE_CLUSTER_NAME>`) |
| `location` | GMP auto-stamps from the cluster's region/zone |
| `project_id` | GMP auto-stamps from the GCP project |

This is why GCP is the simplest of the three managed providers to
onboard — there's no `node` label fallback path the agent has to
exercise, no per-instance IP-to-nodeName lookup, no cross-referencing
the Kubernetes API. The samples arrive labeled correctly.

The `instance` label has a non-standard shape on GMP
(`<nodename>:metrics` rather than `<ip>:<port>`), but since `node`
is already stamped, this doesn't affect any UI panel.

---

## Default matchers

The chart's default `agent.promRead.matchers` is surgical — it pulls
**only** what Mode A doesn't synthesize, avoiding double-emission of
metric families like `node_fs_used_bytes` and
`container_cpu_usage_seconds_total` that the agent's kubelet
collector already provides in Mode A.

For Mode C (where Mode A is off), the defaults pull:

- `kube_.*` — full kube-state-metrics
- `node_load.*|node_pressure_.*` — uptime + PSI
- `node_disk_.*|node_network_.*_errs_.*` — I/O detail
- `up|process_.*` — target health

You can override `agent.promRead.matchers` in the helm install if
you have additional series the UI panels need. Keep matchers
**surgical** — broad matchers cause ~65% sample bloat in our
benchmarks (S1 multi-node smoke 2026-05-26) and run up the GMP
query bill.

---

## Cost notes

GMP charges per sample **ingested** and per **query API call**. Mode C
adds query API charges (the agent polls every 30s by default) but
does NOT change ingestion — the agent is reading samples GMP already
has.

Default poll cadence (every 30s, ~10 matchers) works out to roughly:

```
2 calls/min × 60 × 24 × 30 = ~86,400 extra query calls/month
```

GCP's GMP query pricing is the lowest of the three managed Prom
services at the time of writing, so the delta is typically the
smallest of any cloud. Verify against the
[Google Cloud Operations Pricing page](https://cloud.google.com/stackdriver/pricing#monitoring-pricing-summary)
for your project's volume.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Pod logs show `oauth2: cannot fetch token: 403` | Workload Identity binding is wrong | Re-verify the `workloadIdentityUser` binding member matches exactly `serviceAccount:<PROJECT>.svc.id.goog[<NAMESPACE>/<KSA>]` — typos in namespace or KSA name silently fail |
| Pod logs show `googleapi: Error 403: ... monitoring.viewer` | GSA lacks the role | `gcloud projects add-iam-policy-binding ... --role=roles/monitoring.viewer` |
| KSA annotation missing | Forgot the `serviceAccount.annotations` chart flag | `kubectl describe sa kubebolt-agent -n kubebolt` — must show `iam.gke.io/gcp-service-account: <GSA>@<PROJECT>.iam.gserviceaccount.com` |
| Card still says `Not installed` after 5 min | Cluster UID not stamped on the leader gauge | Check `kubectl -n kubebolt logs -l kubebolt.dev/role=promread \| grep cluster_id` — should show a UUID. If empty, set `--set cluster.id=<kube-system UID>` explicitly |
| Pod logs show `connection refused` to `monitoring.googleapis.com` | Egress firewall blocking the metadata server or googleapis | Open egress to `metadata.google.internal:80` and `monitoring.googleapis.com:443` |

---

## Cleanup

```bash
NAMESPACE=kubebolt
KSA=kubebolt-agent
PROJECT_ID=$(gcloud config get-value project)
GSA=kubebolt-promread

# 1. Remove the agent.
helm uninstall kubebolt-agent -n $NAMESPACE

# 2. Remove the WI binding.
gcloud iam service-accounts remove-iam-policy-binding \
  ${GSA}@${PROJECT_ID}.iam.gserviceaccount.com \
  --role="roles/iam.workloadIdentityUser" \
  --member="serviceAccount:${PROJECT_ID}.svc.id.goog[${NAMESPACE}/${KSA}]"

# 3. Remove the project-level role.
gcloud projects remove-iam-policy-binding $PROJECT_ID \
  --member="serviceAccount:${GSA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/monitoring.viewer"

# 4. Delete the GSA.
gcloud iam service-accounts delete \
  ${GSA}@${PROJECT_ID}.iam.gserviceaccount.com --quiet
```

GMP itself does NOT need explicit deletion — it's a project-scoped
managed service; disabling it would affect other workloads. Leaving
it enabled has zero per-month cost; only ingestion + queries are
billed.

**Optional — delete the GKE cluster.** If this cluster was created
solely to evaluate the KubeBolt + GMP integration and you don't plan
to keep it around, the fastest way to stop control-plane + node
billing is:

```bash
CLUSTER=my-kubebolt-cluster       # the name you picked in Prereq B1
LOCATION_FLAG="--zone=us-east1-b"  # or --region=<your-region> if you went regional

gcloud container clusters delete $CLUSTER $LOCATION_FLAG --quiet
```

Takes ~3 minutes; tears down the node VMs as a side-effect. **Do
NOT run this against a production GKE cluster** — the rest of this
section (helm uninstall + IAM cleanup) is enough to fully remove
KubeBolt from a cluster you want to keep.

---

## References

- Parent mode matrix: [`prometheus.md`](./prometheus.md)
- AMP recipe (same pattern, different auth): [`aws-amp.md`](./aws-amp.md)
- Azure Managed Prometheus recipe: [`azure-managed-prometheus.md`](./azure-managed-prometheus.md)
- Self-managed Prom (Basic auth / Bearer / None): [`self-managed-prom-readonly.md`](./self-managed-prom-readonly.md)
- [Google Cloud — Workload Identity overview](https://cloud.google.com/kubernetes-engine/docs/concepts/workload-identity)
- [Google Cloud — Managed Service for Prometheus query API](https://cloud.google.com/stackdriver/docs/managed-prometheus/query)
