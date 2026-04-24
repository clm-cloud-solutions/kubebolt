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

## Install

```bash
helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt-system --create-namespace \
  --set backendUrl=kubebolt.kubebolt.svc.cluster.local:9090
```

Replace `backendUrl` with wherever your KubeBolt backend's gRPC port
(`:9090`) is reachable from inside the cluster. See the "Connecting
to the backend" section below for concrete examples.

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

## Key values

| Value | Default | Purpose |
|-------|---------|---------|
| `backendUrl` | _(required)_ | Host:port of the KubeBolt API gRPC. |
| `cluster.name` | `""` | Human-readable cluster label. Empty is fine. |
| `cluster.id` | `""` | Override the auto-discovered cluster_id. Leave empty. |
| `hubble.enabled` | `true` | Toggle the flow collector. No-op when Cilium is absent. |
| `hubble.relay.address` | `""` | Override the relay target. Default: `hubble-relay.kube-system.svc.cluster.local:80`. |
| `hubble.relay.tls.existingSecret` | `""` | Secret with `ca.crt` (TLS) + optional `tls.crt`/`tls.key` (mTLS). |
| `image.tag` | `""` | Falls back to `Chart.appVersion`. Pin for prod. |
| `rbac.create` | `true` | Creates ClusterRole/Role + bindings. |
| `logLevel` | `info` | `debug` / `info` / `warn` / `error`. |

Full reference in [`values.yaml`](./values.yaml).

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
