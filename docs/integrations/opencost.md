# OpenCost — the pricing oracle

OpenCost turns "you are running 3 nodes and 90 pods" into "that costs $743 a
month, and this namespace is $210 of it". It is the source behind **Cost**,
behind the monthly-spend figure on Home, and behind Lifecycle Management's
savings accounting.

KubeBolt does not query OpenCost's API. OpenCost is a **Prometheus exporter**, and
the KubeBolt agent scrapes its `/metrics` and ships the samples through the same
channel as everything else. That is why it needs an agent in the cluster — this
is the one integration that does.

---

## Two ways to install it

### Bundled with the agent (recommended)

The agent chart carries the official OpenCost sub-chart. One flag installs it and
wires the scrape at the same time:

```bash
helm upgrade --install kubebolt-agent kubebolt/kubebolt-agent \
  -n kubebolt --create-namespace \
  --set opencost.enabled=true
```

After toggling it, run `helm dependency update` on the chart — the sub-chart is
pinned in `Chart.yaml` and is not fetched until you do.

### Your own OpenCost

If you already run one, point the agent's scraper at it instead:

```bash
helm upgrade --install kubebolt-agent kubebolt/kubebolt-agent -n kubebolt \
  --set-string collectors.exporters.opencost=http://opencost.opencost.svc.cluster.local:9003/metrics
```

Samples arrive stamped `source=opencost`, and the scrape is **leader-elected**:
exactly one agent pod in the cluster does it, so a DaemonSet across ten nodes
does not multiply the same metrics by ten.

---

## The dependency that catches people out

**OpenCost queries a Prometheus of its own** to do the allocation maths — it needs
usage history to divide a node's cost among the pods that ran on it. Without one:

- **node pricing still flows** — the monthly total is right;
- **container allocation is incomplete** — the per-namespace and per-workload
  breakdown is partial or empty.

The symptom is specific and easy to misread: a total that looks correct with a
breakdown that does not add up to it. Point it at your Prometheus through the
passthrough:

```yaml
opencost:
  enabled: true
  opencost:
    prometheus:
      internal:
        enabled: true
        serviceName: kube-prometheus-stack-prometheus
        namespaceName: monitoring
        port: 9090
```

If you have no Prometheus at all, the [Prometheus integration](prometheus.md)
covers the options — including letting KubeBolt's agent be the one that reads it.

---

## Verify it works

```bash
# 1. OpenCost is exporting
kubectl port-forward -n opencost svc/opencost 9003:9003
curl -s localhost:9003/metrics | grep node_total_hourly_cost

# 2. the agent is scraping it (leader pod only)
kubectl logs -n kubebolt -l app.kubernetes.io/name=kubebolt-agent --tail=50 | grep -i exporter
```

Then **Cost** shows a run-rate within a couple of minutes.

| Symptom | Cause |
|---|---|
| Card "installed", but "no cost samples reaching KubeBolt yet" | OpenCost runs, but nothing scrapes it — the exporter is not wired on the agent |
| Total looks right, breakdown is empty | OpenCost has no Prometheus for allocation (see above) |
| Everything at $0 | OpenCost has no pricing for the provider — on-prem and kind need a custom price list |
| Card "not installed" | no pods labelled `app.kubernetes.io/name=opencost` in `opencost`, `kubecost` or `monitoring` |

That first row deserves a note: the card separates **running** from **feeding**.
KubeBolt checks whether cost samples for this cluster actually reached storage,
so a detected-but-silent OpenCost reports as such instead of looking healthy while
the Cost dashboard stays empty.

---

## What the numbers mean

Prices are **list prices** for the provider and region OpenCost detects, not your
invoice. They do not know about committed-use discounts, reserved instances,
enterprise agreements or spot fluctuation. Expect a consistent gap against
billing — the value is in the *relative* picture, which is the one that drives
decisions: which namespace grew, which workload is over-provisioned, what a
change cost you.

On-prem or bare metal there is no price list to detect, and everything reads $0
until you give OpenCost a custom one.

Cost samples are ordinary metrics: they obey the same retention as the rest, so
history goes as far back as your plan allows.
