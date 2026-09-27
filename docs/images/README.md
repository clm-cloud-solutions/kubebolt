# Screenshots

The project README and the Artifact Hub listing reference the images in this
folder by name. To refresh a screenshot, replace the file **with the same
name** — nothing else needs to change.

## How to capture

- **Version:** the current release (2.1.x), built from this repository — not
  KubeBolt Cloud, whose screens show plans, teams and Autopilot.
- **Theme:** dark. **Window:** 1600 × 1000 CSS px at 2× device pixel ratio
  (a 3200 × 2000 capture), browser chrome cropped out, no browser zoom.
- **Format:** WebP, quality ~85, under ~400 KB each.
- **Data:** a demo cluster, never a customer's. Cluster, namespace and
  workload names must be neutral (`demo-eu-west`, `shop`, `payments-api`…),
  and no real hostnames, IPs, tokens or e-mail addresses may be visible.
- **Environment that makes every screen meaningful:** the Helm chart plus
  `kubebolt-agent` (`rbac.mode=reader`), at least two clusters, some
  workloads with steady traffic, a few failing ones (the scenarios in
  [`docs/incident-simulations`](../incident-simulations/README.md) work
  well), Trivy Operator for Security, OpenCost for spend, and Kobi Copilot
  configured with an API key. Let it run for an hour or two so charts have
  history.

## The list

| File | Screen | What must be visible |
|---|---|---|
| `kubebolt-overview.webp` | Cluster dashboard → **Overview** (`/`) | Hero image. KPI cards, CPU/memory commitment, workload health, namespaces, recent events; a couple of warning insights so it isn't all green. |
| `kubebolt-home.webp` | **Home** (`/home`) | The "While you were away" shift report with at least one burst, the strip cards (clusters, pods, monthly spend, critical findings) and the attention list. |
| `kubebolt-fleet.webp` | **Fleet** (`/fleet`), table view | Two or more clusters with health, findings, nodes, pods and monthly spend filled in. |
| `kubebolt-capacity.webp` | Cluster dashboard → **Capacity** (`/capacity`) | The KPI row and the four trend charts, with at least one deploy marker. |
| `kubebolt-rightsizing.webp` | **Capacity**, scrolled to the lower panels | Top Workloads · CPU and Right-sizing Recommendations with rows of each kind (over-provisioned, near limit, no specs). |
| `kubebolt-reliability.webp` | Cluster dashboard → **Reliability** (needs Hubble) | The KPI row, the cluster error-rate chart and Top Workloads · Traffic with at least one workload returning 5xx, plus Error Hot-spots. |
| `kubebolt-cost.webp` | Cluster dashboard → **Cost** (needs OpenCost) | Run-rate, idle, savings, cost per pod and efficiency cards, the cost trends and the breakdown by namespace. |
| `kubebolt-cluster-map.webp` | **Cluster Map** (`/map`), Flow layout | One or two namespaces: Service → Deployment → ReplicaSet → Pod chains, at least one unhealthy node highlighted. |
| `kubebolt-cluster-map-traffic.webp` | **Cluster Map**, Traffic layout (needs Hubble) | Caller → Service → Pod edges across two or three namespaces, an erroring edge in red and an external destination. |
| `kubebolt-insight-episode.webp` | **Insights** → an episode (`/insights/episodes/:id`) | The timeline (opened, flapped or escalated, resolved), the recurrence count and the recommendation. |
| `kubebolt-workload.webp` | A **Deployment** detail page | Toolbar actions (Scale, Restart, Roll back…), the Pods tab or Monitor tab, and the History tab label visible. |
| `kubebolt-security.webp` | **Security** → Vulnerabilities (`/security`) | The four lens cards and "workloads to fix" grouped per workload, from Trivy Operator. |
| `kubebolt-kobi.webp` | **Kobi** panel (⌘J) over a failing workload | A question, the tool calls, the root cause, and an action proposal card with its dry-run preview and the Approve button. |

`kubebolt-icon.png` / `kubebolt-icon.svg` are the brand mark (also used by the
Helm charts and the Discord notification avatar); don't replace them with a
screenshot.
