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

### A. Local toolchain (on your laptop)

| Tool | Verify | Notes |
|---|---|---|
| `aws` CLI v2 | `aws --version` | Required for IAM + AMP setup. v1 works but v2 is the documented baseline. |
| AWS authenticated | `aws sts get-caller-identity` | Must return the account you intend to use. **Avoid root** for daily ops — use an IAM user/role with AdministratorAccess (or the composite below) instead. |
| `kubectl` | `kubectl version --client` | >= 1.27 recommended. |
| `helm` v3 | `helm version --short` | |
| `eksctl` | `eksctl version` | The one-shot for IRSA setup. Required path; the manual IAM-trust-policy route is 4-5 fiddly steps. |
| `awscurl` (smoke testing only) | `awscurl --version` | Signs SigV4 against arbitrary AWS service URLs. macOS install: `brew install pipx && pipx install awscurl` (PEP 668 blocks bare `pip install`). |

### B. AWS account + EKS cluster + AMP workspace

Pick the path that matches your starting point:

**B1 — Greenfield (no EKS cluster, no AMP workspace):**

> **Run the variable-setting and the command in the SAME shell
> session.** If the `eksctl` line is pasted into a fresh terminal
> with unset variables, `--region` will silently consume the next
> flag (`--version`) and you'll see a confusing
> `sts.--version.amazonaws.com: no such host` error. Either keep
> them in one block, or use the inline-env one-liner form shown
> below. The `set -u` at the top makes an unset variable hard-fail
> at expansion time so the error points at the right cause.

```bash
set -u

REGION=us-east-1                  # pick your region
CLUSTER=my-kubebolt-cluster       # pick a name
WORKSPACE_ALIAS=my-kubebolt-amp   # pick an alias
K8S_VERSION=1.35                  # 1.35+ recommended; verify supported via `aws eks describe-addon-versions --kubernetes-version <ver>`

# Create the EKS cluster WITH the IRSA OIDC provider attached.
eksctl create cluster \
  --name "$CLUSTER" \
  --region "$REGION" \
  --version "$K8S_VERSION" \
  --with-oidc \
  --managed

# Create the AMP workspace and capture its workspace-id.
WORKSPACE_ID=$(aws amp create-workspace \
  --alias "$WORKSPACE_ALIAS" --region "$REGION" \
  --query 'workspaceId' --output text)
echo "WORKSPACE_ID=$WORKSPACE_ID"
```

The three flags that matter most: `--with-oidc` enables IRSA
(without it, Step 2's `eksctl create iamserviceaccount` will fail
to find an OIDC provider). `--managed` uses EKS-managed node groups
— less infra surface to maintain than self-managed nodes.
`--version` pins the K8s version; omit only if you want eksctl's
current default (which lags the latest GA by ~1 minor).

> EKS cluster creation typically takes ~15 minutes (control plane +
> managed node group + addons). AMP workspace creation is
> ~10 seconds.

> **Validation-only cost optimization.** For ad-hoc evaluation that
> you'll tear down within a day, append `--node-type t3.medium
> --nodes 1 --nodes-min 1 --nodes-max 1` to the `eksctl create
> cluster` command. Default `m5.large × 2` costs ~$4.60/day in
> nodes; t3.medium × 1 drops nodes to ~$1.00/day (control plane
> $2.40/day is fixed regardless). t3.medium with 2 vCPU + 4 GiB is
> the practical floor for the EKS addons (CoreDNS + kube-proxy +
> aws-node) plus the KubeBolt agent stack — t3.small is too tight.
> **Do NOT use this sizing for production.**

**B2 — Existing EKS cluster WITHOUT IRSA:**

```bash
eksctl utils associate-iam-oidc-provider \
  --cluster $CLUSTER --region $REGION --approve
```

**B3 — Existing EKS cluster with IRSA, but no AMP workspace:**

```bash
WORKSPACE_ID=$(aws amp create-workspace \
  --alias my-kubebolt-amp --region $REGION \
  --query 'workspaceId' --output text)
```

**Verification (all paths):**

```bash
# OIDC provider associated with the cluster
aws eks describe-cluster --name $CLUSTER --region $REGION \
  --query 'cluster.identity.oidc.issuer' --output text
# Expected: https://oidc.eks.<region>.amazonaws.com/id/<hash>  (NOT empty)

# AMP workspace ACTIVE (not CREATING / CREATION_FAILED)
aws amp describe-workspace --workspace-id $WORKSPACE_ID --region $REGION \
  --query 'workspace.status.statusCode' --output text
# Expected: ACTIVE

# Your IAM (need this to run Step 2's IRSA mutations)
aws sts get-caller-identity --query 'Arn' --output text
# Verify the principal has either IAMFullAccess OR the composite
# (iam:CreatePolicy + iam:CreateRole + eks:DescribeCluster +
# iam:AttachRolePolicy). Root and AdministratorAccess satisfy both.
```

**No KSM install needed if your remote_write source already scrapes
KSM** — AMP holds whatever your existing Prom (typically
`kube-prometheus-stack` with `remoteWrite.sigv4.region` set) pushes
to it. Unlike GMP, AMP does NOT auto-scrape — it's a pure ingest
sink. If you don't have a remote_write source yet, the integration
will still install but queries will return empty results until a
source starts pushing.

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
| **`ingest-token`** (recommended for SaaS / cross-cluster) | Backend is remote from the agent; multi-cluster operators | Issue a bearer token in the backend UI → `Admin` → `Agent tokens` → label it (e.g. `eks-prod`), keep the `kb_...` value handy |
| **`tokenreview`** | Backend runs in the SAME cluster as the agent (self-hosted single-cluster) | Backend chart already grants `tokenreviews/create`; no per-cluster prep |
| **`none`** | Dev only | Skip |

If you chose **`ingest-token`** (the common path), prepare the
Secret in the EKS cluster's `kubebolt` namespace BEFORE Step 3:

```bash
# Get EKS credentials if you haven't yet
aws eks update-kubeconfig --name $CLUSTER --region $REGION

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
EKS networking (no custom egress NetworkPolicy, no AWS Network
Firewall rules, public NAT for the node subnets) these all work
out of the box.

| Destination | Port | Purpose |
|---|---|---|
| `sts.<region>.amazonaws.com` | 443 | IRSA `AssumeRoleWithWebIdentity` for the agent's KSA |
| `aps-workspaces.<region>.amazonaws.com` | 443 | AMP query API — where the agent reads from |
| Your KubeBolt backend host | 443 (TLS) or 9090 (plain gRPC) | Where the agent ships samples |

If your cluster has restrictive egress controls:
- **NetworkPolicy** — add an egress rule in the `kubebolt` namespace
  allowing the three destinations above
- **VPC endpoints** — if your node subnets have **no NAT** (fully
  private), create AWS PrivateLink endpoints for `sts` and `aps`
  in your VPC. The KubeBolt backend reachability is a separate
  routing question.
- **AWS Network Firewall** — add stateful rules allowing the three
  hosts above

---

## Step 1 — Locate the AMP query endpoint

AMP's Prom-API-compatible endpoint is **region-scoped per workspace**:

```bash
REGION=us-east-1
WORKSPACE_ID=$(aws amp list-workspaces --region $REGION \
  --query 'workspaces[?alias==`<your-workspace-alias>`].workspaceId' \
  --output text)

AMP_ENDPOINT=$(aws amp describe-workspace \
  --workspace-id $WORKSPACE_ID --region $REGION \
  --query 'workspace.prometheusEndpoint' --output text)

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
set -u

CLUSTER=<your-eks-cluster-name>
REGION=us-east-1
NAMESPACE=kubebolt
KSA=kubebolt-agent   # the chart's default; override with serviceAccount.name

# 0. Pre-flight: confirm kubeconfig points at THIS EKS cluster.
#    Especially important if you just finished setting up a different
#    cluster (e.g. the GCP/GMP guide) — your kubectl context may
#    still be the previous one.
#    Substring-match because eksctl-created contexts look like
#    `<user>@<cluster>.<region>.eksctl.io` while
#    `aws eks update-kubeconfig` writes
#    `arn:aws:eks:<region>:<account>:cluster/<cluster>` — both are
#    valid, both contain the cluster name.
CURRENT_CTX=$(kubectl config current-context 2>/dev/null || echo "")
if ! printf '%s' "$CURRENT_CTX" | grep -q "${CLUSTER}"; then
  echo "kubectl context doesn't reference ${CLUSTER} — fixing"
  aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"
fi

# 1. Create the namespace (idempotent).
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# 2. Create the IRSA-bound KSA + IAM role + CFN stack in one shot.
eksctl create iamserviceaccount \
  --cluster "$CLUSTER" \
  --region "$REGION" \
  --namespace "$NAMESPACE" \
  --name "$KSA" \
  --attach-policy-arn arn:aws:iam::aws:policy/AmazonPrometheusQueryAccess \
  --override-existing-serviceaccounts \
  --approve

# 3. Verify (recommended — Step 3 will fail with cryptic
#    WebIdentityErr errors at runtime if this is misconfigured):
ROLE_ARN=$(kubectl -n "$NAMESPACE" get sa "$KSA" \
  -o jsonpath='{.metadata.annotations.eks\.amazonaws\.com/role-arn}')
echo "KSA role-arn annotation: ${ROLE_ARN:-MISSING}"
# Expected: arn:aws:iam::<account>:role/eksctl-<cluster>-addon-iamserviceaccount-...

ROLE_NAME="${ROLE_ARN##*/}"
aws iam list-attached-role-policies --role-name "$ROLE_NAME" \
  --query 'AttachedPolicies[].PolicyArn' --output text
# Expected: arn:aws:iam::aws:policy/AmazonPrometheusQueryAccess
```

> **Why `AmazonPrometheusQueryAccess`.** This AWS-managed policy
> grants exactly what Mode C needs: `aps:QueryMetrics`,
> `aps:GetSeries`, `aps:GetLabels`, `aps:GetMetricMetadata`. It does
> NOT grant ingest or admin privileges. Custom least-privilege
> policies are possible but the managed policy is the right default —
> AWS updates it as new query verbs land.

> **Behind the scenes — CloudFormation stack.** eksctl creates a
> CFN stack named
> `eksctl-<CLUSTER>-addon-iamserviceaccount-<NAMESPACE>-<KSA>` to
> own the IAM role + trust policy. If a re-run errors with
> "Stack ... already exists", either pass
> `--override-existing-serviceaccounts` (KSA side) or delete the
> stack manually:
> `aws cloudformation delete-stack --stack-name <stack-name>` then
> re-run. The Cleanup section's `eksctl delete iamserviceaccount`
> tears the stack down atomically.

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
already created. The rest is the standard Mode C install: disable
the legacy scrape sidecar (`scrape.enabled=false`), enable promread,
point at the AMP endpoint, pick `awsSigV4` as the auth mode, and
**wire the ingest-token Secret you created in Prerequisite C2** so
the agent can authenticate to the KubeBolt backend.

```bash
set -u

REGION=us-east-1
WORKSPACE_ID=<your-workspace-id>
AMP_ENDPOINT="https://aps-workspaces.${REGION}.amazonaws.com/workspaces/${WORKSPACE_ID}/"

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
  --set agent.promRead.url="${AMP_ENDPOINT}" \
  --set agent.promRead.auth.mode=awsSigV4 \
  --set agent.promRead.auth.awsRegion="${REGION}" \
  --set serviceAccount.create=false \
  --set serviceAccount.name=kubebolt-agent
```

**Flag-by-flag reference:**

| Flag | What it does | Common mistake |
|------|--------------|----------------|
| `backendUrl` | host:port the agent dials over gRPC. | Don't include a `https://` scheme — the agent expects `host:port` only. Port `443` is correct for a Caddy/Ingress fronting the KubeBolt backend. |
| `tls.enabled=true` | Encrypts the gRPC dial to the backend. Independent from `auth.mode`. | Omitting this with a TLS-fronted backend causes a confusing handshake failure that looks like the agent can't reach the host at all. |
| `cluster.name` | Operator-chosen label shown in the KubeBolt UI cluster selector. **Independent** of any cluster label your remote_write source pushes to AMP. |
| `auth.mode=ingest-token` + `auth.ingestToken.existingSecret` | Authenticates the agent → backend channel. Without this, the gRPC handshake is rejected with `unauthenticated` and the agent never registers. The Secret was created in Prerequisite C2. |
| `scrape.enabled=false` | Disables the legacy vmagent scrape sidecar (you don't want it scraping in parallel with promread). **Does NOT** disable Mode A's DaemonSet kubelet-stats collector — that still runs and complements AMP with KubeBolt-canonical metrics. |
| `agent.promRead.enabled=true` | Spawns the promread Deployment (single replica, Lease-elected). |
| `agent.promRead.url` | The base AMP endpoint **without** `/api/v1/query_range` — the agent appends the path internally. Trailing slash on the workspace URL is fine. |
| `agent.promRead.auth.mode=awsSigV4` | Tells the agent to sign each query with SigV4 using credentials from the IRSA-bound KSA. No key file needed. |
| `agent.promRead.auth.awsRegion` | The region SigV4 signs against. Must match the AMP workspace's region — see the note below. |
| `serviceAccount.create=false` + `serviceAccount.name` | Re-use the KSA `eksctl` created in Step 2 with the IAM role ARN annotation already on it. **Letting Helm create a fresh KSA** would lose the annotation and IRSA would silently not bind. |

> **Why `awsRegion` is required.** SigV4 signs against the region —
> mismatched region means signed-but-rejected requests with a 403
> response. The chart's render check (`validatePromRead` in
> `_helpers.tpl`) hard-fails if you set `auth.mode=awsSigV4` without
> a region; this is intentional, the alternative is a confusing 403
> at runtime.

> **Default matchers are AMP-compatible.** The agent's default
> matcher set uses explicit metric names (`kube_pod_status_phase`,
> `node_load1`, etc.). AMP accepts both explicit names and regex on
> `__name__`, so the default works as-is for AMP. If you override
> `agent.promRead.matchers`, you have more flexibility on AMP than
> on GMP — but **keep them surgical** to avoid query-cost blow-ups
> (see [Cost notes](#cost-notes)).

> **If your `remote_write` source pushes node-exporter metrics, set
> `agent.deferNodeStress=true`.** AMP is sink-only — it holds
> whatever your source pushes. If that source is
> `kube-prometheus-stack` (or equivalent) and ships
> `node_load1/5/15`, Mode C will pull those AND Mode A's NodeStress
> collector will emit the same metric family from `/proc/loadavg`
> directly. The UI's Load average panel then renders 6 lines
> instead of 3. Append `--set agent.deferNodeStress=true` to the
> helm install. If your source does NOT push node-exporter (e.g.
> Prometheus-Operator without the node-exporter chart), leave the
> flag at its `false` default so NodeStress remains the loadavg
> source.

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
#   INFO msg="promread enabled" url=https://aps-workspaces...  auth_mode=awsSigV4
#   INFO msg="channel registered" agent_id=<id> cluster_id=<uuid>
# RED FLAGS:
#   WARN msg="promread matcher failed" error="… AccessDenied …"
#     → IRSA isn't wired through; re-verify Step 2 post-install block.
#   WARN msg="promread matcher failed" error="… InvalidSignatureException …"
#     → awsRegion mismatch with the AMP workspace's region.
#   (no "channel registered" line)
#     → backend reject. Check `auth.mode` and the ingest-token Secret.

# 4. Sample flow (only meaningful if a remote_write source is pushing).
kubectl -n kubebolt logs -l kubebolt.dev/role=promread --tail=30 \
  | grep "buffer stats"
# Expected: collected_total growing, dropped_total=0.
# On an EMPTY AMP workspace (no remote_write source pushing yet),
# only the agent's self-metrics show up — promread queries succeed
# but return zero rows, which is normal. The integration is still
# correctly installed; samples start flowing the moment a source
# (e.g. kube-prometheus-stack with remoteWrite.sigv4) starts pushing.
```

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

> **IRSA is orthogonal to `rbac.mode`.** The IAM role from Step 2
> only governs the agent's access to AWS APIs (AMP queries). The
> `rbac.mode` flag governs the agent's access to the EKS cluster's
> own Kubernetes API. The two permission planes are independent —
> bumping `rbac.mode` does not require any AWS IAM changes.

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
set -u

CLUSTER=<your-eks-cluster-name>
REGION=us-east-1
NAMESPACE=kubebolt
KSA=kubebolt-agent

# 1. Remove the agent (helm release + all ClusterRole/CRB resources).
helm uninstall kubebolt-agent -n "$NAMESPACE"

# 2. Remove the IRSA binding (deletes IAM role + KSA + CFN stack atomically).
eksctl delete iamserviceaccount \
  --cluster "$CLUSTER" \
  --region "$REGION" \
  --namespace "$NAMESPACE" \
  --name "$KSA"

# 3. Remove the namespace (and the ingest-token Secret it holds).
kubectl delete namespace "$NAMESPACE"
```

The AMP workspace itself you typically keep — it's billed only for
samples ingested and queries processed, both of which drop to zero
when no source is pushing or reading. If you want to delete it:

```bash
aws amp delete-workspace --workspace-id <id> --region "$REGION"
```

**Optional — delete the EKS cluster.** If this cluster was created
solely to evaluate the KubeBolt + AMP integration and you don't
plan to keep it around, the fastest way to stop control-plane + node
billing is:

```bash
eksctl delete cluster --name "$CLUSTER" --region "$REGION"
```

Takes ~10-15 minutes; tears down the managed node group, the EKS
control plane, the VPC, and all CloudFormation stacks eksctl created.
**Do NOT run this against a production EKS cluster** — the rest of
this section (helm uninstall + eksctl delete iamserviceaccount +
namespace delete) is enough to fully remove KubeBolt from a cluster
you want to keep.

---

## References

- Parent mode matrix: [`prometheus.md`](./prometheus.md)
- GCP GMP recipe (same pattern, different auth): [`gcp-managed-prometheus.md`](./gcp-managed-prometheus.md)
- Azure Managed Prometheus recipe: [`azure-managed-prometheus.md`](./azure-managed-prometheus.md)
- Self-managed Prom (Basic auth / Bearer / None): [`self-managed-prom-readonly.md`](./self-managed-prom-readonly.md)
- [AWS — IAM roles for service accounts (IRSA)](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html)
- [AWS — Amazon Managed Service for Prometheus query API](https://docs.aws.amazon.com/prometheus/latest/userguide/AMP-APIReference.html)
