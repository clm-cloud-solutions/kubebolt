# Self-managed Prometheus (read-only) — read with kubebolt-agent (Mode C)

> **Available from KubeBolt 1.13.0+.** Earlier releases only supported
> Mode A (agent scrapes targets directly) and Mode B (customer's Prom
> pushes to KubeBolt). Mode C — agent **reads** from a customer-managed
> Prometheus — landed in 1.13 to cover the three managed Prom services
> where outbound `remote_write` is either impossible (AMP) or
> change-management-restricted.
>
> See the [mode matrix in `prometheus.md`](./prometheus.md#which-ingest-mode-fits-your-cluster)
> for when to pick Mode C over A or B.

This page is the **self-managed-Prom recipe** for Mode C. It assumes
you already understand the topology described in the Prometheus
parent doc; here we only cover the three static-credential auth
modes the agent supports for plain Prom instances: **None**,
**Basic auth**, and **Bearer token**.

> **When to pick this over Mode B (remote_write push).** Pick
> Mode C with one of the auth modes below when:
>
> - Your Prom is locked down by change-management — someone else
>   owns the config and you can't add a `remote_write` block.
> - You don't want a second copy of every sample in KubeBolt's VM
>   AND your Prom's storage is the canonical store.
> - Your Prom is reachable from inside the K8s cluster but NOT from
>   wherever the KubeBolt receiver lives (egress firewalling).
>
> Otherwise, Mode B (push) is usually simpler — no per-cluster
> credentials to manage.

---

## What you'll end up with

```
Self-managed Prometheus (anywhere reachable from the K8s cluster)
      ▲ query_range every 30s
      │  static credentials (none / basicAuth / bearer)
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
section](../../CLAUDE.md#packagesagent)). One leader polls Prom; if
the pod dies the next scheduled pod takes over via Kubernetes Lease.

---

## Prerequisites

| Requirement | Why |
|---|---|
| A running Prometheus reachable from the K8s cluster the agent is in | Network reachability is the only hard requirement. In-cluster Prom is the typical case (`http://prometheus-server.monitoring.svc:9090`); external Prom over public/private network also works. |
| Read access to `/api/v1/query_range` (and `/api/v1/query`) | These are the standard Prom HTTP API endpoints the agent calls. Both are exposed by stock Prometheus, `vmselect`, Thanos Querier, Cortex query-frontend, etc. — any Prom-API-compatible source works. |
| Credentials (if your Prom is auth-protected) | None / Basic / Bearer — pick one. See Step 2 below. |
| KubeBolt backend reachable from the cluster | Same as any other agent install — the agent ships samples via gRPC to your KubeBolt host. |

**KSM coverage is your responsibility.** Unlike GMP and Azure
Managed Prometheus, a self-managed Prom only has the targets you
configured it to scrape. The chart's default Mode C matchers expect
`kube_.*` (KSM) and node-exporter series to be present in the
source — if your Prom doesn't scrape these, install
[`kube-prometheus-stack`](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
or equivalent **before** turning Mode C on.

---

## Step 1 — Locate the query endpoint

For an in-cluster Prom installed via `kube-prometheus-stack`, the
service typically lives at:

```bash
# Adjust namespace + service name for your install.
PROM_URL="http://prometheus-server.monitoring.svc.cluster.local:9090"
```

Smoke-test it from inside the cluster (running from any pod with
`curl` or `wget`):

```bash
kubectl run -n default --rm -it --image=curlimages/curl \
  --restart=Never tmp-curl -- \
  curl -s "${PROM_URL}/api/v1/query?query=count(up)"
# → {"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[...,"N"]}]}}
```

For external Prom, use whatever URL is reachable from inside your
K8s cluster (could be a LoadBalancer DNS name, a VPN-side IP, etc.).
Test reachability with the same smoke command pointing at the
external URL.

If the smoke query returns `success` with a non-zero count, Prom is
reachable and answering. If you get `401 Unauthorized`, skip to the
matching auth section below.

---

## Step 2 — Pick an auth mode

The agent supports three static-credential modes for self-managed
Prom. Pick the one your Prom is configured for.

### Option A — None (no auth)

For dev clusters, in-cluster Prom installs that rely on
NetworkPolicy for isolation, or any other case where the Prom HTTP
endpoint is unauthenticated.

No setup needed beyond making sure the URL is reachable. Skip to
Step 3.

### Option B — Basic auth (username + password)

If your Prom is behind a reverse proxy (nginx, Traefik) doing HTTP
Basic auth, or your Thanos Querier has its own basic-auth layer.

For dev / quick test, pass directly via `--set` — but **for
production**, use a Kubernetes Secret + `extraEnv` so the password
doesn't end up in your Helm release history:

```bash
# Create a Secret holding the password.
kubectl create namespace kubebolt
kubectl -n kubebolt create secret generic promread-basic-auth \
  --from-literal=password='<your-password>'
```

You'll wire it via `extraEnv` in Step 3 below.

### Option C — Bearer token

If your Prom (or a fronting proxy) accepts a static bearer token.

Same secret pattern as Basic auth — **never paste tokens into
`--set` for production**:

```bash
kubectl create namespace kubebolt
kubectl -n kubebolt create secret generic promread-bearer-token \
  --from-literal=token='<your-bearer-token>'
```

You'll wire it via `extraEnv` in Step 3 below.

---

## Step 3 — Install the agent with Mode C enabled

Three variants depending on the auth mode you picked. Pick **one**.

### Variant A — None

```bash
helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set backendUrl=<your-kubebolt-host:443> \
  --set cluster.name=<your-cluster-name> \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url="${PROM_URL}" \
  --set agent.promRead.auth.mode=none
```

### Variant B — Basic auth (production-grade via Secret + extraEnv)

```bash
helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set backendUrl=<your-kubebolt-host:443> \
  --set cluster.name=<your-cluster-name> \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url="${PROM_URL}" \
  --set agent.promRead.auth.mode=basicAuth \
  --set agent.promRead.auth.basicAuthUsername=<your-username> \
  --set-json 'extraEnv=[{"name":"KUBEBOLT_AGENT_PROMREAD_BASIC_AUTH_PASSWORD","valueFrom":{"secretKeyRef":{"name":"promread-basic-auth","key":"password"}}}]'
```

> **Why `extraEnv` overrides the chart's password field.** When you
> set `agent.promRead.auth.basicAuthPassword` via `--set`, the chart
> renders it as an inline pod env var, leaving the password readable
> in `kubectl describe deployment`. The `extraEnv` block with
> `valueFrom.secretKeyRef` produces the same env var pointed at the
> Secret instead — same chart code path consumes it, but the
> password never leaves the Secret. The `extraEnv`-rendered env
> shadows the `--set`-rendered one (later wins in the env list).

### Variant C — Bearer token (production-grade via Secret + extraEnv)

```bash
helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set backendUrl=<your-kubebolt-host:443> \
  --set cluster.name=<your-cluster-name> \
  --set scrape.enabled=false \
  --set agent.promRead.enabled=true \
  --set agent.promRead.url="${PROM_URL}" \
  --set agent.promRead.auth.mode=bearer \
  --set-json 'extraEnv=[{"name":"KUBEBOLT_AGENT_PROMREAD_BEARER_TOKEN","valueFrom":{"secretKeyRef":{"name":"promread-bearer-token","key":"token"}}}]'
```

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

A self-managed Prom emits exactly the labels its scrape configs set
— no managed-service magic. With a stock `kube-prometheus-stack`
install you'll get:

| Label | How it gets there |
|---|---|
| `node` | NOT typically auto-stamped (depends on your relabel configs). Agent synthesizes via `K8sNodeIndex` IP→nodeName lookup, stripping the port from `instance`. Works for node-exporter's standard `instance=<IP>:<port>` shape. |
| `cluster_id` | The agent stamps this client-side from kube-system UID — does NOT come from Prom. |
| `instance` | Whatever your scrape configs set. Standard node-exporter is `<pod-IP>:9100`. |
| `cluster_name` (display) | The agent stamps this from `cluster.name` chart value. |

If your Prom uses non-standard `instance` labels (e.g. you have a
custom relabel that replaces them with hostnames), the agent's
`K8sNodeIndex` has a fallback that matches against known node names
directly. No operator config needed.

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
benchmarks (S1 multi-node smoke 2026-05-26) and can pressure your
Prom's query path (since it's the agent's source, not a managed
service).

---

## Cost notes

There's no per-query cost for a self-managed Prom — you're paying
for the underlying compute either way. But Mode C does add query
load:

- ~2 queries/min (one per matcher × 1 leader)
- Each `query_range` over the lookback window (default 60s)

For a typical 3-node cluster Prom this is negligible. For Prom
serving as a query backend to other tools too, monitor
`prometheus_engine_query_duration_seconds` and
`prometheus_http_requests_total{handler=~"api_v1_query.*"}` to
make sure the added load doesn't push it over.

If load is a concern, bump `agent.promRead.pollInterval` from the
default `30s` to `60s` or `120s` — UI panels show 1-min trends fine
with the slower cadence.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Pod logs show `connection refused` or `no route to host` | URL is wrong, OR a NetworkPolicy blocks the agent's egress | `kubectl run -n kubebolt --rm -it --image=curlimages/curl curl-test -- curl -v "${PROM_URL}/api/v1/query?query=up"` — repro the failure outside the agent first |
| Pod logs show `401 Unauthorized` | Credentials don't match Prom's expected auth | For Basic: try `curl -u user:pass ${PROM_URL}/api/v1/query?query=up` to confirm. For Bearer: try `curl -H "Authorization: Bearer <token>"` |
| Pod logs show `403 Forbidden` | Reverse proxy is rejecting at the IP / source level (not the credentials) | Check the proxy's allow-list — agent's pod IP needs to be allowed |
| `extraEnv` Secret not being picked up | Secret name or key mismatch | `kubectl describe deployment kubebolt-agent-promread -n kubebolt \| grep -A2 PASSWORD` — must show `Valid: true` for the secretKeyRef |
| Card still says `Not installed` after 5 min | Cluster UID not stamped on the leader gauge | Check `kubectl -n kubebolt logs -l kubebolt.dev/role=promread \| grep cluster_id` — should show a UUID. If empty, set `--set cluster.id=<kube-system UID>` explicitly |
| `kube_pod_info` and friends return empty | Source Prom doesn't scrape KSM | Install `kube-prometheus-stack` (which deploys KSM by default) before turning Mode C on |

---

## Cleanup

```bash
NAMESPACE=kubebolt

# 1. Remove the agent.
helm uninstall kubebolt-agent -n $NAMESPACE

# 2. (Optional) Remove the credential Secret if you created one.
kubectl -n $NAMESPACE delete secret promread-basic-auth --ignore-not-found
kubectl -n $NAMESPACE delete secret promread-bearer-token --ignore-not-found
```

Your source Prometheus is untouched — Mode C is read-only.

---

## References

- Parent mode matrix: [`prometheus.md`](./prometheus.md)
- AWS AMP recipe (IRSA + SigV4): [`aws-amp.md`](./aws-amp.md)
- GCP GMP recipe (Workload Identity): [`gcp-managed-prometheus.md`](./gcp-managed-prometheus.md)
- Azure Managed Prometheus recipe: [`azure-managed-prometheus.md`](./azure-managed-prometheus.md)
- [Prometheus — HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/)
- [`kube-prometheus-stack` helm chart](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
