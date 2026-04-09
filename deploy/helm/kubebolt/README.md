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

## Ingress

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=kubebolt.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

## Cloud-specific Guides

- [Amazon EKS](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/eks.md)
- [Google GKE](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/gke.md)
- [Azure AKS](https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/guides/aks.md)

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
