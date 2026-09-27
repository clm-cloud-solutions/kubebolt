# KubeBolt on Google Kubernetes Engine (GKE)

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

## Workload Identity Federation

If your GKE cluster uses Workload Identity Federation and you need KubeBolt's Kubernetes ServiceAccount to act as a Google Cloud service account:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set serviceAccount.annotations."iam\.gke\.io/gcp-service-account"=kubebolt@my-project.iam.gserviceaccount.com
```

Then bind the KSA to the GSA:

```bash
gcloud iam service-accounts add-iam-policy-binding kubebolt@my-project.iam.gserviceaccount.com \
  --role roles/iam.workloadIdentityUser \
  --member "serviceAccount:my-project.svc.id.goog[default/kubebolt]"
```

> KubeBolt itself doesn't need GCP permissions — it only talks to the Kubernetes API. Workload Identity is only relevant for GCP-integrated scenarios.

## Ingress with GCE

To expose KubeBolt via a Google Cloud Load Balancer:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.className=gce \
  --set ingress.hosts[0].host=kubebolt.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

For an internal load balancer, use the `gce-internal` class annotation instead of `ingress.className` (the API server rejects an Ingress that sets both):

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set ingress.enabled=true \
  --set ingress.annotations."kubernetes\.io/ingress\.class"=gce-internal \
  --set ingress.hosts[0].host=kubebolt.internal.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

## GKE Autopilot

KubeBolt works on Autopilot clusters. Resource requests/limits are enforced by Autopilot automatically. The default values in the chart are within Autopilot's accepted ranges:

```bash
helm install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --namespace kubebolt --create-namespace
```

> Autopilot may adjust resource requests to meet minimum thresholds. This is normal.

## Troubleshooting

**API pod stuck in `CrashLoopBackOff`:**
Check logs with `kubectl logs -l app.kubernetes.io/component=api`. Common causes:
- RBAC: verify the ClusterRoleBinding exists: `kubectl get clusterrolebinding | grep kubebolt`

**No live CPU/Memory data:**
GKE runs Metrics Server as a managed component in `kube-system`. Verify the metrics API is available:
```bash
kubectl get apiservice v1beta1.metrics.k8s.io
```

**Binary Authorization blocking images:**
If your cluster enforces Binary Authorization, add an exemption for `ghcr.io/clm-cloud-solutions/kubebolt/*` or use a custom attestation policy.

**403 errors for some resources:**
KubeBolt degrades gracefully. Restricted resources appear dimmed in the sidebar with a "Limited access" banner.

## Next steps

- **Historical metrics and remote clusters** — install the [kubebolt-agent](../../deploy/helm/kubebolt-agent/README.md) chart. It ships kubelet/cAdvisor (and Hubble, if present) metrics for the Capacity and Monitor views, and connects clusters whose API server the backend can't reach directly.
- **Already on Google Cloud Managed Service for Prometheus (GMP)?** The agent can read it instead of scraping: see [gcp-managed-prometheus.md](../integrations/gcp-managed-prometheus.md). Other options: [`deployment-scenarios.md`](../deployment-scenarios.md).
