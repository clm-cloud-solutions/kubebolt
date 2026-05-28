# Azure Managed Prometheus (AMW) — read with kubebolt-agent (Mode C)

> **Available from KubeBolt 1.13.0+.** Earlier releases only supported
> Mode A (agent scrapes targets directly) and Mode B (customer's Prom
> pushes to KubeBolt). Mode C — agent **reads** from a customer-managed
> Prometheus — landed in 1.13 to cover the three managed Prom services
> where outbound `remote_write` is either impossible (AMP) or
> change-management-restricted (often the case with GMP).
>
> See the [mode matrix in `prometheus.md`](./prometheus.md#which-ingest-mode-fits-your-cluster)
> for when to pick Mode C over A or B.

This page is the **Azure-specific recipe** for Mode C. It assumes you
already understand the topology described in the Prometheus parent
doc; here we only cover what's Azure-flavored: Workload Identity
binding, the Azure Monitor Workspace (AMW) query endpoint shape, and
the role assignment the agent needs.

> **Why Mode C exists for Azure Managed Prometheus.** Azure's managed
> Prom is **query-only outbound** — it has no `remote_write` egress
> API. Mode B (Prom-pushes-to-KubeBolt) is therefore impossible. Mode
> C is the only path that lets a KubeBolt install reuse an existing
> Azure Monitor Workspace.

---

## What you'll end up with

```
Azure Monitor Workspace (managed Prom in Azure)
      ▲ query_range every 30s
      │  Workload Identity → KSA → Federated Cred → UAMI →
      │  Monitoring Data Reader role on AMW
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
section](../../CLAUDE.md#packagesagent)). One leader polls AMW; if
the pod dies the next scheduled pod takes over via Kubernetes Lease.

---

## Prerequisites

### A. Local toolchain (on your laptop)

| Tool | Verify | Notes |
|---|---|---|
| `az` CLI v2 | `az version` (>= 2.50 recommended) | Used for all Azure-side resource creation. |
| Azure authenticated | `az account show` | Must return the subscription you intend to use. If wrong, `az account set --subscription "<name-or-id>"`. |
| `kubectl` | `kubectl version --client` | >= 1.27 recommended. |
| `helm` v3 | `helm version --short` | |

### B. Azure subscription + AKS cluster + Azure Monitor Workspace

Pick the path that matches your starting point:

**B1 — Greenfield (no cluster, no AMW):**

```bash
set -u

LOCATION=eastus                   # pick your region
RG=my-kubebolt-rg                 # pick a resource group name
CLUSTER=my-kubebolt-cluster       # pick a cluster name
K8S_VERSION=1.35                  # 1.35+ recommended; check the supported window via `az aks get-versions --location $LOCATION --output table` — AKS resolves `<major>.<minor>` to the latest patch in that minor automatically

# Create the resource group.
az group create --name "$RG" --location "$LOCATION"

# Create the AKS cluster WITH the THREE flags that matter most for
# this integration. All three are required:
#  --enable-oidc-issuer       → mints the OIDC issuer URL Azure AD will trust
#  --enable-workload-identity → installs the in-cluster webhook that
#                                injects federated tokens into pods
#  --enable-azure-monitor-metrics → auto-provisions an AMW + the
#                                    ama-metrics scraper DaemonSet
az aks create \
  --resource-group "$RG" \
  --name "$CLUSTER" \
  --location "$LOCATION" \
  --kubernetes-version "$K8S_VERSION" \
  --node-count 1 \
  --node-vm-size Standard_B2s \
  --enable-oidc-issuer \
  --enable-workload-identity \
  --enable-azure-monitor-metrics \
  --generate-ssh-keys
```

The three flags that matter most for this integration:

- `--enable-oidc-issuer` — mints the OIDC issuer URL Azure AD trusts
  for federated tokens (Step 2 requires it).
- `--enable-workload-identity` — installs the webhook that injects
  the federated token file + env vars into pods carrying the
  `azure.workload.identity/use: "true"` label (Step 3 requires it).
- `--enable-azure-monitor-metrics` — auto-provisions an Azure
  Monitor Workspace AND deploys the `ama-metrics` scraper DaemonSet
  that scrapes kubelet + cadvisor + node-exporter + (optionally)
  Retina flows directly into AMW. Skip this only if you want to
  point at an existing AMW from a different RG / subscription.

> **All three flags must land together.** Missing
> `--enable-oidc-issuer` makes Step 2's federated credential
> creation succeed but pods can't exchange the token. Missing
> `--enable-workload-identity` makes the federated token file never
> appear in the pod. Missing `--enable-azure-monitor-metrics`
> creates a cluster with no metrics flowing into AMW — Step 1's
> smoke test then returns `result:[]` and Step 4 looks "broken" but
> the agent itself is fine.

> **Validation-only cost optimization.** `Standard_B2s` (2 vCPU,
> 4 GiB, burstable) is ~$30/month. Default AKS nodes
> (`Standard_DS2_v2`) are ~$70/month. AKS control plane is free.
> For ad-hoc evaluation, B2s × 1 node is the practical floor. **Do
> NOT use this sizing for production.**

> AKS cluster creation typically takes ~10-15 minutes (control
> plane + node pool + Azure Monitor agent addons).

**B2 — Existing AKS cluster missing one or more of the three flags:**

```bash
# Enable OIDC issuer + WI together (idempotent if already on).
az aks update --resource-group "$RG" --name "$CLUSTER" \
  --enable-oidc-issuer --enable-workload-identity

# Enable Azure Monitor metrics (provisions AMW if not already linked).
az aks update --resource-group "$RG" --name "$CLUSTER" \
  --enable-azure-monitor-metrics
```

Both updates can take ~5 minutes each. The cluster's existing
workloads keep running.

**B3 — Existing cluster fully configured, no AMW yet:**

```bash
# Create AMW separately (e.g. in a different RG you control).
az monitor account create \
  --name my-kubebolt-amw \
  --resource-group "$RG" \
  --location "$LOCATION"
```

Then link the AKS cluster's ama-metrics addon to it via
`az aks update --enable-azure-monitor-metrics --azure-monitor-workspace-resource-id <amw-id>`.

**Verification (all paths):**

```bash
# OIDC issuer attached?
az aks show --resource-group "$RG" --name "$CLUSTER" \
  --query 'oidcIssuerProfile.issuerUrl' --output tsv
# Expected: https://eastus.oic.prod-aks.azure.com/<tenant>/<guid>/  (NOT empty)

# Workload Identity enabled?
az aks show --resource-group "$RG" --name "$CLUSTER" \
  --query 'securityProfile.workloadIdentity.enabled' --output tsv
# Expected: true

# Azure Monitor metrics enabled?
az aks show --resource-group "$RG" --name "$CLUSTER" \
  --query 'azureMonitorProfile.metrics.enabled' --output tsv
# Expected: true

# Your principal can create UAMIs + assign roles?
az role assignment list --assignee "$(az account show --query user.name --output tsv)" \
  --query "[?contains(roleDefinitionName, 'Owner') || contains(roleDefinitionName, 'Contributor') || contains(roleDefinitionName, 'User Access Administrator')].roleDefinitionName" \
  --output tsv
# Expected: at least one of Owner, Contributor, or User Access Administrator
```

**KSM and node-exporter are included** when AMW is enabled via
`--enable-azure-monitor-metrics` — Azure auto-deploys `ama-metrics`
pods that scrape `kubelet`, `cadvisor`, `node`, and optionally
`networkobservability-retina`. You do not need a separate
`kube-prometheus-stack` install on Azure for the default Mode C
matchers to find data.

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
| **`ingest-token`** (recommended for SaaS / cross-cluster) | Backend is remote from the agent; multi-cluster operators | Issue a bearer token in the backend UI → `Admin` → `Agent tokens` → label it (e.g. `aks-prod`), keep the `kb_...` value handy |
| **`tokenreview`** | Backend runs in the SAME cluster as the agent (self-hosted single-cluster) | Backend chart already grants `tokenreviews/create`; no per-cluster prep |
| **`none`** | Dev only | Skip |

If you chose **`ingest-token`** (the common path), prepare the
Secret in the AKS cluster's `kubebolt` namespace BEFORE Step 3:

```bash
# Get AKS credentials if you haven't yet
az aks get-credentials --resource-group "$RG" --name "$CLUSTER"

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
AKS networking (no custom egress NSG rules, no Azure Firewall, no
Private Link restrictions) these all work out of the box.

| Destination | Port | Purpose |
|---|---|---|
| `login.microsoftonline.com` | 443 | Azure AD federated token exchange for the agent's KSA |
| `<workspace>.<region>.prometheus.monitor.azure.com` | 443 | AMW query API — where the agent reads from |
| Your KubeBolt backend host | 443 (TLS) or 9090 (plain gRPC) | Where the agent ships samples |

If your cluster has restrictive egress controls:
- **NSG / NetworkPolicy** — add an egress rule in the `kubebolt`
  namespace allowing the three destinations above
- **Azure Firewall** — add application rules for `*.prometheus.monitor.azure.com`
  and `login.microsoftonline.com`
- **Private cluster** — verify Private Link endpoints exist for
  `prometheus.monitor.azure.com` (and any internal-network route
  to the KubeBolt backend)

---

## Step 1 — Locate the AMW query endpoint

Azure Monitor Workspace endpoints are **workspace-scoped + region-scoped**,
and the URL has a random suffix Azure picks at provisioning time:

```bash
# Discover the AMW resource ID (most subscriptions have just one;
# adjust the index if you have several).
AMW_ID=$(az resource list \
  --resource-type "Microsoft.Monitor/accounts" \
  --query "[0].id" -o tsv)

# Extract the Prom query endpoint from its properties.
AMW_ENDPOINT=$(az resource show --ids $AMW_ID \
  --query "properties.metrics.prometheusQueryEndpoint" -o tsv)

echo "$AMW_ENDPOINT"
# → https://defaultazuremonitorworkspace-eastus-abc1.eastus.prometheus.monitor.azure.com
```

The endpoint pattern is:

```
https://<workspace-name>-<random>.<region>.prometheus.monitor.azure.com
```

Smoke-test it from your laptop before going further. AMW uses a
bearer token bound to the `https://prometheus.monitor.azure.com`
resource scope — `az account get-access-token` mints one for the
logged-in user:

```bash
TOKEN=$(az account get-access-token \
  --resource "https://prometheus.monitor.azure.com" \
  --query accessToken --output tsv)

curl -s -H "Authorization: Bearer $TOKEN" \
  "${AMW_ENDPOINT}/api/v1/query?query=count(up)"
# → {"status":"success","data":{"resultType":"vector",
#    "result":[{"metric":{},"value":[<unix-ts>,"<N>"]}]}}
```

A `status:success` response means the endpoint and bearer-auth path
work. **`<N>` is the number of scrape targets being collected** —
on a single-node AKS cluster with default `--enable-azure-monitor-metrics`
scrape config (kubelet + cadvisor + kube-state-metrics + node-exporter
+ Retina), the count is typically 5. A multi-node cluster scales
roughly linearly.

> **Two-stage population timing.** On a freshly enabled AMW, you'll
> see two different states evolve:
>
> 1. **Catalog (metric names) populates within ~30 seconds** —
>    `curl ${AMW_ENDPOINT}/api/v1/label/__name__/values` already
>    returns the full default scrape catalog including `up`,
>    `kube_pod_*`, `node_*`, etc.
> 2. **Time-series values populate ~60-120 seconds later** —
>    `count(up)` may return `result:[]` for the first 1-2 minutes
>    even though the catalog already lists `up`. This is the
>    `ama-metrics` DaemonSet's first scrape cycle landing in AMW.
>
> If `result:[]` persists past ~3 minutes, check that the
> `ama-metrics-node` DaemonSet pods are Running in `kube-system`:
> ```
> kubectl -n kube-system get pods -l dsName=ama-metrics-node
> ```

> **403 Forbidden** means your account needs the `Monitoring Data
> Reader` role on the AMW resource scope. Owner / Contributor /
> Reader inherit Monitoring Data Reader implicitly on subscription
> scope; standalone Reader on the AMW alone does NOT.

> **The full API surface** of AMW's Prom-compatible endpoint
> includes `/api/v1/query`, `/api/v1/query_range`,
> `/api/v1/labels`, `/api/v1/label/<name>/values`,
> `/api/v1/series`. The agent uses `query_range` — `count(up)`
> here is just a one-shot canary.

---

## Step 2 — Create the UAMI and bind it to a KSA via Workload Identity

Azure Workload Identity has the **most resources to wire** of the
three managed providers — 5 sequential steps. This is the trade-off
for a fully ambient-credential runtime path.

```bash
set -u

RG=<your-aks-resource-group>
CLUSTER=<your-aks-cluster-name>
LOCATION=eastus
NAMESPACE=kubebolt
KSA=kubebolt-agent   # the chart's default; override with serviceAccount.name
UAMI=kubebolt-promread

# 0. Pre-flight: confirm kubeconfig points at THIS AKS cluster.
#    Especially important if you just finished setting up a different
#    cluster (GMP/AMP guides) — your kubectl context may still be the
#    previous one. Substring-match the cluster name.
CURRENT_CTX=$(kubectl config current-context 2>/dev/null || echo "")
if ! printf '%s' "$CURRENT_CTX" | grep -q "${CLUSTER}"; then
  echo "kubectl context doesn't reference ${CLUSTER} — fixing"
  az aks get-credentials --resource-group "$RG" --name "$CLUSTER" --overwrite-existing
fi

# 0b. Create the kubebolt namespace (idempotent).
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# 1. Create the User-Assigned Managed Identity.
#    `az identity create` is idempotent — re-running returns the
#    existing UAMI's JSON without error.
az identity create \
  --name "$UAMI" \
  --resource-group "$RG" \
  --location "$LOCATION"

UAMI_CLIENT_ID=$(az identity show \
  --resource-group "$RG" --name "$UAMI" \
  --query clientId --output tsv)
UAMI_PRINCIPAL_ID=$(az identity show \
  --resource-group "$RG" --name "$UAMI" \
  --query principalId --output tsv)

# 2. Grant the UAMI Monitoring Data Reader on the AMW scope.
#    Filter the AMW by name to avoid picking the wrong one if your
#    subscription has multiple. Default-provisioned name follows the
#    pattern `DefaultAzureMonitorWorkspace-<region>`.
AMW_ID=$(az resource list \
  --resource-type "Microsoft.Monitor/accounts" \
  --query "[0].id" --output tsv)
# If multiple AMWs exist, narrow with --name:
#   AMW_ID=$(az resource list --resource-type "Microsoft.Monitor/accounts" \
#     --name "DefaultAzureMonitorWorkspace-${LOCATION}" --query "[0].id" --output tsv)

# `az role assignment create` errors with "already exists" if re-run —
# the `|| true` makes it idempotent.
az role assignment create \
  --assignee-object-id "$UAMI_PRINCIPAL_ID" \
  --assignee-principal-type ServicePrincipal \
  --role "Monitoring Data Reader" \
  --scope "$AMW_ID" 2>&1 | grep -v "already exists" || true

# 3. Grab the cluster's OIDC issuer URL — Azure AD will trust this
#    issuer's tokens when minting federated credentials.
OIDC_ISSUER=$(az aks show \
  --resource-group "$RG" --name "$CLUSTER" \
  --query "oidcIssuerProfile.issuerUrl" --output tsv)

# 4. Create the federated credential binding UAMI ↔ AKS OIDC ↔ KSA.
#    Re-running errors with "already exists" — guard with grep:
az identity federated-credential create \
  --name "${UAMI}-fed" \
  --identity-name "$UAMI" \
  --resource-group "$RG" \
  --issuer "$OIDC_ISSUER" \
  --subject "system:serviceaccount:${NAMESPACE}:${KSA}" \
  --audience api://AzureADTokenExchange 2>&1 | grep -v "already exists" || true

# 5. Verify (recommended — Step 3 will fail with cryptic
#    "AZURE_FEDERATED_TOKEN_FILE not set" errors at runtime if any
#    of the above silently mis-wired):

# 5a. UAMI exists and we have its clientId:
echo "UAMI_CLIENT_ID=${UAMI_CLIENT_ID:-MISSING}"

# 5b. Role assignment landed on the AMW scope:
az role assignment list \
  --assignee "$UAMI_PRINCIPAL_ID" --scope "$AMW_ID" \
  --query "[].roleDefinitionName" --output tsv
# Expected: Monitoring Data Reader

# 5c. Federated credential exists with the right subject:
az identity federated-credential show \
  --name "${UAMI}-fed" --identity-name "$UAMI" --resource-group "$RG" \
  --query "{subject:subject,issuer:issuer,audiences:audiences}"
# Expected:
#   subject = system:serviceaccount:kubebolt:kubebolt-agent
#   issuer  = (matches $OIDC_ISSUER)
#   audiences = [api://AzureADTokenExchange]
```

> **Why 5 steps.** Each Azure resource lives in a different control
> plane: UAMI is in `Microsoft.ManagedIdentity`, the role assignment
> is in `Microsoft.Authorization`, the federated credential lives
> under the UAMI, and the KSA annotation (Step 3 below) is the only
> piece on the K8s side. AWS IRSA collapses several of these into a
> single `eksctl create iamserviceaccount`; Azure doesn't have an
> equivalent one-shot CLI.

> **Forward reference: pod label too.** Step 3 sets BOTH a KSA
> annotation (`azure.workload.identity/client-id`) AND a pod label
> (`azure.workload.identity/use: "true"`). The federated credential
> wired here ties the chain together — but if Step 3's pod label is
> missing, the Azure WI webhook silently skips injection and Step 4
> will fail with `AZURE_FEDERATED_TOKEN_FILE not set`.

> **Note on KSA naming.** The chart's default `serviceAccount.name`
> resolves to `kubebolt-agent` (the fullname). If you override it
> (`--set serviceAccount.name=<custom>`), use that name in the
> federated credential's `--subject`. The agent's promread Deployment
> uses the same KSA as the DaemonSet.

---

## Step 3 — Install the agent with Mode C enabled

Two pieces of Azure-specific chart config: the KSA needs the
`azure.workload.identity/client-id` annotation pointing at the UAMI,
and the **pod itself** needs the `azure.workload.identity/use: "true"`
label so the WI webhook injects the federated token + env vars. The
rest is the standard Mode C install: disable the legacy scrape
sidecar (`scrape.enabled=false`), enable promread, point at the AMW
endpoint, pick `azureWorkloadIdentity` as the auth mode, and **wire
the ingest-token Secret you created in Prerequisite C2** so the
agent can authenticate to the KubeBolt backend.

```bash
set -u

UAMI_CLIENT_ID=$(az identity show \
  --resource-group "$RG" --name "$UAMI" \
  --query clientId --output tsv)

helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set backendUrl=<your-kubebolt-host>:443 \
  --set tls.enabled=true \
  --set cluster.name=<your-cluster-name> \
  --set auth.mode=ingest-token \
  --set auth.ingestToken.existingSecret=kubebolt-ingest-token \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url="${AMW_ENDPOINT}" \
  --set agent.promRead.auth.mode=azureWorkloadIdentity \
  --set serviceAccount.annotations."azure\.workload\.identity/client-id"="${UAMI_CLIENT_ID}" \
  --set-string podLabels."azure\.workload\.identity/use"="true" \
  --set agent.deferNodeStress=true
```

> **Why `--set-string` for the pod label.** `helm --set` type-infers
> the value: `true` becomes a YAML boolean, but Kubernetes labels
> are strictly strings. Without `--set-string`, the install fails
> at apply time with
> `cannot unmarshal bool into Go struct field ObjectMeta.spec.template.metadata.labels of type string`.
> The other Azure-specific value (`UAMI_CLIENT_ID`, a UUID) is
> unambiguously a string and works fine with plain `--set`.

**Flag-by-flag reference:**

| Flag | What it does | Common mistake |
|------|--------------|----------------|
| `backendUrl` | host:port the agent dials over gRPC. | Don't include a `https://` scheme — the agent expects `host:port` only. Port `443` is correct for a Caddy/Ingress fronting the KubeBolt backend. |
| `tls.enabled=true` | Encrypts the gRPC dial to the backend. Independent from `auth.mode`. | Omitting this with a TLS-fronted backend causes a confusing handshake failure that looks like the agent can't reach the host at all. |
| `cluster.name` | Operator-chosen label shown in the KubeBolt UI cluster selector. **Independent** of any cluster label the AMW ama-metrics scraper auto-injects. |
| `auth.mode=ingest-token` + `auth.ingestToken.existingSecret` | Authenticates the agent → backend channel. Without this, the gRPC handshake is rejected with `unauthenticated` and the agent never registers. The Secret was created in Prerequisite C2. |
| `scrape.enabled=false` | Disables the legacy vmagent scrape sidecar (you don't want it scraping in parallel with promread). **Does NOT** disable Mode A's DaemonSet kubelet-stats collector — that still runs and complements AMW with KubeBolt-canonical metrics. |
| `agent.promRead.enabled=true` | Spawns the promread Deployment (single replica, Lease-elected). |
| `agent.promRead.url` | The base AMW endpoint **without** `/api/v1/query_range` — the agent appends the path internally. |
| `agent.promRead.auth.mode=azureWorkloadIdentity` | Tells the agent to mint federated tokens via the WI webhook's env vars + token file. No key file needed. |
| `serviceAccount.annotations."azure\.workload\.identity/client-id"` | Links the KSA to the UAMI from Step 2. The backslash-escaping is for Helm's `--set` parser, not for YAML. |
| `podLabels."azure\.workload\.identity/use"="true"` | Opts the pod into the WI webhook's token injection. **Required** — see the note below. |
| `agent.deferNodeStress=true` | Disables Mode A's NodeStress collector (loadavg + PSI from /proc). **Required on AMW** because `--enable-azure-monitor-metrics` always deploys `ama-metrics-node` which scrapes node-exporter — without this flag the UI's Load average panel renders duplicate series, one from each path. See the dedicated note below. |

> **Escaping `azure.workload.identity/*`.** Helm's `--set` syntax
> treats `.` as a path separator, so both the annotation key AND the
> pod label key have to be backslash-escaped. If you'd rather avoid
> the escaping dance, put both in a values file and pass `-f`:
>
> ```yaml
> serviceAccount:
>   annotations:
>     azure.workload.identity/client-id: <UAMI_CLIENT_ID>
> podLabels:
>   azure.workload.identity/use: "true"
> auth:
>   mode: ingest-token
>   ingestToken:
>     existingSecret: kubebolt-ingest-token
> tls:
>   enabled: true
> ```

> **Why the pod label is required.** The Azure WI webhook is opt-in
> per-pod, gated on the `azure.workload.identity/use: "true"` label.
> Without it the webhook skips the pod and the Go SDK's
> `NewWorkloadIdentityCredential` will fail with
> `AZURE_FEDERATED_TOKEN_FILE not set`.

> **Default matchers are AMW-compatible.** The agent's default
> matcher set uses explicit metric names (`kube_pod_status_phase`,
> `node_load1`, etc.). AMW accepts both explicit names and regex on
> `__name__`, so the default works as-is. If you override
> `agent.promRead.matchers`, you have more flexibility on AMW than
> on GMP — but **keep them surgical** to avoid query-cost blow-ups
> (see [Cost notes](#cost-notes)).

> **Why `agent.deferNodeStress=true` on AMW.** Azure's
> `--enable-azure-monitor-metrics` always deploys the
> `ama-metrics-node` DaemonSet, which scrapes node-exporter on
> every node and writes `node_load1/5/15` +
> `node_pressure_*_waiting_seconds_total` into AMW. The agent's
> Mode A NodeStress collector (introduced in 1.13 to give GMP
> parity — GMP has no node-exporter scrape) would emit the **same
> metric family** from `/proc/loadavg` directly. Without
> `deferNodeStress=true`, the UI's Load average panel renders
> duplicate series (1m + 1m', 5m + 5m', 15m + 15m') — one from each
> path. This is **always** the case on AMW because ama-metrics is
> mandatory when AMW is enabled. On GMP the flag is the opposite
> default (NodeStress is the ONLY loadavg source on GKE). On AMP it
> depends on whether your remote_write source pushes node-exporter
> metrics — leave the flag at its `false` default if you don't, set
> to `true` if you do.

`cluster.name` is operator-chosen and shows up in the UI's cluster
selector. The chart also accepts `cluster.id` if you want to pin it;
when omitted, the agent derives it from the kube-system namespace
UID on first boot. The default `rbac.mode=reader` gives full
read-only inventory; to enable exec / port-forward / write actions,
see [Step 5](#step-5--optional-choose-the-agents-rbac-tier).

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

# 3. Logs show the agent registered with the backend (round-trip proof).
kubectl -n kubebolt logs -l kubebolt.dev/role=promread --tail=50 \
  | grep -E "cluster identity|channel registered|promread enabled|matcher failed"
# Expected:
#   INFO msg="cluster identity" cluster_id=<uuid> cluster_name=<your name>
#   INFO msg="promread enabled" url=https://...prometheus.monitor.azure.com auth_mode=azureWorkloadIdentity
#   INFO msg="channel registered" agent_id=<id> cluster_id=<uuid>
# RED FLAGS:
#   WARN msg="promread matcher failed" error="… AZURE_FEDERATED_TOKEN_FILE not set …"
#     → Pod label `azure.workload.identity/use: "true"` missing.
#       Verify with `kubectl get pod <pod> -o jsonpath='{.metadata.labels}'`
#       — must include the label. If absent, re-check Step 3's
#       `--set-string` (NOT `--set`) on the pod label.
#   WARN msg="promread matcher failed" error="… 401 Unauthorized …"
#     → Federated credential subject doesn't match. Re-verify Step 2's
#       `--subject "system:serviceaccount:${NAMESPACE}:${KSA}"`.
#   WARN msg="promread matcher failed" error="… 403 Forbidden …"
#     → UAMI lacks "Monitoring Data Reader" on the AMW scope.
#   (no "channel registered" line)
#     → backend reject. Check `auth.mode` and the ingest-token Secret.

# 4. Sample flow.
kubectl -n kubebolt logs -l kubebolt.dev/role=promread --tail=30 \
  | grep -E "samples collected|buffer stats"
# Expected:
#   INFO msg="samples collected" collector=promread count=N   (N typically 800-1500 on a 1-node AKS)
#   INFO msg="buffer stats" collected_total=… dropped_total=0
# Azure auto-deploys ama-metrics + KSM + node-exporter via
# --enable-azure-monitor-metrics, so a 1-node AKS typically returns
# 10-15× the samples of a fresh GMP or AMP install (which need
# external scrape sources to populate).
```

> **Verifying WI injection on a distroless image.** The
> `kubebolt-agent` image is distroless — no `sh`, no `env`, no
> shell utilities. `kubectl exec ... env` will fail with
> `exec: "sh": executable file not found`. To confirm the WI
> webhook injected the federated token chain, use describe
> instead:
> ```
> kubectl -n kubebolt describe pod <pod> | grep -E "AZURE_|azure-identity-token"
> ```
> Expected: `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`,
> `AZURE_FEDERATED_TOKEN_FILE`, `AZURE_AUTHORITY_HOST` all set,
> plus a `Mount` for `/var/run/secrets/azure/tokens` (read-only).

In the KubeBolt UI, the **Prometheus (read)** card under
`/admin/integrations` should flip from `Not installed` to `Installed`
within ~30-60 seconds. The wait is the lease handover (~5-15s) +
first poll cycle (~`agent.promRead.pollInterval`, default 30s). The
cluster also shows up in the UI's cluster selector with the
`cluster.name` you set in Step 3.

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
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
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

> **Workload Identity is orthogonal to `rbac.mode`.** The UAMI from
> Step 2 only governs the agent's access to Azure APIs (AMW
> queries). The `rbac.mode` flag governs the agent's access to the
> AKS cluster's own Kubernetes API. The two permission planes are
> independent — bumping `rbac.mode` does not require any UAMI or
> federated-credential changes.

---

## What the agent sees (vs Mode A)

Azure Managed Prometheus has the **most quirks** of the three managed
providers — two important ones the agent handles transparently:

### Quirk 1 — `instance` label is the VMSS instance name, not an IP

Azure auto-scrapes node-level metrics from the AKS VMSS, and the
`instance` label arrives shaped like `aks-nodepool1-35286437-vmss000000`
— which IS the Kubernetes node name. **NOT** the standard
`<pod-IP>:<port>` shape that AMP and self-managed Prom use.

The agent's `K8sNodeIndex` has a fallback path for this: if the
stripped `instance` value doesn't match any known IP in the cluster,
it checks whether the value matches a known **node name** directly,
and stamps `node=<instance>` in that case. No operator config needed.

### Quirk 2 — Azure adds 5 `microsoft.*` metadata labels

Every series carries:

| Label | Value pattern |
|---|---|
| `microsoft.amwresourceid` | `/subscriptions/.../accounts/<workspace>` |
| `microsoft.resourcegroupname` | the AKS cluster's RG |
| `microsoft.resourceid` | `/subscriptions/.../managedclusters/<cluster>` |
| `microsoft.resourcetype` | `microsoft.containerservice/managedclusters` |
| `microsoft.subscriptionid` | the Azure subscription UUID |

Useful for multi-tenant attribution but adds cardinality. If your VM
storage is tight, drop them with a metric relabeling rule in the
agent's chart (not exposed via `--set` today — file an issue if you
need this surfaced).

### Quirk 3 — Retina flows are included

If your AKS cluster has Azure's `networkobservability-retina` enabled
(Azure's Cilium-equivalent flow observer), AMW auto-scrapes its
metrics too. Mode C pulls them into KubeBolt automatically, which
gives you flow-level visibility on AKS without running Cilium/Hubble.

### Quirk 4 — `node_load*` and `node_pressure_*` double-emission

This is the **most important Azure-specific operational quirk** —
miss it and the UI's Load average panel will show duplicate series
(1m + 1m', 5m + 5m', 15m + 15m') for the entire lifetime of the
install. The mechanism:

1. **Mode A path** — the agent's NodeStress collector (added in
   1.13.0 to give GMP-on-GKE parity, since GMP doesn't scrape
   node-exporter) reads `/proc/loadavg` directly on every node and
   emits `node_load1/5/15` + `node_pressure_*_waiting_seconds_total`
   under `source="kubebolt-agent"`.
2. **Mode C path** — `--enable-azure-monitor-metrics` deploys the
   `ama-metrics-node` DaemonSet, which scrapes node-exporter on
   every node and ships the **same** metric family into AMW. The
   agent's promread leader pulls them back out with Azure-injected
   labels (`microsoft.*`, `instance=<vmss-name>`).

Both paths land in VictoriaMetrics with different label sets, so
the UI's chart engine renders them as distinct series. The Step 3
install command sets `agent.deferNodeStress=true` to disable the
Mode A path; the Mode C path stays authoritative on AMW.

**Cross-provider matrix:**

| Provider | Recommended `deferNodeStress` | Why |
|---|---|---|
| AMW (Azure) | **`true`** | ama-metrics always scrapes node-exporter — guaranteed duplication if NodeStress also runs |
| AMP (AWS) | depends on your `remote_write` source | If kube-prometheus-stack pushes node-exporter → `true`; if nothing pushes → leave at `false` default |
| GMP (GCP) | **`false`** (default) | GMP doesn't scrape node-exporter — NodeStress is the **only** loadavg source on GKE |

**Validation:** if you ever see Load average rendering 6 lines
instead of 3, the agent's `--set agent.deferNodeStress=true` was
either omitted at install time or got wiped by a `helm upgrade`
without `--reuse-values`. Re-apply it.

> **Future-proofing.** A chart-side enhancement (1.13.1+) is
> tracked to auto-defer NodeStress when `auth.mode` is
> `azureWorkloadIdentity` (an always-true predicate on AMW). Once
> shipped, this flag becomes redundant for new installs and the
> doc patch will drop it from the Step 3 command.

### Universal labels

| Label | How it gets there |
|---|---|
| `node` | NOT auto-stamped by AMW (it's in `instance`). Agent synthesizes via the K8sNodeIndex name-fallback described above. |
| `cluster` | AMW auto-stamps from the AKS cluster name. |
| `cluster_id` | The agent stamps this client-side from kube-system UID — does NOT come from AMW. |
| `cluster_name` (display) | The agent stamps this from `cluster.name` chart value. |

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
benchmarks (S1 multi-node smoke 2026-05-26) and run up the AMW
query bill.

---

## Cost notes

Azure Monitor Workspace charges per sample **ingested** and per
**query API call**. Mode C adds query API charges (the agent polls
every 30s by default) but does NOT change ingestion — the agent is
reading samples AMW already has.

Default poll cadence (every 30s, ~10 matchers) works out to roughly:

```
2 calls/min × 60 × 24 × 30 = ~86,400 extra query calls/month
```

Azure's query pricing sits between GCP (cheapest) and AWS (most
expensive). Verify against the
[Azure Monitor pricing page](https://azure.microsoft.com/en-us/pricing/details/monitor/)
for your workspace's volume.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Pod logs show `AZURE_FEDERATED_TOKEN_FILE: no such file` | Pod label missing — WI webhook skipped the pod | `kubectl get pod -n kubebolt -l kubebolt.dev/role=promread -o jsonpath='{.items[0].metadata.labels}'` — must include `azure.workload.identity/use: "true"`. If missing, redeploy with the chart's `podLabels` flag |
| Pod logs show `AADSTS70021: No matching federated identity record` | Federated credential subject doesn't match the actual KSA | Re-check `az identity federated-credential show ...` — the `--subject` must be exactly `system:serviceaccount:<namespace>:<ksa-name>` |
| Pod logs show `403 Forbidden ... Monitoring Data Reader` | UAMI lacks the role on AMW | Re-run `az role assignment create --assignee-object-id $UAMI_PRINCIPAL_ID --role "Monitoring Data Reader" --scope $AMW_ID` — note `--assignee-object-id` (principal_id), NOT `--assignee` (which expects client_id and silently fails for UAMIs) |
| KSA annotation missing | Forgot the `serviceAccount.annotations` chart flag | `kubectl describe sa kubebolt-agent -n kubebolt` — must show `azure.workload.identity/client-id: <UAMI client_id>` |
| Card still says `Not installed` after 5 min | Cluster UID not stamped on the leader gauge | Check `kubectl -n kubebolt logs -l kubebolt.dev/role=promread \| grep cluster_id` — should show a UUID. If empty, set `--set cluster.id=<kube-system UID>` explicitly |
| Pod logs show `Server returned 401 InvalidAuthenticationToken` | Token resource scope is wrong (Azure SDK picked the default scope, not `prometheus.monitor.azure.com`) | This shouldn't happen with the chart's wired `azureWorkloadIdentity` provider — file an issue if it does, with the full log line |
| Load average panel renders 6 lines instead of 3 (1m + 1m', 5m + 5m', 15m + 15m') | Mode A NodeStress + Mode C promread both emit `node_load*` — duplicate paths to VictoriaMetrics | `helm upgrade kubebolt-agent ... --reuse-values --set agent.deferNodeStress=true` — see [Quirk 4](#quirk-4--node_load-and-node_pressure_-double-emission) for the full mechanism |

---

## Cleanup

**Order matters.** The role assignment on the AMW lives in a
different resource group than the UAMI. If you delete the cluster RG
before cleaning the role assignment, the assignment becomes
orphaned. Doesn't break anything active but pollutes
`az role assignment list` forever.

```bash
set -u

RG=<your-aks-resource-group>
NAMESPACE=kubebolt
UAMI=kubebolt-promread

# 1. Remove the agent (helm release + all ClusterRole/CRB resources).
helm uninstall kubebolt-agent -n "$NAMESPACE"

# 2. Federated credential (must precede UAMI delete — it hangs off UAMI).
az identity federated-credential delete \
  --name "${UAMI}-fed" \
  --identity-name "$UAMI" \
  --resource-group "$RG" --yes

# 3. Role assignment on AMW — use principal_id, NOT client_id.
UAMI_PRINCIPAL_ID=$(az identity show \
  --resource-group "$RG" --name "$UAMI" \
  --query principalId --output tsv)
AMW_ID=$(az resource list \
  --resource-type "Microsoft.Monitor/accounts" \
  --query "[0].id" --output tsv)

az role assignment delete \
  --assignee-object-id "$UAMI_PRINCIPAL_ID" \
  --scope "$AMW_ID"
# Note: --assignee-principal-type is for `create`, not `delete` —
# the delete command rejects it as unrecognized.

# 4. UAMI itself.
az identity delete --name "$UAMI" --resource-group "$RG"

# 5. Remove the namespace (and the ingest-token Secret it holds).
kubectl delete namespace "$NAMESPACE"
```

The AMW you typically keep — it costs only for samples ingested and
queries processed, both of which drop to zero when no source is
pushing or reading. If you DO want to delete it:

```bash
# AMW is NOT deleted automatically when you delete the AKS cluster,
# even if it was auto-provisioned by --enable-azure-monitor-metrics
# at cluster create time. The two resources are independent.
az monitor account delete \
  --name "DefaultAzureMonitorWorkspace-<region>" \
  --resource-group "DefaultResourceGroup-<region>" --yes
```

**Optional — delete the AKS cluster (and its RG).** If this cluster
was created solely to evaluate the KubeBolt + AMW integration and
you don't plan to keep it around, the fastest way to stop control-
plane + node billing is to delete the whole resource group, which
tears down the cluster, the VMSS node pool, the load balancer, NSGs,
and any AMW that lives in that same RG in a single async operation:

```bash
CLUSTER=<your-aks-cluster-name>

# Granular: cluster only (keeps RG + AMW + UAMI artifacts elsewhere).
az aks delete --name "$CLUSTER" --resource-group "$RG" --yes --no-wait

# Or atomic: the whole RG (deletes cluster + every resource in it).
az group delete --name "$RG" --yes --no-wait
```

Both take ~5-10 minutes async. **Do NOT run either against a
production resource group** — the rest of this section (helm
uninstall + federated cred + role assignment + UAMI + namespace
delete) is enough to fully remove KubeBolt from a cluster you want
to keep.

---

## References

- Parent mode matrix: [`prometheus.md`](./prometheus.md)
- AMP recipe (same pattern, different auth): [`aws-amp.md`](./aws-amp.md)
- GCP GMP recipe: [`gcp-managed-prometheus.md`](./gcp-managed-prometheus.md)
- Self-managed Prom (Basic auth / Bearer / None): [`self-managed-prom-readonly.md`](./self-managed-prom-readonly.md)
- [Azure — Workload Identity overview](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview)
- [Azure — Managed Prometheus query API](https://learn.microsoft.com/en-us/azure/azure-monitor/essentials/prometheus-api-promql)
