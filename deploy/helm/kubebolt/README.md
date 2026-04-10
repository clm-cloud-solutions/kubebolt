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

## Architecture

KubeBolt deploys two containers:

- **API** (Go) — Connects to the Kubernetes API using in-cluster ServiceAccount credentials. Runs shared informers, metrics collector, insights engine, and exec/port-forward bridges.
- **Web** (nginx) — Serves the React frontend and proxies API/WebSocket requests to the API service.

The Helm chart creates a ServiceAccount with a ClusterRole granting read access to all cluster resources, plus exec and port-forward permissions for pod management.

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
