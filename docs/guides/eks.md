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

Open http://localhost:3000

## IAM Roles for Service Accounts (IRSA)

If your EKS cluster uses IRSA and you need KubeBolt's ServiceAccount to assume an IAM role (e.g., for accessing AWS resources):

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/kubebolt-role
```

> KubeBolt itself doesn't need AWS permissions — it only talks to the Kubernetes API. IRSA is only relevant if you're running KubeBolt alongside AWS-integrated workloads that share the ServiceAccount.

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

KubeBolt works on Fargate profiles. Make sure the namespace where you install KubeBolt is included in a Fargate profile:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --namespace kubebolt --create-namespace
```

## Troubleshooting

**API pod stuck in `CrashLoopBackOff`:**
Check logs with `kubectl logs -l app.kubernetes.io/component=api`. Common causes:
- RBAC: the ClusterRoleBinding wasn't created. Verify with `kubectl get clusterrolebinding | grep kubebolt`.

**No metrics data:**
EKS doesn't install Metrics Server by default. Install it:
```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

**403 errors for some resources:**
KubeBolt degrades gracefully. If the ServiceAccount lacks permissions for certain resources, those will appear dimmed in the sidebar with a "Limited access" banner.
