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

| Requirement | Why |
|---|---|
| AKS cluster with `--enable-oidc-issuer` AND `--enable-workload-identity` (and `--enable-azure-monitor-metrics` if you want AMW auto-provisioned) | OIDC issuer is what Azure AD trusts to mint federated tokens. Workload Identity is the in-cluster webhook that injects env vars + token file into pods carrying the right label. |
| An Azure Monitor Workspace in the subscription | Created automatically when `--enable-azure-monitor-metrics` is on; or via `az monitor account create` separately. AMW holds the Prom-API endpoint the agent queries. |
| `Contributor` or composite of `Microsoft.ManagedIdentity/userAssignedIdentities/*` + `Microsoft.Authorization/roleAssignments/write` on the cluster's RG | To create the UAMI and assign the role. One-time setup; the agent itself only needs `Monitoring Data Reader`. |
| `az` CLI installed and logged in | Used for all Azure-side resource creation. |
| KubeBolt backend reachable from the cluster | Same as any other agent install — the agent ships samples via gRPC to your KubeBolt host. |

**KSM and node-exporter are included** when AMW is enabled via
`--enable-azure-monitor-metrics` — Azure auto-deploys
`ama-metrics` pods that scrape `kubelet`, `cadvisor`, `node`, and
optionally `networkobservability-retina`. You do not need a separate
`kube-prometheus-stack` install on Azure for the default Mode C
matchers to find data.

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
  --query accessToken -o tsv)

curl -s -H "Authorization: Bearer $TOKEN" \
  "${AMW_ENDPOINT}/api/v1/query?query=count(up)"
# → {"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[...,"5"]}]}}
```

A non-zero count means AMW is ingesting and the bearer-auth path
works. If you get a 403, your account needs the `Monitoring Data
Reader` role on the AMW resource scope.

---

## Step 2 — Create the UAMI and bind it to a KSA via Workload Identity

Azure Workload Identity has the **most resources to wire** of the
three managed providers — 5 sequential steps. This is the trade-off
for a fully ambient-credential runtime path.

```bash
RG=<your-aks-resource-group>
CLUSTER=<your-aks-cluster-name>
LOCATION=eastus
NAMESPACE=kubebolt
KSA=kubebolt-agent   # the chart's default; override with serviceAccount.name
UAMI=kubebolt-promread

# 1. Create the User-Assigned Managed Identity.
az identity create \
  --name $UAMI \
  --resource-group $RG \
  --location $LOCATION

UAMI_CLIENT_ID=$(az identity show \
  --resource-group $RG --name $UAMI \
  --query clientId -o tsv)
UAMI_PRINCIPAL_ID=$(az identity show \
  --resource-group $RG --name $UAMI \
  --query principalId -o tsv)

# 2. Grant the UAMI Monitoring Data Reader on the AMW scope.
AMW_ID=$(az resource list \
  --resource-type "Microsoft.Monitor/accounts" \
  --query "[0].id" -o tsv)

az role assignment create \
  --assignee-object-id $UAMI_PRINCIPAL_ID \
  --assignee-principal-type ServicePrincipal \
  --role "Monitoring Data Reader" \
  --scope $AMW_ID

# 3. Grab the cluster's OIDC issuer URL — Azure AD will trust this
#    issuer's tokens when minting federated credentials.
OIDC_ISSUER=$(az aks show \
  --resource-group $RG --name $CLUSTER \
  --query "oidcIssuerProfile.issuerUrl" -o tsv)

# 4. Create the federated credential binding UAMI ↔ AKS OIDC ↔ KSA.
az identity federated-credential create \
  --name "${UAMI}-fed" \
  --identity-name $UAMI \
  --resource-group $RG \
  --issuer $OIDC_ISSUER \
  --subject "system:serviceaccount:${NAMESPACE}:${KSA}" \
  --audience api://AzureADTokenExchange
```

> **Why 5 steps.** Each Azure resource lives in a different control
> plane: UAMI is in `Microsoft.ManagedIdentity`, the role assignment
> is in `Microsoft.Authorization`, the federated credential lives
> under the UAMI, and the KSA annotation (Step 3 below) is the only
> piece on the K8s side. AWS IRSA collapses several of these into a
> single `eksctl create iamserviceaccount`; Azure doesn't have an
> equivalent one-shot CLI.

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
label so the WI webhook injects the federated token + env vars.

```bash
UAMI_CLIENT_ID=$(az identity show \
  --resource-group $RG --name $UAMI \
  --query clientId -o tsv)

helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set backendUrl=<your-kubebolt-host:443> \
  --set cluster.name=<your-cluster-name> \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url="${AMW_ENDPOINT}" \
  --set agent.promRead.auth.mode=azureWorkloadIdentity \
  --set serviceAccount.annotations."azure\.workload\.identity/client-id"="${UAMI_CLIENT_ID}" \
  --set podLabels."azure\.workload\.identity/use"="true"
```

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
> ```

> **Why the pod label is required.** The Azure WI webhook is opt-in
> per-pod, gated on the `azure.workload.identity/use: "true"` label.
> Without it the webhook skips the pod and the Go SDK's
> `NewWorkloadIdentityCredential` will fail with
> `AZURE_FEDERATED_TOKEN_FILE not set`.

`cluster.name` is operator-chosen and shows up in the UI's cluster
selector. The chart also accepts `cluster.id` if you want to pin it;
when omitted, the agent derives it from the kube-system namespace
UID on first boot.

---

## Step 4 — Verify

Three things to check, in order:

```bash
# 1. The promread pod is Running.
kubectl -n kubebolt get pods -l kubebolt.dev/role=promread
# Expected: 1 pod, STATUS=Running.

# 2. The leader lease is held.
kubectl -n kubebolt get lease kubebolt-promread
# Expected: HOLDER=<pod-name>.

# 3. Logs show successful collection.
kubectl -n kubebolt logs -l kubebolt.dev/role=promread --tail=20
# Expected: "promread: collected N samples" lines, no auth errors.
```

In the KubeBolt UI, the **Prometheus (read)** card under
`/admin/integrations` should flip from `Not installed` to `Installed`
within ~1 minute. The wait is the lease handover + first poll cycle.

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

---

## Cleanup

**Order matters.** The role assignment on the AMW lives in a
different resource group than the UAMI. If you delete the cluster RG
before cleaning the role assignment, the assignment becomes
orphaned. Doesn't break anything active but pollutes
`az role assignment list` forever.

```bash
RG=<your-aks-resource-group>
NAMESPACE=kubebolt
UAMI=kubebolt-promread

# 1. Remove the agent.
helm uninstall kubebolt-agent -n $NAMESPACE

# 2. Federated credential (must precede UAMI delete — it hangs off UAMI).
az identity federated-credential delete \
  --name "${UAMI}-fed" \
  --identity-name $UAMI \
  --resource-group $RG --yes

# 3. Role assignment on AMW — use principal_id, NOT client_id.
UAMI_PRINCIPAL_ID=$(az identity show \
  --resource-group $RG --name $UAMI \
  --query principalId -o tsv)
AMW_ID=$(az resource list \
  --resource-type "Microsoft.Monitor/accounts" \
  --query "[0].id" -o tsv)

az role assignment delete \
  --assignee-object-id $UAMI_PRINCIPAL_ID \
  --scope $AMW_ID
# Note: --assignee-principal-type is for `create`, not `delete` —
# the delete command rejects it as unrecognized.

# 4. UAMI itself.
az identity delete --name $UAMI --resource-group $RG
```

The AMW you typically keep — it costs only for samples ingested and
queries processed, both of which drop to zero when no source is
pushing or reading.

---

## References

- Parent mode matrix: [`prometheus.md`](./prometheus.md)
- AMP recipe (same pattern, different auth): [`aws-amp.md`](./aws-amp.md)
- GCP GMP recipe: [`gcp-managed-prometheus.md`](./gcp-managed-prometheus.md)
- Self-managed Prom (Basic auth / Bearer / None): [`self-managed-prom-readonly.md`](./self-managed-prom-readonly.md)
- [Azure — Workload Identity overview](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview)
- [Azure — Managed Prometheus query API](https://learn.microsoft.com/en-us/azure/azure-monitor/essentials/prometheus-api-promql)
