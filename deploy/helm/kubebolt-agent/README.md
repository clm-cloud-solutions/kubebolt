# kubebolt-agent

Node agent for [KubeBolt](https://github.com/clm-cloud-solutions/kubebolt).
Ships as a DaemonSet and collects three streams of data from each
Kubernetes node, then forwards them to the KubeBolt backend:

- **kubelet `/stats/summary`** — per-pod / per-container CPU, memory,
  and network counters sampled every 15 s.
- **cAdvisor fallback** — covers kubelets that don't populate the
  pod-level network block (e.g. docker-desktop).
- **Cilium Hubble flow events** — L4 counters, L7 HTTP status +
  latency, and DNS resolutions. Collected by a single leader-elected
  pod so the relay isn't N-times-counted.

The agent is optional. Without it KubeBolt still works — you lose
historical metrics, network / disk observability, and live traffic
flows, but everything else (inventory, insights, YAML edit, exec,
port-forward, logs) is unchanged.

## Installation methods

There are three ways to install the agent. All produce the same
DaemonSet; they differ in who owns it and which tool can later
modify or remove it. The ownership signal is the
`app.kubernetes.io/managed-by` label on the DaemonSet, which
KubeBolt reads on every poll.

| Method | `managed-by` label | Install command | When to use | Who can modify |
|---|---|---|---|---|
| **This chart** | `Helm` | `helm install kubebolt-agent oci://...` | Production. Full value surface including `affinity`, custom `tolerations`, `podAnnotations`, etc. | Helm (`helm upgrade`), KubeBolt UI with force |
| **KubeBolt UI** | `kubebolt` | Administration → Integrations → Install | Quickest path when you already have KubeBolt running. Opinionated value set covering 90% of installs. | KubeBolt UI (Configure / Uninstall) |
| **Raw manifest** | _(unset)_ | `kubectl apply -f deploy/agent/kubebolt-agent-dev.yaml` | Dev loops, air-gapped clusters, GitOps flows that manage their own manifests | The tool that applied it; KubeBolt UI with force |

**Mixing paths:** KubeBolt's UI refuses to modify DaemonSets without
the `managed-by=kubebolt` label by default. Uninstall has a
Force option that removes the workload by name regardless — useful
when migrating between methods. See the Uninstall section below.

## Install

```bash
helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system --create-namespace \
  --set backendUrl=kubebolt.kubebolt.svc.cluster.local:9090
```

Replace `backendUrl` with wherever your KubeBolt backend's gRPC port
(`:9090`) is reachable from inside the cluster. See the "Connecting
to the backend" section below for concrete examples.

## Permission tier — `rbac.mode`

The chart applies a 3-tier permission model. Pick the one that
matches what you want the dashboard to be able to do in this
cluster:

| `rbac.mode` | What the agent SA can do | Proxy | Auth | When to pick |
|---|---|---|---|---|
| `metrics` | Kubelet stats + pods list/watch + namespaces get | OFF | optional | Privacy-conscious. Only kubelet metrics + Hubble flows leave the cluster. The dashboard shows historical CPU / memory / flows but no inventory through this agent. |
| `reader` (default) | Cluster-wide `get`/`list`/`watch` on `*/*` | ON (mandatory) | optional but recommended | "I want to see everything but not change it." Backend renders inventory + YAML + describe + logs through the agent's tunnel. Write attempts come back 403. |
| `operator` | Wildcard `get/list/watch/create/update/patch/delete` on `*/*` | ON (mandatory) | **REQUIRED** | Full UI parity through the agent — exec, scale, restart, delete, YAML edit. Effectively cluster-admin scoped to the SA; auth on the gRPC channel is the only thing keeping random network probers from pivoting to admin. |

**Pre-0.2.0 readers**: this replaces the binary
`proxy.enabled` + (apply-out-of-band) operator-tier ClusterRole
overlay. Wire-compat is preserved on the backend's install
endpoint, but the chart only speaks `rbac.mode` — pick one of the
three values.

## Auth (`auth.mode`)

Independent toggle from `rbac.mode`. Three values:

- `disabled` (default for self-hosted lab): no credentials. The
  agent dials in plaintext + no token. Only valid against a backend
  that runs in `KUBEBOLT_AGENT_AUTH_MODE=disabled`.
- `ingest-token`: long-lived bearer token. The operator generates
  it via the KubeBolt backend's admin UI ("Agent Tokens") or
  REST (`POST /admin/tenants/{id}/tokens`), then either:
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
  `auth.tokenReview.audience=kubebolt-backend` (matches the
  backend's expected audience).

## Upgrade

```bash
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system \
  --reuse-values
```

## Uninstall

```bash
helm uninstall kubebolt-agent -n kubebolt-system
```

This removes every resource the chart created AND the Helm release
metadata, so `helm list -n kubebolt-system` no longer shows it.
The release namespace itself is preserved.

If the agent was installed through the KubeBolt UI instead of this
chart, `helm uninstall` won't find anything — remove it from the
UI (Administration → Integrations → Agent → Uninstall) or delete
the resources by name. The other direction works too: KubeBolt's
UI can force-uninstall a chart-installed agent, though the Helm
release metadata will linger until you also run `helm uninstall`
with `--keep-history=false` or delete the release Secret directly.

## Key values

The values below are the ones most commonly set in production.
Full reference with every knob the chart exposes is in
[`values.yaml`](./values.yaml).

### Required

| Value | Default | Purpose |
|-------|---------|---------|
| `backendUrl` | _(required)_ | Host:port of the KubeBolt API gRPC. See "Connecting to the backend" below. |

### Cluster identity

| Value | Default | Purpose |
|-------|---------|---------|
| `cluster.name` | `""` | Human-readable cluster label emitted alongside `cluster_id`. Empty is fine — only surfaces when querying VictoriaMetrics directly. |
| `cluster.id` | `""` | Override the auto-discovered `cluster_id` (kube-system namespace UID). Leave empty unless migrating legacy data. |

### Image

| Value | Default | Purpose |
|-------|---------|---------|
| `image.repository` | `ghcr.io/clm-cloud-solutions/kubebolt/agent` | Registry path. Override for mirrors or private registries. |
| `image.tag` | `""` | Falls back to `Chart.appVersion`. Pin explicitly in prod. |
| `image.pullPolicy` | `IfNotPresent` | `Always` / `IfNotPresent` / `Never`. Use `Never` for kind/dev where the image is pre-loaded with `kind load`. |
| `imagePullSecrets` | `[]` | For private registries. |

### Hubble flow collector

| Value | Default | Purpose |
|-------|---------|---------|
| `hubble.enabled` | `true` | Toggle the flow collector. Silent no-op when Cilium isn't installed — safe to leave on. |
| `hubble.relay.address` | `""` | Override the relay target. Default: `hubble-relay.kube-system.svc.cluster.local:80`. |
| `hubble.relay.tls.existingSecret` | `""` | Pre-existing Secret in the release namespace with `ca.crt` (TLS) + optional `tls.crt`/`tls.key` (mTLS). |
| `hubble.relay.tls.serverName` | `""` | SNI / verification hostname. Override when the relay's cert uses a CN/SAN distinct from the dial target. |

### Scheduling

| Value | Default | Purpose |
|-------|---------|---------|
| `tolerations` | `[{operator: Exists}]` | Tolerates every taint so the agent lands on control-plane nodes. Trim if you want to exclude some. |
| `nodeSelector` | `{}` | Pin the agent to specific nodes, e.g. `{kubernetes.io/os: linux}`. |
| `affinity` | `{}` | Full affinity object. Most installs don't need this. |
| `priorityClassName` | `""` | Set to `system-cluster-critical` (or your own PriorityClass) in prod to avoid preemption. |

### Resources

| Value | Default | Purpose |
|-------|---------|---------|
| `resources.requests.cpu` | `10m` | Agent is a light Go binary; defaults are sized for small clusters. |
| `resources.requests.memory` | `30Mi` | Same — scale up for clusters with thousands of pods. |
| `resources.limits.cpu` | `100m` | |
| `resources.limits.memory` | `80Mi` | |

### RBAC + ServiceAccount

| Value | Default | Purpose |
|-------|---------|---------|
| `rbac.create` | `true` | Creates ClusterRole/Role + bindings. Set `false` when RBAC is provisioned externally (e.g. Rancher, platform operators). |
| `serviceAccount.create` | `true` | |
| `serviceAccount.name` | `""` | Empty = derive from release name. Set explicitly when `serviceAccount.create=false`. |
| `serviceAccount.annotations` | `{}` | Useful for IRSA (EKS) / Workload Identity (GKE). |

### Extras

| Value | Default | Purpose |
|-------|---------|---------|
| `logLevel` | `info` | `debug` / `info` / `warn` / `error`. |
| `extraEnv` | `[]` | Inject arbitrary env vars into the agent container — escape hatch for features without first-class values. |
| `podAnnotations` | `{}` | Useful for external scrapers or policy engines. |
| `podLabels` | `{}` | |

## Connecting to the backend

| Topology | `backendUrl` |
|----------|---------------|
| Backend in Docker Compose on your laptop, agent in Docker Desktop K8s | `host.docker.internal:9090` |
| Backend in-cluster via the main chart (release `kubebolt` in namespace `kubebolt`) | `kubebolt.kubebolt.svc.cluster.local:9090` |
| Backend behind an internal LoadBalancer | that LB's IP:9090 |
| Backend on a VM reachable from the cluster | that host:9090 |

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
