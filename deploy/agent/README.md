# kubebolt-agent — install guide

Three permission tiers, three OSS manifests, plus a Helm chart for
operators who want finer-grained control. This README answers two
questions:

1. **Do I even need the agent?** (Half of OSS users don't.)
2. **Which permission tier should I pick?**

---

## Do I need the agent?

```
Where does your KubeBolt backend run?

  ┌─────────────────────────────────────────────────────────┐
  │ Inside the same cluster you want to monitor             │  →  NO agent
  │ (Helm install via the kubebolt chart, ServiceAccount-    │     KubeBolt uses its in-cluster
  │  authenticated against the apiserver)                    │     ServiceAccount directly.
  └─────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────┐
  │ Outside the cluster, BUT your local kubeconfig           │  →  NO agent
  │ has API access to every cluster you want to monitor      │     KubeBolt uses your
  │ (laptop, single-cluster home lab, dev loop)              │     kubeconfig directly.
  └─────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────┐
  │ Outside the cluster, AND the cluster's apiserver is NOT  │  →  YES, install the agent
  │ reachable from the backend (private network, no public   │     in the target cluster.
  │ load balancer, on-prem behind NAT, SaaS topology…)       │     Agent dials OUT to the
  │                                                          │     backend's gRPC port.
  └─────────────────────────────────────────────────────────┘
```

The agent is also useful as a SUPPLEMENT in cases 1 and 2 if you
want **kubelet metrics** (historical CPU/memory/network) and
**Cilium Hubble flows** that the apiserver alone can't surface —
but install it in `metrics` mode there, since the proxy is
unnecessary when the backend already has direct API access.

---

## Picking a permission tier

Once you've decided you need the agent, the next question is what
the agent's ServiceAccount should be allowed to do in the cluster.
Three tiers:

| Tier | Manifest | Backend can read inventory? | Backend can mutate? | Auth required |
|---|---|---|---|---|
| **metrics** | `kubebolt-agent-metrics.yaml` | ❌ (only metrics + flows ship) | ❌ | optional |
| **reader** | `kubebolt-agent-reader.yaml` | ✅ everything | ❌ (returns 403) | recommended |
| **operator** | `kubebolt-agent-operator.yaml` | ✅ everything | ✅ exec, scale, restart, delete, YAML edit | **REQUIRED** |

### When to pick which

- **Metrics-only**. You want historical metrics + Hubble flows in
  the dashboard but **don't want any apiserver call to traverse
  the agent's tunnel**. Privacy-conscious orgs, regulated
  environments, agents-on-untrusted-clusters scenarios. Pairs well
  with Case 1 or Case 2 above (KubeBolt has its own API access).

- **Reader**. The typical install for the SaaS-style topology:
  backend reaches the cluster via the agent, dashboard shows full
  inventory + YAML + describe + logs, but mutations come back 403
  (no exec, no scale, no delete). This is the right default if
  you're not sure.

- **Operator**. You want full UI parity through the agent —
  click-to-restart, scale sliders, YAML edit, exec into pods. This
  grants the agent ServiceAccount **cluster-admin equivalent**
  power, so the backend's auth is the only thing keeping a network
  attacker from pivoting. Auth is mandatory; the manifest's header
  walks you through creating the Secret first.

---

## Installing — three paths

### Path 1: raw manifest (simplest)

```bash
# Pick ONE of these based on the tier you decided above:
kubectl apply -f https://raw.githubusercontent.com/clm-cloud-solutions/kubebolt/main/deploy/agent/kubebolt-agent-metrics.yaml
kubectl apply -f https://raw.githubusercontent.com/clm-cloud-solutions/kubebolt/main/deploy/agent/kubebolt-agent-reader.yaml
kubectl apply -f https://raw.githubusercontent.com/clm-cloud-solutions/kubebolt/main/deploy/agent/kubebolt-agent-operator.yaml
```

BEFORE applying, edit the file to set:

- `KUBEBOLT_BACKEND_URL` → your backend's gRPC host:port.
- (Operator only) Create the `kubebolt-agent-token` Secret first —
  see the manifest's `PREREQUISITE` block for the exact
  `kubectl create secret` recipe.

### Path 2: Helm (more flexibility)

```bash
helm install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system --create-namespace \
  --set backendUrl=YOUR_BACKEND:9090 \
  --set rbac.mode=reader   # or metrics, or operator
```

For operator mode + auth in one shot:

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

See [`../helm/kubebolt-agent/README.md`](../helm/kubebolt-agent/README.md)
for every `--set` knob.

### Path 3: KubeBolt UI wizard

Only works when KubeBolt's backend already has kubeconfig access to
the target cluster — the wizard uses YOUR active KubeBolt cluster
context to apply manifests for you. Useful for self-hosted
single-tenant; **not** useful for the SaaS cross-cluster case
(that's Path 1 or 2).

Administration → Integrations → KubeBolt Agent → Install. The
wizard surfaces the same 3-mode picker as a radio control + a
"Generate token + create Secret" button so the auth flow is one
click.

---

## Customizing flags

All three paths expose the same flags; the names just differ
slightly between Helm values and raw env vars:

| Flag | Helm | Raw env / manifest |
|---|---|---|
| Permission tier | `--set rbac.mode=reader` | (file you picked) |
| Backend URL | `--set backendUrl=…` | `KUBEBOLT_BACKEND_URL` env |
| Auth mode | `--set auth.mode=ingest-token` | `KUBEBOLT_AGENT_AUTH_MODE` env |
| Token Secret | `--set auth.ingestToken.existingSecret=…` | `volumes.secret.secretName` |
| Hubble on/off | `--set hubble.enabled=false` | `KUBEBOLT_HUBBLE_ENABLED` env |
| Cluster display name | `--set cluster.name=…` | `KUBEBOLT_AGENT_CLUSTER_NAME` env |
| Image tag | `--set image.tag=v0.2.0` | `containers[0].image` |
| Resources | `--set resources.requests.cpu=…` | `resources:` block |

For raw manifest installs, **the manifest is the source of truth**
— edit the YAML before apply and re-apply for changes to take
effect.

---

## Upgrading from v0.1.0

If you applied `kubebolt-agent-dev*.yaml` from before
`agent-v0.2.0`, the cleanest upgrade is to delete those resources
and apply one of the new tier manifests:

```bash
# Old name was kubebolt-agent-reader (narrow rules) + kubebolt-agent
# Binding. v0.2.0 renames the narrow CR to kubebolt-agent-metrics
# and reuses kubebolt-agent-reader for cluster-wide read.
kubectl delete clusterrolebinding kubebolt-agent  # legacy Binding
# (the chart / manifests handle the CR rename on apply)

kubectl apply -f kubebolt-agent-reader.yaml
```

The KubeBolt UI's Configure dialog also handles the rename
automatically when it sees an old install — you can switch tiers
through Configure without touching kubectl.

---

## Uninstall

```bash
# Helm
helm uninstall kubebolt-agent -n kubebolt-system

# Raw manifest (works regardless of which tier you applied)
kubectl delete -f kubebolt-agent-reader.yaml  # or whichever you used

# Manual cleanup if neither file is handy
kubectl delete daemonset/kubebolt-agent -n kubebolt-system
kubectl delete sa/kubebolt-agent -n kubebolt-system
kubectl delete clusterrolebinding/kubebolt-agent-metrics
kubectl delete clusterrolebinding/kubebolt-agent-reader 2>/dev/null || true
kubectl delete clusterrolebinding/kubebolt-agent-operator 2>/dev/null || true
kubectl delete clusterrole/kubebolt-agent-metrics
kubectl delete clusterrole/kubebolt-agent-reader 2>/dev/null || true
kubectl delete clusterrole/kubebolt-agent-operator 2>/dev/null || true
```

The namespace `kubebolt-system` is preserved — it may hold Secrets
or ConfigMaps you'd rather keep.
