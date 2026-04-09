# KubeBolt on Azure Kubernetes Service (AKS)

## Quick Install

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
```

This works out of the box — the Helm chart creates a ServiceAccount with a ClusterRole that grants KubeBolt read access to your cluster.

## Access

```bash
kubectl port-forward svc/kubebolt 3000:80
```

Open http://localhost:3000

## Azure AD Workload Identity

If your AKS cluster uses Azure AD Workload Identity and you need KubeBolt's ServiceAccount to authenticate with Azure services:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set serviceAccount.annotations."azure\.workload\.identity/client-id"=<CLIENT_ID> \
  --set serviceAccount.labels."azure\.workload\.identity/use"=true
```

Then create the federated credential:

```bash
az identity federated-credential create \
  --name kubebolt-federated \
  --identity-name kubebolt-identity \
  --resource-group my-rg \
  --issuer $(az aks show -n my-cluster -g my-rg --query "oidcIssuerProfile.issuerUrl" -o tsv) \
  --subject system:serviceaccount:default:kubebolt
```

> KubeBolt itself doesn't need Azure permissions — it only talks to the Kubernetes API. Workload Identity is only relevant for Azure-integrated scenarios.

## Ingress with AGIC

To expose KubeBolt via Azure Application Gateway Ingress Controller:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.className=azure-application-gateway \
  --set ingress.hosts[0].host=kubebolt.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

For nginx ingress controller (common on AKS):

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=kubebolt.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

## Azure RBAC for Kubernetes

If your AKS cluster uses Azure RBAC for Kubernetes authorization (instead of native Kubernetes RBAC), KubeBolt's ClusterRole still applies — Azure RBAC and Kubernetes RBAC coexist. The ServiceAccount created by the Helm chart uses native Kubernetes RBAC.

## AKS with Azure CNI

KubeBolt works with both kubenet and Azure CNI networking. No special configuration needed.

## Troubleshooting

**API pod stuck in `CrashLoopBackOff`:**
Check logs with `kubectl logs -l app.kubernetes.io/component=api`. Common causes:
- RBAC: verify the ClusterRoleBinding exists: `kubectl get clusterrolebinding | grep kubebolt`

**No metrics data:**
AKS has Metrics Server enabled by default. Verify with:
```bash
kubectl get apiservice v1beta1.metrics.k8s.io
```

**Image pull errors:**
If your AKS cluster uses a private ACR and restricts external registries, you may need to allow `ghcr.io`. Alternatively, mirror the images to your ACR:
```bash
az acr import --name myacr --source ghcr.io/clm-cloud-solutions/kubebolt/api:1.0.5
az acr import --name myacr --source ghcr.io/clm-cloud-solutions/kubebolt/web:1.0.5
```

Then override image repositories in the Helm values.

**403 errors for some resources:**
KubeBolt degrades gracefully. Restricted resources appear dimmed in the sidebar with a "Limited access" banner.
