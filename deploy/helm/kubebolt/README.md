# KubeBolt Helm Chart

Instant Kubernetes Monitoring & Management — full cluster visibility in under 2 minutes.

## Install

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
```

Then access via port-forward:

```bash
kubectl port-forward svc/kubebolt 3000:80
```

Open http://localhost:3000

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `api.image.repository` | API image | `ghcr.io/clm-cloud-solutions/kubebolt/api` |
| `api.image.tag` | API image tag | Chart appVersion |
| `api.port` | API port | `8080` |
| `api.resources` | API resource requests/limits | 50m/64Mi - 500m/256Mi |
| `web.image.repository` | Web image | `ghcr.io/clm-cloud-solutions/kubebolt/web` |
| `web.image.tag` | Web image tag | Chart appVersion |
| `web.port` | Web port | `3000` |
| `web.resources` | Web resource requests/limits | 10m/16Mi - 100m/64Mi |
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `80` |
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class | `""` |
| `ingress.hosts` | Ingress hosts | `[{host: kubebolt.local}]` |
| `ingress.tls` | Ingress TLS config | `[]` |
| `serviceAccount.create` | Create ServiceAccount | `true` |
| `rbac.create` | Create ClusterRole/Binding | `true` |
| `replicaCount` | Number of replicas | `1` |
| `metrics.storage.embedded.enabled` | Deploy bundled VictoriaMetrics | `true` |
| `metrics.storage.embedded.retention` | TSDB retention window | `30d` |
| `metrics.storage.embedded.persistence.size` | PVC size for VM data | `10Gi` |
| `metrics.storage.embedded.persistence.storageClass` | Storage class (cluster default if empty) | `""` |
| `metrics.storage.embedded.resources` | VM resource requests/limits | 100m/256Mi - 1000m/2Gi |
| `metrics.storage.externalUrl` | URL of an external VictoriaMetrics (used when embedded is disabled) | `""` |
| `agentIngest.enabled` | Bind the gRPC channel for `kubebolt-agent` connections | `true` |
| `agentIngest.port` | gRPC port the agent dials | `9090` |
| `agentIngest.authMode` | `disabled` / `permissive` / `enforced`. Controls whether unauthenticated agents are rejected at the welcome handshake. `permissive` accepts but logs a warning — useful while migrating an existing fleet onto auth. | `disabled` |
| `agentIngest.tokenAudience` | Audience the projected SA token must carry when `auth.mode=tokenreview` on the agent side. Empty = backend default (`kubebolt-backend`) | `""` |
| `auth.enabled` | Enable built-in authentication | `true` |
| `auth.adminPassword` | Initial admin password (generated if empty) | `""` |
| `auth.jwtSecret` | JWT signing secret (generated if empty, won't survive restarts) | `""` |
| `auth.existingSecret` | Existing Secret with `admin-password` and/or `jwt-secret` keys | `""` |
| `auth.persistence.enabled` | Enable persistent storage for user database | `true` |
| `auth.persistence.size` | PVC size | `1Gi` |
| `auth.persistence.storageClass` | Storage class (cluster default if empty) | `""` |
| `copilot.enabled` | Enable AI Copilot chat panel | `false` |
| `copilot.provider` | LLM provider: `anthropic`, `openai`, `custom` | `anthropic` |
| `copilot.model` | Model name (uses provider default if empty) | `""` |
| `copilot.apiKey` | LLM API key (use `existingSecret` for production) | `""` |
| `copilot.existingSecret` | Existing Secret with `api-key` field | `""` |
| `copilot.fallback.enabled` | Enable fallback model on primary failure | `false` |
| `copilot.fallback.provider` / `model` / `apiKey` / `existingSecret` | Fallback config | — |

## Ingress

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=kubebolt.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

## Authentication

KubeBolt includes built-in authentication with three roles: **Admin** (full access + user management), **Editor** (edit YAML, scale, restart), and **Viewer** (read-only). Enabled by default.

### Initial admin password

On first boot a `admin` user is seeded. If neither `auth.adminPassword` nor `auth.existingSecret` is set, the API:

1. Generates a random password,
2. Prints it once to the API log (banner only fires when seeding actually happens — restarts on an existing DB stay quiet),
3. Persists it to a Secret named `kubebolt-admin-password` in this release's namespace.

Retrieve it any time with:

```bash
kubectl -n NS get secret kubebolt-admin-password -o jsonpath='{.data.password}' | base64 -d ; echo
```

For production, supply your own Secret instead:

```bash
kubectl create secret generic kubebolt-auth \
  --from-literal=admin-password=YourSecurePassword \
  --from-literal=jwt-secret=$(openssl rand -hex 32)

helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set auth.existingSecret=kubebolt-auth
```

To disable auth (open access, no login):

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set auth.enabled=false
```

### Forgot-password recovery

Two paths, pick whichever fits your workflow.

**Path A — `helm upgrade` (recommended for helm-managed installs).** Sets `KUBEBOLT_RESET_ADMIN_PASSWORD` on the deployment; the API resets the admin password on next start, then continues normal boot. With `strategy: Recreate` the BoltDB lock is released cleanly during rollover.

```bash
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --reuse-values --set auth.resetAdminPassword=NEWPASS

# log in with NEWPASS, change to your real password from the Account menu, then:
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --reuse-values --set auth.resetAdminPassword=
```

**Path B — one-shot `Job` (when helm isn't available, or for a runbook).** Scale the API to zero (BoltDB is single-writer), run a Job with the same image and PVC, scale back up:

```bash
NS=kubebolt   # your release namespace
IMAGE=$(kubectl -n $NS get deploy/kubebolt-api -o jsonpath='{.spec.template.spec.containers[0].image}')

kubectl -n $NS scale deploy/kubebolt-api --replicas=0
kubectl -n $NS apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata: { name: kubebolt-pw-reset }
spec:
  ttlSecondsAfterFinished: 60
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: reset
        image: $IMAGE
        command: ["kubebolt-api", "--reset-admin-password=NEWPASS"]
        env: [{ name: KUBEBOLT_DATA_DIR, value: /data }]
        volumeMounts: [{ name: data, mountPath: /data }]
      volumes:
      - name: data
        persistentVolumeClaim: { claimName: kubebolt-data }
EOF
kubectl -n $NS wait --for=condition=Complete job/kubebolt-pw-reset --timeout=60s
kubectl -n $NS scale deploy/kubebolt-api --replicas=1
```

Min password length: 8 chars. Both paths log the reset to the API log so it's auditable.

## AI Copilot

KubeBolt includes an optional in-app AI assistant that can answer questions about your
cluster. It uses **your own** LLM provider API key — KubeBolt has no managed AI service.

Quick enable:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set copilot.enabled=true \
  --set copilot.provider=anthropic \
  --set copilot.apiKey=$ANTHROPIC_API_KEY
```

For production, use an existing Kubernetes Secret instead of inline values:

```bash
kubectl create secret generic kubebolt-copilot-key --from-literal=api-key=$ANTHROPIC_API_KEY
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set copilot.enabled=true \
  --set copilot.existingSecret=kubebolt-copilot-key
```

See the [full copilot guide](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/copilot.md)
for fallback configuration, recipes, and security notes. See the
[providers reference](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/copilot-providers.md)
for the full list of supported LLM providers (Anthropic, OpenAI, Azure, Groq, OpenRouter,
DeepSeek, Mistral, self-hosted Ollama/vLLM, and more).

## Cloud-specific Guides

- [Amazon EKS](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/eks.md)
- [Google GKE](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/gke.md)
- [Azure AKS](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/aks.md)
- [AI Copilot configuration](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/copilot.md)
- [AI Copilot providers reference](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/copilot-providers.md)

## Metrics Storage

Time-series data (CPU, memory, network samples shipped by `kubebolt-agent`,
plus Hubble flow events) lives in a Prometheus-compatible TSDB.

**Default — embedded VictoriaMetrics.** The chart deploys a single-node
VictoriaMetrics StatefulSet alongside the API, with a 10 GiB PVC and 30-day
retention. Nothing else to configure for it to work.

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
# bundles VictoriaMetrics automatically
```

**Bring your own.** If you already run VictoriaMetrics, vmselect, or any
endpoint compatible with the VM ingestion + query API, point KubeBolt at
it instead:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set metrics.storage.embedded.enabled=false \
  --set metrics.storage.externalUrl=http://vmselect.observability.svc.cluster.local:8481
```

Plain Prometheus remote-write isn't enough — KubeBolt also queries the
endpoint back, so it must accept VM-style reads. vmagent + vmselect
behind a single URL is the typical setup.

**Sizing.** Rule of thumb: ~1 GiB of PVC per 100 pods at 30-day retention
with default scrape cadence. Bump `metrics.storage.embedded.persistence.size`
and `retention` together if you need longer history. High-cardinality
workloads (lots of label dimensions) can consume noticeably more — the
embedded instance is fine for clusters up to a few hundred nodes; beyond
that, run a dedicated VictoriaMetrics cluster.

## Architecture

KubeBolt deploys three workloads:

- **API** (Go, Deployment) — Connects to the Kubernetes API using in-cluster ServiceAccount credentials. Runs shared informers, metrics collector, insights engine, and exec/port-forward bridges. Writes samples to and queries from VictoriaMetrics.
- **Web** (nginx, Deployment) — Serves the React frontend and proxies API/WebSocket requests to the API service.
- **VictoriaMetrics** (StatefulSet, optional) — Time-series database for metrics and flow events. Deployed by default; can be replaced with an external instance via `metrics.storage.externalUrl`.

The Helm chart creates a ServiceAccount with a ClusterRole granting read access to all cluster resources, plus exec and port-forward permissions for pod management.

### Agent-proxy clusters

Clusters reached via `kubebolt-agent` (DaemonSet inside another
cluster, dialing back to this backend's gRPC port) are persisted in
the API's BoltDB and **restored on boot** before the gRPC server
starts accepting traffic — so a `helm upgrade` or pod-restart of the
API doesn't blank the cluster selector for the ~30s window each
agent takes to reconnect. Records older than 24h whose agent never
reconnected are pruned automatically;
`KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON` (parseable Go duration, e.g.
`12h`, `7d`) overrides the horizon if you need longer or shorter
retention.

## RBAC

KubeBolt works with any access level. The chart creates a ClusterRole with permissions for:

- Read access to all 23 supported resource types
- Pod exec (terminal) and port-forward
- Metrics Server access
- SelfSubjectAccessReview (permission probing)

If you need restricted access, set `rbac.create=false` and bind your own Role/ClusterRole to the ServiceAccount.

## More Information

- [GitHub Repository](https://github.com/clm-cloud-solutions/kubebolt)
- [Full Documentation](https://github.com/clm-cloud-solutions/kubebolt/blob/main/README.md)
