# KubeBolt Helm Chart

[KubeBolt](https://kubebolt.io) is an open-source **Kubernetes operations
platform**: one place to see, understand and operate your clusters — health
and insights, topology, day-2 operations (logs, exec, YAML edit, scale,
restart), cost and security. It includes **Kobi Copilot**, KubeBolt's AI SRE,
running on your own LLM API key (BYOK), plus a read-only MCP server for
external AI tools.

This chart deploys the KubeBolt backend (API), the web UI and, by default, an
embedded VictoriaMetrics for metrics history. To monitor other clusters or
collect kubelet / Hubble metrics, pair it with the
[`kubebolt-agent`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/deploy/helm/kubebolt-agent/README.md) chart.

User documentation: [kubebolt.io/docs/helm](https://kubebolt.io/docs/helm) ·
[installation](https://kubebolt.io/docs/installation) ·
[environment variables](https://kubebolt.io/docs/environment-variables) ·
[troubleshooting](https://kubebolt.io/docs/troubleshooting).

## Install

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --namespace kubebolt --create-namespace
```

Then access it via port-forward:

```bash
kubectl -n kubebolt port-forward svc/kubebolt 3000:80
```

Open http://localhost:3000 and sign in as `admin`. On first boot, if you did
not set `auth.adminPassword` or `auth.existingSecret`, the API generates a
password and stores it in the `kubebolt-admin-password` Secret:

```bash
kubectl -n kubebolt get secret kubebolt-admin-password -o jsonpath='{.data.password}' | base64 -d ; echo
```

See [Authentication](#authentication) for details and password recovery.

## Configuration model — values are boot defaults

Most runtime settings can be edited in the **Administration** area of the UI
after the first login, with no redeploy. They are stored in the API's embedded
database (on the `auth.persistence` volume), and a value saved in the UI
overrides the value this chart sets:

| Domain | Where in the UI | Chart values that seed it |
|--------|-----------------|---------------------------|
| Kobi (AI copilot) — provider, model, API key, fallback | Administration → AI (Kobi) | `copilot.*` |
| Notifications (Slack, Discord, SMTP, severity gates) | Administration → System → Notifications | `extraEnv` |
| General (display name, refresh interval) | Administration → System → General | `extraEnv` |
| Authentication (token lifetimes; restart required) | Administration → Access → Authentication | `extraEnv` |
| Agents & Ingest (agent auth mode, rate limit, auto-registration, prune horizon, remote_write receiver) | Administration → Agents & Ingest → Configuration | `agentIngest.authMode`, `agentIngest.autoRegisterClusters`, `agentIngest.rateLimit.*`, `metrics.remoteWrite.*` |

You can leave these values empty and configure everything from the UI, or set
them here for GitOps / config-as-code installs. A few Agents & Ingest fields
(auth mode, token audience, mTLS) only take effect after an API restart.

Values that stay **boot-time only**: images, resources, Service / Ingress /
Gateway API exposure, ServiceAccount and RBAC, `agentIngest.port` and
`agentIngest.tls`, metrics storage (image, PVC, retention), and
`auth.adminPassword` / `auth.jwtSecret` / `auth.persistence`.

To see which `KUBEBOLT_*` environment variables the running API received, open
**Administration → System → Boot snapshot**.

## Parameters

Full reference with comments: [`values.yaml`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/deploy/helm/kubebolt/values.yaml).

### API and web

| Parameter | Description | Default |
|-----------|-------------|---------|
| `api.image.repository` | API image | `ghcr.io/clm-cloud-solutions/kubebolt/api` |
| `api.image.tag` | API image tag | `""` (Chart appVersion) |
| `api.image.pullPolicy` | API image pull policy | `IfNotPresent` |
| `api.port` | API container port | `8080` |
| `api.resources` | API requests / limits | `50m`/`64Mi` – `500m`/`256Mi` |
| `web.image.repository` | Web (nginx + UI) image | `ghcr.io/clm-cloud-solutions/kubebolt/web` |
| `web.image.tag` | Web image tag | `""` (Chart appVersion) |
| `web.image.pullPolicy` | Web image pull policy | `IfNotPresent` |
| `web.port` | Web container port | `3000` |
| `web.resources` | Web requests / limits | `10m`/`16Mi` – `100m`/`64Mi` |
| `replicaCount` | Replicas for the API and web Deployments. Keep at `1`: the embedded database is single-writer (the API uses `strategy: Recreate`). | `1` |
| `extraEnv` | Extra env vars for the API container, for `KUBEBOLT_*` settings without a dedicated value (see [Environment variables](https://kubebolt.io/docs/environment-variables) and the examples in `values.yaml`) | `[]` |

### Service, Ingress and Gateway API

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Type of the HTTP Service (UI + API via nginx) | `ClusterIP` |
| `service.port` | HTTP Service port | `80` |
| `service.nodePort` | Fixed NodePort when `service.type=NodePort`; empty = auto-assign | `""` |
| `ingress.enabled` | Create an Ingress | `false` |
| `ingress.className` | IngressClass name | `""` |
| `ingress.annotations` | Ingress annotations | `{}` |
| `ingress.hosts` | Hosts and paths | `[{host: kubebolt.local, paths: [{path: /, pathType: Prefix}]}]` |
| `ingress.tls` | Ingress TLS config | `[]` |
| `gatewayAPI.enabled` | Create a Gateway + routes instead of an Ingress (enable one or the other) | `false` |
| `gatewayAPI.gatewayClassName` | GatewayClass of your controller | `eg` (Envoy Gateway) |
| `gatewayAPI.forceHTTP1` | Create an Envoy Gateway `ClientTrafficPolicy` forcing HTTP/1.1 on the UI/API listener (needed for WebSockets with Envoy Gateway). Set `false` for other controllers. | `true` |
| `gatewayAPI.annotations` | Extra annotations on the Gateway | `{}` |
| `gatewayAPI.certManager.clusterIssuer` | cert-manager ClusterIssuer that issues the listener certificates; empty = you provide the TLS Secrets | `""` |
| `gatewayAPI.api.host` | Hostname for the UI/API listener (HTTPRoute) | `kubebolt.example.com` |
| `gatewayAPI.api.tlsSecretName` | TLS Secret for that listener | `kubebolt-api-tls` |
| `gatewayAPI.api.requestTimeout` | HTTPRoute request timeout; `0s` disables Envoy Gateway's 15 s default, which would cut Kobi's streaming responses | `0s` |
| `gatewayAPI.agent.enabled` | Add a listener + GRPCRoute for the agent gRPC channel | `true` |
| `gatewayAPI.agent.host` | Hostname agents dial | `agent.kubebolt.example.com` |
| `gatewayAPI.agent.tlsSecretName` | TLS Secret for the agent listener | `kubebolt-agent-tls` |

### ServiceAccount and RBAC

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceAccount.create` | Create the API ServiceAccount | `true` |
| `serviceAccount.name` | ServiceAccount name; empty = release fullname | `""` |
| `serviceAccount.annotations` | ServiceAccount annotations (e.g. IRSA / Workload Identity) | `{}` |
| `rbac.create` | Create the ClusterRole / ClusterRoleBinding described in [RBAC](#rbac) | `true` |

### Agent ingest (gRPC channel for `kubebolt-agent`)

| Parameter | Description | Default |
|-----------|-------------|---------|
| `agentIngest.enabled` | Listen for `kubebolt-agent` connections | `true` |
| `agentIngest.port` | gRPC port agents dial | `9090` |
| `agentIngest.externalUrl` | Public `host:port` remote agents dial (e.g. `agent.example.com:443`). When set, the Add cluster wizard defaults to it with TLS and authentication on. Leave empty for co-located installs. | `""` |
| `agentIngest.authMode` | `disabled` / `permissive` (authenticate, log failures, still accept) / `enforced` (reject unauthenticated agents) | `disabled` |
| `agentIngest.autoRegisterClusters` | Add a cluster to the cluster switcher automatically when an authenticated agent with the API proxy capability connects | `true` |
| `agentIngest.tokenAudience` | Audience the agent's projected ServiceAccount token must carry (agent `auth.mode=tokenreview`); must match the agent's `auth.tokenReview.audience` | `kubebolt-backend` |
| `agentIngest.service.create` | Create the `<release>-agent-ingest` Service | `true` |
| `agentIngest.service.type` | Type of that Service | `ClusterIP` |
| `agentIngest.service.nodePort` | Fixed NodePort when `type=NodePort`; empty = auto-assign | `""` |
| `agentIngest.rbac.enableTokenReview` | Grant `tokenreviews/create` so the API can validate agent `tokenreview` credentials. Can be `false` when every agent uses ingest tokens. | `true` |
| `agentIngest.rateLimit.enabled` | Token-bucket rate limit on the agent channel | `false` |
| `agentIngest.rateLimit.requestsPerSec` | Sustained rate | `1000` |
| `agentIngest.rateLimit.burst` | Burst size | `2000` |
| `agentIngest.tls.enabled` | Serve the agent channel over TLS | `false` |
| `agentIngest.tls.existingSecret` | Secret with `tls.crt` / `tls.key` (required when TLS is on) | `""` |
| `agentIngest.tls.clientCASecret` | Secret with `ca.crt` used to verify agent client certificates (mTLS) | `""` |
| `agentIngest.tls.requireClientCert` | Reject agents without a verified client certificate (requires `clientCASecret`) | `false` |

### Metrics storage

| Parameter | Description | Default |
|-----------|-------------|---------|
| `metrics.storage.embedded.enabled` | Deploy the bundled single-node VictoriaMetrics StatefulSet | `true` |
| `metrics.storage.embedded.image.repository` | VictoriaMetrics image | `victoriametrics/victoria-metrics` |
| `metrics.storage.embedded.image.tag` | VictoriaMetrics tag | `v1.148.0-scratch` |
| `metrics.storage.embedded.image.pullPolicy` | Pull policy | `IfNotPresent` |
| `metrics.storage.embedded.retention` | Retention window | `30d` |
| `metrics.storage.embedded.persistence.enabled` | Use a PVC (otherwise `emptyDir`) | `true` |
| `metrics.storage.embedded.persistence.size` | PVC size | `10Gi` |
| `metrics.storage.embedded.persistence.storageClass` | StorageClass; empty = cluster default | `""` |
| `metrics.storage.embedded.persistence.accessModes` | PVC access modes | `[ReadWriteOnce]` |
| `metrics.storage.embedded.resources` | VictoriaMetrics requests / limits | `100m`/`256Mi` – `1000m`/`2Gi` |
| `metrics.storage.embedded.extraArgs` | Extra VictoriaMetrics flags, appended after the chart's (`--dedup.minScrapeInterval=30s` is set by default; pass `--dedup.minScrapeInterval=0` to disable) | `[]` |
| `metrics.storage.externalUrl` | External VictoriaMetrics-compatible URL, required when `embedded.enabled=false` | `""` |
| `metrics.remoteWrite.enabled` | Accept Prometheus `remote_write` at `POST /api/v1/prom/write` (used by the agent's vmagent sidecar or your own Prometheus) | `false` |
| `metrics.remoteWrite.authMode` | Bearer-token enforcement on that endpoint: `disabled` / `permissive` / `enforced` (same ingest tokens as the agent channel) | `disabled` |

### Authentication

| Parameter | Description | Default |
|-----------|-------------|---------|
| `auth.enabled` | Built-in login with Admin / Editor / Viewer roles | `true` |
| `auth.adminPassword` | Initial `admin` password; generated on first boot if empty | `""` |
| `auth.jwtSecret` | JWT signing secret; generated if empty (sessions do not survive a restart) | `""` |
| `auth.existingSecret` | Existing Secret with `admin-password` and/or `jwt-secret` keys | `""` |
| `auth.resetAdminPassword` | Reset the `admin` password to this value on next start — see [Forgot-password recovery](#forgot-password-recovery) | `""` |
| `auth.persistence.enabled` | PVC for the embedded database (users, settings, history); `emptyDir` if false | `true` |
| `auth.persistence.size` | PVC size | `1Gi` |
| `auth.persistence.storageClass` | StorageClass; empty = cluster default | `""` |
| `auth.persistence.accessModes` | PVC access modes | `[ReadWriteOnce]` |

### Kobi (AI copilot)

These values seed Kobi at boot; **Administration → AI (Kobi)** is the
preferred place to configure it.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `copilot.enabled` | Enable Kobi Copilot from chart values | `false` |
| `copilot.provider` | `anthropic`, `openai` or `custom` | `anthropic` |
| `copilot.model` | Model name; provider default if empty | `""` |
| `copilot.baseUrl` | Custom / self-hosted endpoint | `""` |
| `copilot.maxTokens` | Max output tokens per response | `4096` |
| `copilot.apiKey` | LLM API key (prefer `existingSecret`) | `""` |
| `copilot.existingSecret` | Existing Secret with an `api-key` key | `""` |
| `copilot.fallback.enabled` | Use a fallback model when the primary fails (429, 5xx, network) | `false` |
| `copilot.fallback.provider` | Fallback provider; defaults to the primary's | `""` |
| `copilot.fallback.model` | Fallback model (required when fallback is enabled) | `""` |
| `copilot.fallback.baseUrl` | Fallback endpoint | `""` |
| `copilot.fallback.apiKey` / `copilot.fallback.existingSecret` | Fallback API key, inline or from a Secret with `api-key` | `""` |

The chart templates also honor `nameOverride` and `fullnameOverride`.

## Exposing KubeBolt

### Ingress

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --namespace kubebolt --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=kubebolt.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

The Ingress routes to the web Service, which serves the UI and proxies `/api`
and `/ws` to the API. The agent gRPC channel is not exposed by the Ingress; for
remote agents expose `<release>-agent-ingest` separately (LoadBalancer,
NodePort, or a gRPC-capable proxy) or use the Gateway API option below.

### Gateway API

As an alternative to Ingress, the chart can create a Gateway API `Gateway`
with two HTTPS listeners: one for the UI/API (`HTTPRoute`) and one for the
agent gRPC channel (`GRPCRoute`, TLS terminated at the gateway). It needs a
Gateway API controller and CRDs in the cluster; the defaults target
[Envoy Gateway](https://gateway.envoyproxy.io/) (GatewayClass `eg`).

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --namespace kubebolt --create-namespace \
  --set gatewayAPI.enabled=true \
  --set gatewayAPI.api.host=kubebolt.example.com \
  --set gatewayAPI.agent.host=agent.kubebolt.example.com \
  --set gatewayAPI.certManager.clusterIssuer=letsencrypt-prod \
  --set web.enabled=true
```

Notes:

- `web.enabled` is not listed in `values.yaml`. When it is `true`, the
  HTTPRoute targets the web Service (UI + API through nginx). When it is unset,
  the HTTPRoute targets the API Service directly, which only fits setups that
  serve the UI from somewhere else. Set it to `true` for a self-contained
  install.
- Keep `gatewayAPI.forceHTTP1=true` with Envoy Gateway: its default HTTP/2 to
  browsers breaks the WebSocket upgrade (live updates, pod terminal). The
  policy only applies to the UI/API listener; the agent listener keeps HTTP/2,
  which gRPC requires. With another controller, set it to `false`.
- If a CDN proxies your DNS, keep the agent hostname DNS-only: proxies break
  long-lived bidirectional gRPC.
- Remote agents then use `backendUrl=agent.kubebolt.example.com:443` with
  `tls.enabled=true` (see the [agent chart](https://github.com/clm-cloud-solutions/kubebolt/blob/main/deploy/helm/kubebolt-agent/README.md)).

## Authentication

KubeBolt includes built-in authentication with three roles: **Admin** (full
access + administration), **Editor** (edit YAML, scale, restart) and
**Viewer** (read-only). Enabled by default.

### Initial admin password

On first boot an `admin` user is seeded. If neither `auth.adminPassword` nor
`auth.existingSecret` is set, the API:

1. Generates a random password,
2. Prints it once to the API log (only when seeding actually happens —
   restarts on an existing database stay quiet),
3. Persists it to a Secret named `kubebolt-admin-password` in the release
   namespace.

Retrieve it any time with:

```bash
kubectl -n <ns> get secret kubebolt-admin-password -o jsonpath='{.data.password}' | base64 -d ; echo
```

For production, supply your own Secret instead:

```bash
kubectl -n kubebolt create secret generic kubebolt-auth \
  --from-literal=admin-password=YourSecurePassword \
  --from-literal=jwt-secret=$(openssl rand -hex 32)

helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --namespace kubebolt \
  --set auth.existingSecret=kubebolt-auth
```

To disable auth (open access, no login):

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set auth.enabled=false
```

### Forgot-password recovery

Two paths, pick whichever fits your workflow.

**Path A — `helm upgrade` (recommended for Helm-managed installs).** Sets
`KUBEBOLT_RESET_ADMIN_PASSWORD` on the Deployment; the API resets the admin
password on next start, then continues normal boot. With `strategy: Recreate`
the database lock is released cleanly during the rollover.

```bash
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --reuse-values --set auth.resetAdminPassword=NEWPASS

# log in with NEWPASS, change to your real password from the Account menu, then:
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --reuse-values --set auth.resetAdminPassword=
```

**Path B — one-shot `Job` (when Helm isn't available, or for a runbook).**
Scale the API to zero (the database is single-writer), run a Job with the same
image and PVC, scale back up:

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

Min password length: 8 chars. Both paths log the reset to the API log so it's
auditable.

## Kobi (AI copilot)

Kobi is KubeBolt's AI SRE. In the open-source edition it runs as **Kobi
Copilot**: an in-app assistant that investigates your cluster with read tools
and proposes actions, using **your own** LLM provider API key — KubeBolt has
no managed AI service in this edition.

The simplest path is to configure it after login in **Administration → AI
(Kobi)**. To seed it from the chart instead:

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

- [Enabling Kobi](https://kubebolt.io/docs/enabling-kobi)
- [Kobi configuration guide](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/copilot.md) — fallback, recipes, privacy notes
- [Providers reference](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/copilot-providers.md) — Anthropic, OpenAI, Azure, Groq, OpenRouter, DeepSeek, Mistral, self-hosted Ollama/vLLM, and more
- [Kobi MCP server (read-only)](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/kobi-mcp.md) — use Kobi's read-only tools from Claude Code, Cursor or other MCP hosts via `POST /api/v1/mcp`

## Security and cost integrations

KubeBolt reads the output of common open-source tools and shows it under
**Security** and **Cost**. Nothing extra is needed in this chart: the RBAC
already grants read access to the scanner CRDs.

| Tool | What it adds | How KubeBolt gets it | Guide |
|------|--------------|----------------------|-------|
| Trivy Operator | Image CVEs, misconfiguration, RBAC posture, exposed secrets, CIS benchmark | Lists Trivy's report CRDs every 10 min | [trivy.md](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/trivy.md) |
| Kyverno (or any PolicyReport producer) | Policy violations | Lists `wgpolicyk8s.io` PolicyReports | [kyverno.md](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/kyverno.md) |
| Falco | Runtime security events | Falco pushes to `POST /api/v1/ingest/falco` with a cluster-scoped ingest token | [falco.md](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/falco.md) |
| OpenCost | Cluster, namespace and workload cost | The `kubebolt-agent` scrapes OpenCost's `/metrics` (optionally installs it) | [opencost.md](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/opencost.md) |

## Metrics storage

Time-series data (CPU, memory, network and filesystem samples shipped by
`kubebolt-agent`, Hubble flow events, remote_write samples) lives in a
Prometheus-compatible TSDB. See
[kubebolt.io/docs/metrics](https://kubebolt.io/docs/metrics).

**Default — embedded VictoriaMetrics.** The chart deploys a single-node
VictoriaMetrics StatefulSet alongside the API, with a 10 GiB PVC and 30-day
retention. Nothing else to configure for it to work.

**Bring your own.** If you already run VictoriaMetrics, vmselect, or any
endpoint compatible with the VM ingestion + query API, point KubeBolt at it
instead:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set metrics.storage.embedded.enabled=false \
  --set metrics.storage.externalUrl=http://vmselect.observability.svc.cluster.local:8481
```

Plain Prometheus remote-write isn't enough — KubeBolt also queries the
endpoint back, so it must accept VM-style reads. vmagent + vmselect behind a
single URL is the typical setup. Rendering fails if embedded storage is off and
`externalUrl` is empty.

**External TSDB + dedicated KSM scrape — enable dedup.** The bundled
VictoriaMetrics runs with `--dedup.minScrapeInterval=30s` as a storage-side
backstop against duplicate `kube-state-metrics` samples (for example when the
agent's `scrape.discovery.kubeStateMetrics.enabled=true` job is used). When you
point KubeBolt at an external VM cluster, **set the same flag on your
vmstorage / vminsert**. See
[`docs/agent-scraping.md`](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/agent-scraping.md) for the full
discussion.

**Sizing.** Rule of thumb: ~1 GiB of PVC per 100 pods at 30-day retention with
default cadence. Bump `metrics.storage.embedded.persistence.size` and
`retention` together if you need longer history. High-cardinality workloads can
consume noticeably more — the embedded instance is fine for clusters up to a
few hundred nodes; beyond that, run a dedicated VictoriaMetrics cluster.

## Architecture

KubeBolt deploys three workloads:

- **API** (Go, Deployment) — Connects to the Kubernetes API using in-cluster
  ServiceAccount credentials (and to other clusters through kubeconfig or
  agents). Runs shared informers, the insights engine, Kobi, exec /
  port-forward bridges and the agent gRPC channel. Writes samples to and
  queries VictoriaMetrics.
- **Web** (nginx, Deployment) — Serves the React UI and proxies API /
  WebSocket requests to the API Service.
- **VictoriaMetrics** (StatefulSet, optional) — Time-series database for
  metrics and flow events. Deployed by default; can be replaced with an
  external instance via `metrics.storage.externalUrl`.

Services: `<release>` (web, port 80), `<release>-api` (port 8080) and
`<release>-agent-ingest` (gRPC, port 9090). With the release name `kubebolt`
in namespace `kubebolt`, an in-cluster agent dials
`kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090`.

### Agent-proxy clusters

Clusters reached via `kubebolt-agent` (an agent inside another cluster dialing
back to this backend's gRPC port) are persisted in the API's database and
**restored on boot** before the gRPC server starts accepting traffic — so a
`helm upgrade` or pod restart of the API doesn't blank the cluster selector
while agents reconnect. Records whose agent has been disconnected for more than
24h are pruned automatically; change the horizon in **Administration → Agents
& Ingest → Configuration** or with `KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON` in
`extraEnv` (a Go duration such as `12h` or `168h`; day units like `7d` are not
accepted).

See [Connect clusters](https://kubebolt.io/docs/connect-clusters) and
[Remote clusters](https://kubebolt.io/docs/remote-clusters).

## RBAC

KubeBolt works with any access level: it probes its permissions at connect
time and dims what it can't read. The chart's ClusterRole grants:

- `get` / `list` / `watch` on the core, apps, batch, networking, storage,
  RBAC, autoscaling, policy (PodDisruptionBudgets), discovery and Gateway API
  resources KubeBolt displays, plus optional CRDs when installed:
  cert-manager Certificates, Argo CD Applications, VPAs, Cilium network
  policies, and the Trivy Operator / Kyverno (`wgpolicyk8s.io`) report CRDs.
- Metrics Server read access and `selfsubjectaccessreviews` (permission
  probing).
- `pods/exec`, `pods/portforward` and `pods/eviction` (terminal, file browser,
  port-forward, evict).
- Write verbs for UI actions (edit YAML, scale, restart, delete, create
  resources) and for the in-UI agent install wizard (namespaces,
  ServiceAccounts, RBAC objects, DaemonSets, ConfigMaps, Secrets), including
  `escalate` / `bind` on roles so the wizard can create the agent's roles.
- `tokenreviews/create` when `agentIngest.rbac.enableTokenReview=true`.

For restricted access, set `rbac.create=false` and bind your own Role /
ClusterRole to the ServiceAccount; views you don't grant are shown as
restricted.

## Cloud-specific guides

- [Amazon EKS](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/eks.md)
- [Google GKE](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/gke.md)
- [Azure AKS](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/aks.md)
- [Red Hat OpenShift](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/openshift.md) — required reading: the web pod needs a workaround under `restricted-v2`

## More information

- [kubebolt.io](https://kubebolt.io) and [documentation](https://kubebolt.io/docs/quickstart)
- [Compatibility matrix](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/COMPATIBILITY.md) — KubeBolt 2.1 pairs with `kubebolt-agent` ≥ 1.0
- [GitHub repository](https://github.com/clm-cloud-solutions/kubebolt)

Kobi Autopilot is not part of the open-source edition; it is available in
[KubeBolt Cloud](https://kubebolt.io) and KubeBolt Enterprise Self-Hosted.
