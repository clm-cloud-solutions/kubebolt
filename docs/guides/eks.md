# KubeBolt on Amazon EKS

## Quick Install

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
```

This works out of the box — the Helm chart creates a ServiceAccount with a ClusterRole that grants KubeBolt read access to your cluster.

## Access

```bash
kubectl port-forward svc/kubebolt 3000:80
```

Open http://localhost:3000 and sign in as `admin`. Unless you set `auth.adminPassword`, the password is generated on first boot and stored in a Secret:

```bash
kubectl get secret kubebolt-admin-password -o jsonpath='{.data.password}' | base64 -d; echo
```

## IAM Roles for Service Accounts (IRSA)

If your EKS cluster uses IRSA and you need KubeBolt's ServiceAccount to assume an IAM role (e.g., for accessing AWS resources):

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/kubebolt-role
```

> KubeBolt doesn't need AWS permissions to show and operate the cluster — that goes through the Kubernetes API. An IAM role on its ServiceAccount (IRSA, or Pod Identity below) matters when Kobi's AWS cloud connector should read the AWS resources your workloads depend on: it is the identity KubeBolt signs with, or assumes the connector's read-only role from. Set `mcpConnectors.aws.principalArn` to that role's ARN so the trust policy the setup guide prints names it — see [Cloud connectors for Kobi](kobi-mcp-connectors.md#aws).

## EKS Pod Identity (recommended for new clusters)

For clusters using EKS Pod Identity instead of IRSA, associate the identity after install:

```bash
aws eks create-pod-identity-association \
  --cluster-name my-cluster \
  --namespace default \
  --service-account kubebolt \
  --role-arn arn:aws:iam::123456789012:role/kubebolt-role
```

## Ingress with ALB

To expose KubeBolt via an AWS Application Load Balancer:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.className=alb \
  --set ingress.annotations."alb\.ingress\.kubernetes\.io/scheme"=internal \
  --set ingress.annotations."alb\.ingress\.kubernetes\.io/target-type"=ip \
  --set ingress.hosts[0].host=kubebolt.internal.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

Requires the [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/) installed in your cluster.

## Fargate

KubeBolt works on Fargate profiles. Make sure the namespace where you install KubeBolt is included in a Fargate profile. Fargate pods can't mount EBS volumes, so the chart's PersistentVolumeClaims (bundled VictoriaMetrics and the auth database) need an EFS-backed StorageClass (`metrics.storage.embedded.persistence.storageClass`, `auth.persistence.storageClass`) — or run KubeBolt on a managed node group.

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --namespace kubebolt --create-namespace
```

## Troubleshooting

**API pod stuck in `CrashLoopBackOff`:**
Check logs with `kubectl logs -l app.kubernetes.io/component=api`. Common causes:
- RBAC: the ClusterRoleBinding wasn't created. Verify with `kubectl get clusterrolebinding | grep kubebolt`.

**No live CPU/Memory data:**
Check whether Metrics Server is installed:
```bash
kubectl get deployment metrics-server -n kube-system
```
If it's missing, add it as the `metrics-server` EKS add-on, or install it directly:
```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

**403 errors for some resources:**
KubeBolt degrades gracefully. If the ServiceAccount lacks permissions for certain resources, those will appear dimmed in the sidebar with a "Limited access" banner.

## Next steps

- **Historical metrics and remote clusters** — install the [kubebolt-agent](../../deploy/helm/kubebolt-agent/README.md) chart. It ships kubelet/cAdvisor (and Hubble, if present) metrics for the Capacity and Monitor views, and connects clusters whose API server the backend can't reach directly.
- **Let Kobi read AWS** — the load balancers, databases and queues behind your workloads, read-only: [Cloud connectors for Kobi](kobi-mcp-connectors.md#aws).
- **Already on Amazon Managed Service for Prometheus (AMP)?** The agent can read it instead of scraping: see [aws-amp.md](../integrations/aws-amp.md). Other options: [`deployment-scenarios.md`](../deployment-scenarios.md).
