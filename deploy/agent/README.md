# kubebolt-agent — install guide

The agent connects a Kubernetes cluster to KubeBolt. It dials **out** to the
KubeBolt backend's gRPC port, ships kubelet (and optionally Hubble) metrics,
and — depending on its permission tier — lets the backend read or operate the
cluster through that tunnel. This guide answers two questions:

1. **Do I even need the agent?**
2. **Which permission tier should I pick?**

Then it shows how to install it. **Helm is the recommended path**; the full
value reference lives in the [chart README](../helm/kubebolt-agent/README.md).
User documentation: [kubebolt.io/docs/agent](https://kubebolt.io/docs/agent).

> [!WARNING]
> **The raw manifests in this directory are legacy.** `kubebolt-agent-*.yaml`
> pin agent image `v0.2.2`, which emits the pre-1.0 (legacy) metric schema.
> With KubeBolt ≥ 1.10 (including 2.x) the agent registers but the dashboards
> for that cluster stay **empty**. Use these files only with KubeBolt ≤ 1.9
> until they are regenerated. For a current plain-YAML install, render the
> manifests from the Helm chart — see
> [Path 2](#path-2-plain-manifests-rendered-from-the-chart). Details:
> [compatibility matrix](../../docs/COMPATIBILITY.md).

---

## Do I need the agent?

```
Where does your KubeBolt backend run?

  ┌─────────────────────────────────────────────────────────┐
  │ Inside the same cluster you want to monitor             │  →  NO agent required
  │ (Helm install via the kubebolt chart, ServiceAccount-    │     KubeBolt uses its in-cluster
  │  authenticated against the apiserver)                    │     ServiceAccount directly.
  └─────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────┐
  │ Outside the cluster, BUT your local kubeconfig           │  →  NO agent required
  │ has API access to every cluster you want to monitor      │     KubeBolt uses your
  │ (laptop, single-cluster home lab, dev loop)              │     kubeconfig directly.
  └─────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────┐
  │ Outside the cluster, AND the cluster's apiserver is NOT  │  →  YES, install the agent
  │ reachable from the backend (private network, no public   │     in the target cluster.
  │ load balancer, on-prem behind NAT, or a remote backend   │     Agent dials OUT to the
  │ such as KubeBolt Cloud)                                  │     backend's gRPC port.
  └─────────────────────────────────────────────────────────┘
```

The agent is also useful as a SUPPLEMENT in the first two cases if you want
**historical metrics** (kubelet CPU / memory / network / filesystem), **Cilium
Hubble flows**, or **cost data** from OpenCost — install it in `metrics` mode
there, since the API proxy is unnecessary when the backend already has direct
API access.

---

## Picking a permission tier

Once you've decided you need the agent, the next question is what the agent's
ServiceAccount should be allowed to do in the cluster. Three tiers, selected
with the chart's `rbac.mode`:

| Tier (`rbac.mode`) | Backend can read inventory? | Backend can mutate? | Auth |
|---|---|---|---|
| **metrics** | ❌ (only metrics + flows ship) | ❌ | optional |
| **reader** (default) | ✅ everything | ❌ (returns 403) | recommended |
| **operator** | ✅ everything | ✅ exec, scale, restart, delete, YAML edit | **strongly recommended** (not enforced by the chart) |

### When to pick which

- **Metrics-only**. You want historical metrics + Hubble flows in the
  dashboard but **don't want any apiserver call to traverse the agent's
  tunnel**. Privacy-conscious orgs, regulated environments,
  agents-on-untrusted-clusters scenarios. Pairs well with the first two cases
  above (KubeBolt has its own API access).

- **Reader**. The typical install for a remote backend: the backend reaches
  the cluster via the agent, the dashboard shows full inventory + YAML +
  describe + logs, but mutations come back 403 (no exec, no scale, no delete).
  This is the right default if you're not sure.

- **Operator**. You want full UI parity through the agent — click-to-restart,
  scale, YAML edit, exec into pods. This grants the agent ServiceAccount
  **cluster-admin equivalent** power, so authentication on the agent channel is
  the only thing keeping a network attacker from pivoting. Always pair it with
  `auth.mode=ingest-token` (or `tokenreview`).

---

## Installing

### Path 1: Helm (recommended)

```bash
helm install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system --create-namespace \
  --set backendUrl=YOUR_BACKEND:9090 \
  --set rbac.mode=reader   # or metrics, or operator
```

`backendUrl` examples:

| Backend | `backendUrl` |
|---|---|
| `kubebolt` chart in the same cluster (release `kubebolt`, namespace `kubebolt`) | `kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090` |
| Reachable host / LoadBalancer | `<host>:9090` |
| Remote backend over TLS, for example [KubeBolt Cloud](https://kubebolt.io) | `agent.kubebolt.io:443` + `tls.enabled=true` + `auth.mode=ingest-token` |

With authentication (strongly recommended for `operator`; required by a remote backend such as KubeBolt Cloud):

```bash
kubectl create namespace kubebolt-system
kubectl create secret generic kubebolt-agent-token \
  -n kubebolt-system \
  --from-literal=token=<paste-token>

helm install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system \
  --set backendUrl=YOUR_BACKEND:9090 \
  --set rbac.mode=operator \
  --set auth.mode=ingest-token \
  --set auth.ingestToken.existingSecret=kubebolt-agent-token
```

Generate the token in KubeBolt under **Administration → Agents & Ingest →
Agent Tokens**, or let the **Add cluster** wizard on the Clusters page issue it
and print a ready-to-run `helm install` command. For a remote backend such as
KubeBolt Cloud, add `--set tls.enabled=true` and use its `host:443` address.

See the [chart README](../helm/kubebolt-agent/README.md) for every `--set`
knob.

### Path 2: plain manifests rendered from the chart

For air-gapped clusters or GitOps flows that want plain YAML, render the
manifests from the chart instead of using the legacy files in this directory:

```bash
helm template kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --version 1.4.2 \
  --namespace kubebolt-system \
  --set backendUrl=YOUR_BACKEND:9090 \
  --set rbac.mode=reader \
  > kubebolt-agent-reader.yaml

kubectl create namespace kubebolt-system
kubectl apply -f kubebolt-agent-reader.yaml
```

Swap `rbac.mode=reader` for `metrics` or `operator`, and add any other value
(auth, TLS, Hubble) with more `--set` flags. The rendered file does not include
the Namespace, so create it first. Resources rendered this way carry
`app.kubernetes.io/managed-by: Helm`, although Helm doesn't track them —
manage them with `kubectl`.

### Path 3: KubeBolt UI wizard

Only works when KubeBolt's backend already has kubeconfig access to the target
cluster — the wizard uses YOUR active KubeBolt cluster context to apply
manifests for you. Useful for self-hosted single-cluster setups; **not** for a
remote backend whose API server access goes through the agent (use Path 1 or
2 there).

**Administration → Agents & Ingest → Integrations → KubeBolt Agent → Install.**
The wizard surfaces the same 3-mode picker as a radio control + a "Generate
token + create Secret" button so the auth flow is one click.

---

## Beyond kubelet metrics

The same chart covers the other data paths — see the
[chart README](../helm/kubebolt-agent/README.md) for the values:

- **Mode C — read an existing Prometheus** (`agent.promRead.enabled=true`). A
  single `promread` Deployment polls your Prometheus (self-managed, AWS AMP,
  Azure Monitor, Google Managed Prometheus) with `query_range` and forwards the
  kube-state-metrics, load / PSI, disk and network-error series KubeBolt
  can't get from the kubelet. See
  [`docs/integrations/prometheus.md`](../../docs/integrations/prometheus.md)
  and the per-cloud guides next to it.
- **vmagent scrape sidecar** (`scrape.enabled=true`). Scrapes node-exporter,
  kube-state-metrics and annotated pods on each node and `remote_write`s them
  to the backend (which needs `metrics.remoteWrite.enabled=true`). Mutually
  exclusive with Mode C. See
  [`docs/agent-scraping.md`](../../docs/agent-scraping.md).
- **OpenCost** (`opencost.enabled=true`, or your own OpenCost under
  `collectors.exporters`). The agent scrapes OpenCost's `/metrics` to power
  KubeBolt's cost views. See
  [`docs/integrations/opencost.md`](../../docs/integrations/opencost.md).

---

## Customizing flags

Helm values and the underlying agent environment variables (useful when you
read or patch rendered manifests):

| Flag | Helm | Env var / manifest field |
|---|---|---|
| Permission tier | `--set rbac.mode=reader` | ClusterRole tier + `KUBEBOLT_AGENT_PROXY_ENABLED` |
| Backend URL | `--set backendUrl=…` | `KUBEBOLT_BACKEND_URL` |
| Auth mode | `--set auth.mode=ingest-token` | `KUBEBOLT_AGENT_AUTH_MODE` |
| Token Secret | `--set auth.ingestToken.existingSecret=…` | `volumes[].secret.secretName` |
| TLS to backend | `--set tls.enabled=true` | `KUBEBOLT_AGENT_TLS_ENABLED` |
| Hubble on/off (default off) | `--set hubble.enabled=true` | `KUBEBOLT_HUBBLE_ENABLED` |
| Cluster display name | `--set cluster.name=…` | `KUBEBOLT_AGENT_CLUSTER_NAME` |
| Image tag | `--set image.tag=1.4.2` (defaults to the chart's appVersion) | `containers[0].image` |
| Resources | `--set resources.requests.cpu=…` | `resources:` block |

---

## Upgrading from the legacy manifests

If you applied the 0.x manifests from this directory, move to the chart:
delete the old resources (see Uninstall below — the ClusterRole names are the
same the chart uses) and install with Helm (Path 1) or apply manifests
rendered from the chart (Path 2). Remember that agent ≥ 1.0 needs
KubeBolt ≥ 1.10 — upgrade the backend first
([compatibility matrix](../../docs/COMPATIBILITY.md)).

`kubebolt-agent-rbac-operator.yaml` is a legacy overlay from the same
generation; with the chart, `rbac.mode=operator` replaces it.

---

## Uninstall

```bash
# Helm
helm uninstall kubebolt-agent -n kubebolt-system

# Plain manifests (rendered from the chart, or the legacy files)
kubectl delete -f kubebolt-agent-reader.yaml  # or whichever you applied

# Manual cleanup if the file isn't handy
kubectl delete daemonset/kubebolt-agent -n kubebolt-system
kubectl delete deployment/kubebolt-agent-promread -n kubebolt-system 2>/dev/null || true
kubectl delete sa/kubebolt-agent -n kubebolt-system
kubectl delete role/kubebolt-agent-leader rolebinding/kubebolt-agent-leader -n kubebolt-system 2>/dev/null || true
kubectl delete clusterrolebinding/kubebolt-agent-metrics
kubectl delete clusterrolebinding/kubebolt-agent-reader 2>/dev/null || true
kubectl delete clusterrolebinding/kubebolt-agent-operator 2>/dev/null || true
kubectl delete clusterrole/kubebolt-agent-metrics
kubectl delete clusterrole/kubebolt-agent-reader 2>/dev/null || true
kubectl delete clusterrole/kubebolt-agent-operator 2>/dev/null || true
```

The namespace `kubebolt-system` is preserved — it may hold Secrets or
ConfigMaps you'd rather keep.
