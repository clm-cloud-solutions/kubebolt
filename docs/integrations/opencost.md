# OpenCost — the pricing oracle

> **Applies to:** KubeBolt ≥ 1.22.0 with kubebolt-agent chart ≥ 1.3.0.

OpenCost turns "you are running 3 nodes and 90 pods" into "that costs $743 a
month, and this namespace is $210 of it". It is the source behind:

- the **Cost** tab of the cluster dashboard (next to Overview, Capacity and
  Reliability; marked **Beta**). The tab only appears once OpenCost series reach
  KubeBolt — a cluster with no cost feed gets no empty tab;
- the **monthly spend** figures on **Home** and per cluster on **Fleet**;
- the dollar value on **rightsizing** recommendations in the Cost tab — each
  over-provisioned workload's reclaimable CPU and memory priced at the cluster's
  node rates.

KubeBolt does not query OpenCost's API. OpenCost is a **Prometheus exporter**, and
the KubeBolt agent scrapes its `/metrics` and ships the samples through the same
channel as everything else. That is why it needs an agent in the cluster — this
is the one integration that does.

---

## Three ways to feed it

The **Add cluster** wizard (agent install) asks which one you want and renders the
matching `helm` flags. By hand:

### Bundled with the agent (recommended)

The agent chart carries the official OpenCost sub-chart (pinned, installed only
when `opencost.enabled=true`; off by default). The published OCI chart already
contains it, so there is nothing to fetch. One flag installs it and wires the
scrape at the same time — plus the Prometheus OpenCost itself needs (see
[below](#the-dependency-that-catches-people-out)):

```bash
helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --create-namespace \
  --reuse-values \
  --set opencost.enabled=true \
  --set opencost.opencost.prometheus.internal.serviceName=kube-prometheus-stack-prometheus \
  --set opencost.opencost.prometheus.internal.namespaceName=monitoring \
  --set opencost.opencost.prometheus.internal.port=9090
```

(`--reuse-values` keeps the backend URL, auth and cluster settings of an existing
agent release; drop it on a fresh install and pass those flags instead.) The
agent scrapes the sub-chart at `http://<release>-opencost.<namespace>.svc:9003/metrics`
automatically — no need to also list it under `collectors.exporters`.

Only if you install from a source checkout of the chart
(`deploy/helm/kubebolt-agent`) do you need `helm dependency build` first, to pull
the pinned sub-chart into `charts/`.

### Your own OpenCost

If you already run one, point the agent's scraper at it instead:

```bash
helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  -n kubebolt --reuse-values \
  --set-string collectors.exporters.opencost=http://opencost.opencost.svc.cluster.local:9003/metrics
```

Samples arrive stamped `source=opencost`, and the scrape is **leader-elected**:
exactly one agent pod in the cluster does it, so a DaemonSet across ten nodes
does not multiply the same metrics by ten.

### Read it from your Prometheus (Mode C)

If your Prometheus already scrapes OpenCost and the agent reads that Prometheus
([Mode C](./prometheus.md#which-ingest-mode-fits-your-cluster)), add
`--set agent.promRead.cost.enabled=true`. The agent then appends the OpenCost
families (`node_*_hourly_cost`, `container_*_allocation`, `pv_hourly_cost`) to its
promRead matchers; the core matchers stay.

---

## The dependency that catches people out

**OpenCost queries a Prometheus of its own** to do the allocation maths — it needs
usage history to divide a node's cost among the pods that ran on it.

With the **bundled** sub-chart this is not optional. If you do not point it at a
Prometheus, the official chart falls back to
`prometheus-server.prometheus-system.svc` — and on any cluster where no Prometheus
lives under that exact name, OpenCost **crash-loops on boot**
(`Failed to create Prometheus data source: ... no such host`).

The **Add cluster** wizard asks for that Prometheus URL when you pick the bundled
mode and turns it into the right sub-chart values: a
`<service>.<namespace>.svc[:port]` address becomes
`opencost.opencost.prometheus.internal.*`, anything else becomes
`opencost.opencost.prometheus.external.url` (with `internal.enabled=false`). When
the cluster's metrics source is already promRead, the wizard reuses that URL and
does not ask twice. By hand, the values look like:

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
      # or, for a Prometheus outside the cluster:
      # internal:
      #   enabled: false
      # external:
      #   enabled: true
      #   url: https://prometheus.example.com
```

If OpenCost reaches a Prometheus that lacks the usage history it needs:

- **node pricing still flows** — the monthly total is right;
- **container allocation is incomplete** — the per-namespace and per-workload
  breakdown is partial or empty.

The symptom is specific and easy to misread: a total that looks correct with a
breakdown that does not add up to it.

If you have no Prometheus at all, the [Prometheus integration](prometheus.md)
covers the options — including letting KubeBolt's agent be the one that reads it.

---

## Verify it works

```bash
# 1. OpenCost is exporting
#    bundled: the Service is <release>-opencost in the agent's namespace
kubectl port-forward -n kubebolt svc/kubebolt-agent-opencost 9003:9003
#    your own: adjust namespace / Service, e.g. -n opencost svc/opencost
curl -s localhost:9003/metrics | grep node_total_hourly_cost

# 2. the agent is scraping it (leader pod only)
kubectl logs -n kubebolt -l app.kubernetes.io/name=kubebolt-agent --tail=50 | grep -i exporter
```

Then the **Cost** tab appears on the cluster dashboard and shows a run-rate
within a couple of minutes. The **OpenCost** card under **Administration →
Agents & Ingest → Integrations** reports the same state:

| Symptom | Cause |
|---|---|
| Card "installed", but "no cost samples reaching KubeBolt yet" | OpenCost runs, but nothing scrapes it — the exporter is not wired on the agent |
| Card "degraded" — "OpenCost pods not fully ready" | Often the bundled sub-chart crash-looping because it has no reachable Prometheus (see above) |
| Total looks right, breakdown is empty | OpenCost's Prometheus lacks the usage history for allocation (see above) |
| Everything at $0 | OpenCost has no pricing for the provider — on-prem and kind need a custom price list |
| Card "not installed" | no pods labelled `app.kubernetes.io/name=opencost` anywhere in the cluster, and no pod whose name starts with `opencost` in the `opencost`, `kubecost` or `monitoring` namespaces |

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

Cost samples are ordinary metrics: they obey the same retention as the rest of
KubeBolt's VictoriaMetrics (30 days by default on the bundled one —
`metrics.storage.embedded.retention` in the `kubebolt` chart), so history goes as
far back as that.
