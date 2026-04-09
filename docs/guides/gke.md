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

Open http://localhost:3000

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

For internal load balancer:

```bash
  --set ingress.annotations."kubernetes\.io/ingress\.class"=gce-internal
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

**No metrics data:**
GKE has Metrics Server enabled by default. If you disabled it, re-enable via:
```bash
gcloud container clusters update my-cluster --enable-managed-prometheus
```
Or install Metrics Server manually.

**Binary Authorization blocking images:**
If your cluster enforces Binary Authorization, add an exemption for `ghcr.io/clm-cloud-solutions/kubebolt/*` or use a custom attestation policy.

**403 errors for some resources:**
KubeBolt degrades gracefully. Restricted resources appear dimmed in the sidebar with a "Limited access" banner.
