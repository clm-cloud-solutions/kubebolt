# Amazon Managed Service for Prometheus (AMP) — read with kubebolt-agent (Mode C)

> **Available from KubeBolt 1.13.0+.** Earlier releases only supported
> Mode A (agent scrapes targets directly) and Mode B (customer's Prom
> pushes to KubeBolt). Mode C — agent **reads** from a customer-managed
> Prometheus — landed in 1.13 to cover the three managed Prom services
> where outbound `remote_write` is either impossible (AMP) or
> change-management-restricted (often the case with GMP).
>
> See the [mode matrix in `prometheus.md`](./prometheus.md#which-ingest-mode-fits-your-cluster)
> for when to pick Mode C over A or B.

This page is the **AWS-specific recipe** for Mode C. It assumes you
already understand the topology described in the Prometheus parent
doc; here we only cover what's AWS-flavored: IRSA binding, the AMP
query endpoint shape, and the IAM role the agent needs.

> **Why Mode C exists for AMP.** AMP is **sink-only** — it accepts
> `remote_write` ingress but does **not** expose an outbound
> `remote_write` API. Mode B (Prom-pushes-to-KubeBolt) is therefore
> impossible for AMP. Mode C is the only path that lets a KubeBolt
> install reuse an existing AMP workspace.

---

## What you'll end up with

```
AMP (managed Prom in AWS)
      ▲ query_range every 30s
      │  IRSA → KSA → IAM Role → AmazonPrometheusQueryAccess
      │  per-request SigV4 signing (region-scoped)
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
section](../../CLAUDE.md#packagesagent)). One leader polls AMP; if
the pod dies the next scheduled pod takes over via Kubernetes Lease.

---

## Prerequisites

| Requirement | Why |
|---|---|
| EKS cluster with IRSA enabled (`eksctl create cluster --with-oidc` or equivalent) | IRSA is the mechanism by which a KSA inside the cluster assumes an IAM role. The cluster's OIDC provider is what AWS STS trusts to mint temporary credentials. |
| AMP workspace in the same AWS account, region of your choice | The agent queries this workspace's region-scoped endpoint. Cross-account AMP is possible via IAM trust policy edits — out of scope for this guide. |
| `IAMFullAccess` (or composite of `iam:CreatePolicy`, `iam:CreateRole`, `eks:DescribeCluster`) for the operator running `eksctl` | One-time setup; the agent itself only needs `AmazonPrometheusQueryAccess`. |
| `eksctl` CLI installed | Used for the IRSA one-shot. The same setup is possible via raw IAM + trust policy edits but takes 4-5 manual steps. |
| KubeBolt backend reachable from the cluster | Same as any other agent install — the agent ships samples via gRPC to your KubeBolt host. |

**No KSM install needed if your remote_write source already scrapes
KSM** — AMP holds whatever your existing Prom (typically
`kube-prometheus-stack` with `remoteWrite.sigv4.region` set) pushes to
it. Unlike GMP, AMP does NOT auto-scrape — it's a pure ingest sink.

---

## Step 1 — Locate the AMP query endpoint

AMP's Prom-API-compatible endpoint is **region-scoped per workspace**:

```bash
REGION=us-east-1
WORKSPACE_ID=$(aws amp list-workspaces --region $REGION \
  --query 'workspaces[?alias==`<your-workspace-alias>`].workspaceId' \
  -o text)

AMP_ENDPOINT=$(aws amp describe-workspace \
  --workspace-id $WORKSPACE_ID --region $REGION \
  --query 'workspace.prometheusEndpoint' -o text)

echo "$AMP_ENDPOINT"
# → https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-58f6ca56-4831-41e9-8513-ea1b1e59a737/
```

> **Gotcha — don't use `list-workspaces` for the endpoint.**
> `aws amp list-workspaces` does NOT include the `prometheusEndpoint`
> field. Use `describe-workspace --workspace-id <id>` (as above), or
> construct the URL from the known pattern:
>
> ```
> https://aps-workspaces.<region>.amazonaws.com/workspaces/<workspace-id>/
> ```

Smoke-test it from your laptop before going further. You'll need
[`awscurl`](https://github.com/okigan/awscurl) which handles SigV4
signing for arbitrary AWS service URLs:

```bash
# macOS: PEP 668 may block `pip install awscurl` — pipx is the
# cleanest workaround:
brew install pipx && pipx install awscurl

awscurl --service=aps --region=$REGION \
  "${AMP_ENDPOINT}api/v1/query?query=up"
# → {"status":"success","data":{"resultType":"vector","result":[...]}}
```

An empty `result` array is **expected** on a freshly created AMP
workspace with no remote_write source pushing yet — the empty + HTTP
200 response still proves SigV4 signing + endpoint reachability work.

---

## Step 2 — Create the IAM role and bind it to a KSA via IRSA

The agent does **not** use a static AWS access key. It uses IRSA: a
Kubernetes ServiceAccount (KSA) inside the cluster assumes an IAM
role via STS, and the Go SDK's `LoadDefaultConfig` mints short-lived
credentials transparently via the EKS Pod Identity Webhook.

The cleanest path is `eksctl create iamserviceaccount`, which in
**one command** creates the IAM role, attaches the trust policy
against the cluster's OIDC provider, creates the KSA, annotates it
with the role ARN, and attaches the managed policy:

```bash
CLUSTER=<your-eks-cluster-name>
REGION=us-east-1
NAMESPACE=kubebolt
KSA=kubebolt-agent   # the chart's default; override with serviceAccount.name

eksctl create iamserviceaccount \
  --cluster $CLUSTER \
  --region $REGION \
  --namespace $NAMESPACE \
  --name $KSA \
  --attach-policy-arn arn:aws:iam::aws:policy/AmazonPrometheusQueryAccess \
  --override-existing-serviceaccounts \
  --approve
```

> **Why `AmazonPrometheusQueryAccess`.** This AWS-managed policy
> grants exactly what Mode C needs: `aps:QueryMetrics`,
> `aps:GetSeries`, `aps:GetLabels`, `aps:GetMetricMetadata`. It does
> NOT grant ingest or admin privileges. Custom least-privilege
> policies are possible but the managed policy is the right default —
> AWS updates it as new query verbs land.

> **If the namespace doesn't exist yet,** create it first:
> `kubectl create namespace kubebolt`. `eksctl` will fail on a
> missing namespace.

> **Note on KSA naming.** The chart's default `serviceAccount.name`
> resolves to `kubebolt-agent` (the fullname). If you override it
> (`--set serviceAccount.name=<custom>`), use that name in the
> `eksctl` command. The agent's promread Deployment uses the same
> KSA as the DaemonSet.

---

## Step 3 — Install the agent with Mode C enabled

The KSA was annotated with the IAM role ARN in Step 2 by `eksctl` —
no `serviceAccount.annotations` chart flag needed. Pass
`serviceAccount.create=false` so Helm reuses the KSA `eksctl`
already created.

```bash
REGION=us-east-1
WORKSPACE_ID=<your-workspace-id>
AMP_ENDPOINT="https://aps-workspaces.${REGION}.amazonaws.com/workspaces/${WORKSPACE_ID}/"

helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set backendUrl=<your-kubebolt-host:443> \
  --set cluster.name=<your-cluster-name> \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url="${AMP_ENDPOINT}" \
  --set agent.promRead.auth.mode=awsSigV4 \
  --set agent.promRead.auth.awsRegion=${REGION} \
  --set serviceAccount.create=false \
  --set serviceAccount.name=kubebolt-agent
```

> **Why `awsRegion` is required.** SigV4 signs against the region —
> mismatched region means signed-but-rejected requests with a 403
> response. The agent's chart's render check (`validatePromRead` in
> `_helpers.tpl`) hard-fails if you set `auth.mode=awsSigV4` without
> a region; this is intentional, the alternative is a confusing 403
> at runtime.

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

AMP does **not** auto-scrape and does **not** auto-add labels (unlike
GMP and Azure Managed Prometheus). What lands in AMP is exactly what
the customer's remote_write source pushed — typically
`kube-prometheus-stack` with `remoteWrite.sigv4.region` and standard
node-exporter labels.

| Label | How it gets there |
|---|---|
| `node` | NOT auto-stamped by AMP. Agent synthesizes via `K8sNodeIndex` IP→nodeName lookup, stripping the port from `instance` (host-network pod IP = node InternalIP for node-exporter). Works because AMP receives standard node-exporter `instance=<IP>:<port>` shape. |
| `cluster_id` | The agent stamps this client-side from kube-system UID — does NOT come from AMP. |
| `instance` | Whatever the remote_write source set. Standard node-exporter is `<pod-IP>:9100`. |
| `cluster_name` (display) | The agent stamps this from `cluster.name` chart value. |

The agent's existing `K8sNodeIndex` IP→nodeName lookup path works
as-is for AMP — this was actually the original design target before
Azure and GCP coverage was added.

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
benchmarks (S1 multi-node smoke 2026-05-26) and run up the AMP
query bill.

---

## Cost notes

AMP charges per sample **ingested** and per **query GiB processed**.
Mode C adds query GiB charges (the agent polls every 30s by default)
but does NOT change ingestion — the agent is reading samples already
in the workspace.

Default poll cadence (every 30s, ~10 matchers) is typically the
**highest** of the three managed providers in cost because AMP's
query pricing is the most expensive at the time of writing. Verify
against the
[AWS Managed Service for Prometheus pricing page](https://aws.amazon.com/prometheus/pricing/)
for your workspace's volume.

If query cost is a concern, bump `agent.promRead.pollInterval` from
the default `30s` to `60s` — UI panels show 1-min trends fine with
the slower cadence and you halve the query volume.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Pod logs show `WebIdentityErr: failed to retrieve credentials` | IRSA env vars not injected (KSA not annotated, or webhook not running) | `kubectl describe sa kubebolt-agent -n kubebolt` — must show `eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/<role>`. If missing, re-run `eksctl create iamserviceaccount --override-existing-serviceaccounts` |
| Pod logs show `AccessDenied: aps:QueryMetrics` | IAM role lacks `AmazonPrometheusQueryAccess` | `aws iam list-attached-role-policies --role-name <role>` — attach the managed policy if missing |
| Pod logs show `signed request ... InvalidSignatureException` | `agent.promRead.auth.awsRegion` doesn't match the AMP workspace's region | Re-set `--set agent.promRead.auth.awsRegion=<correct-region>` and redeploy |
| Pod logs show `lookup aps-workspaces...: no such host` | Endpoint URL is wrong (typo in workspace ID or region) | Re-derive via `aws amp describe-workspace --workspace-id <id>` |
| Card still says `Not installed` after 5 min | Cluster UID not stamped on the leader gauge | Check `kubectl -n kubebolt logs -l kubebolt.dev/role=promread \| grep cluster_id` — should show a UUID. If empty, set `--set cluster.id=<kube-system UID>` explicitly |
| Pod restarts every ~1h with no error logged | STS credential refresh failing silently | Check the IAM role's trust policy still references the cluster's OIDC provider correctly: `aws iam get-role --role-name <role> --query 'Role.AssumeRolePolicyDocument'` |

---

## Cleanup

```bash
CLUSTER=<your-eks-cluster-name>
REGION=us-east-1
NAMESPACE=kubebolt
KSA=kubebolt-agent

# 1. Remove the agent.
helm uninstall kubebolt-agent -n $NAMESPACE

# 2. Remove the IRSA binding (deletes IAM role + KSA + CFN stack atomically).
eksctl delete iamserviceaccount \
  --cluster $CLUSTER \
  --region $REGION \
  --namespace $NAMESPACE \
  --name $KSA
```

The AMP workspace itself you typically keep — it's billed only for
samples ingested and queries processed, both of which drop to zero
when no source is pushing or reading. If you want to delete it:

```bash
aws amp delete-workspace --workspace-id <id> --region $REGION
```

---

## References

- Parent mode matrix: [`prometheus.md`](./prometheus.md)
- GCP GMP recipe (same pattern, different auth): [`gcp-managed-prometheus.md`](./gcp-managed-prometheus.md)
- Azure Managed Prometheus recipe: [`azure-managed-prometheus.md`](./azure-managed-prometheus.md)
- Self-managed Prom (Basic auth / Bearer / None): [`self-managed-prom-readonly.md`](./self-managed-prom-readonly.md)
- [AWS — IAM roles for service accounts (IRSA)](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html)
- [AWS — Amazon Managed Service for Prometheus query API](https://docs.aws.amazon.com/prometheus/latest/userguide/AMP-APIReference.html)
