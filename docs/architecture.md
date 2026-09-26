# Architecture

How KubeBolt is put together, at the level an operator or a new contributor
needs. The user-facing tour lives at <https://kubebolt.io/docs/architecture>;
[`CLAUDE.md`](../CLAUDE.md) carries the package-by-package detail.

```
                       ┌───────────────────────────────────────────────┐
  Browser  ── HTTPS ──►│  Web (React SPA, served by nginx or embedded) │
                       └──────────────┬────────────────────────────────┘
                                      │ REST /api/v1 · WebSocket /ws · SSE
                       ┌──────────────▼────────────────────────────────┐
                       │  KubeBolt API (Go)                     :8080  │
                       │   cluster manager ─ informers per cluster     │
                       │   insights engine ─ 24 rules + lifecycle      │
                       │   findings sweeper (Trivy, Kyverno, CIS)      │
                       │   Kobi Copilot ─ tools, proposals, MCP        │
                       │   auth · API tokens · audit · notifications   │
                       │   BoltDB (embedded state)                     │
                       │   agent channel (gRPC)                 :9090  │
                       └───┬──────────────┬───────────────┬────────────┘
          kubeconfig /     │              │ PromQL        │ outbound gRPC
          in-cluster SA    │              │               │ (agent dials in)
                  ┌────────▼───────┐ ┌────▼──────────┐ ┌──▼─────────────────────┐
                  │ Kubernetes API │ │VictoriaMetrics│ │ kubebolt-agent         │
                  │ + metrics-srv  │ │ (history)     │ │ in a remote cluster:   │
                  └────────────────┘ └───────────────┘ │ metrics, flows, proxy  │
                                                        └────────────────────────┘
```

## Components

**API server** (`apps/api`, Go). A single process that binds its HTTP port
immediately and connects to clusters in the background, so the UI can report
"connecting" or "unreachable" instead of hanging.

- **Cluster manager** — reads every kubeconfig context (or the in-cluster
  ServiceAccount), plus clusters registered by agents or added from the UI.
  Each cluster gets its own connector, metrics collector and insights engine.
- **Connector** — client-go shared informers for every resource type the
  credentials may list, and a dynamic client for CRDs (Gateway API, Cilium
  policies, cert-manager, Argo CD, VPA, Trivy and Kyverno reports). Access is
  probed first with `SelfSubjectAccessReview`, so informers only start for
  what the ServiceAccount can read; namespace-scoped credentials get
  per-namespace informers. Most reads are served from the informer cache.
- **Insights engine** — 24 deterministic rules evaluated against cluster
  state. Each finding opens an *episode* (opened, flapped, escalated, muted,
  resolved, expired) persisted in BoltDB, with mutes and an install-wide rule
  policy layer.
- **Findings sweeper** — every 10 minutes, reads the reports that Trivy
  Operator, Kyverno (or Gatekeeper through PolicyReports) and CIS benchmarks
  already produce in each connected cluster. Falco pushes runtime events to
  `POST /api/v1/ingest/falco`.
- **Kobi Copilot** — a server-side tool-calling loop over a provider you
  configure (Anthropic, or any OpenAI-compatible endpoint). Read tools query
  the same caches the UI uses; action tools only *propose* a change, which is
  dry-run on the server and runs after a person approves it, with that
  person's role. The same read tools are exposed over MCP at `/api/v1/mcp`
  and through the `kubebolt-mcp` stdio binary.
- **Auth and governance** — local users with viewer / editor / admin roles,
  JWT sessions, API tokens for automation, an audit trail of mutations and
  access sessions (exec, port-forward, file reads), and Slack / Discord /
  email notifications.
- **Agent channel** — a gRPC server (port 9090) that agents dial *into*.
  Agents ship samples over it and, when allowed, tunnel Kubernetes API
  requests (including exec, port-forward and file browsing over SPDY) so the
  backend can operate a cluster whose API server it cannot reach.

**Embedded state** — BoltDB (pure Go, no CGO) in `KUBEBOLT_DATA_DIR`:
users, tokens, settings edited in the UI, clusters, agent records, insight
episodes, mutes and policies, security findings, audit trail and Kobi
conversations. An hourly retention pass prunes each kind on its own horizon.

**VictoriaMetrics** — the time-series store for history: node, pod and
container metrics from the agent, Hubble flows, OpenCost series, and anything
received over Prometheus `remote_write` (`/api/v1/prom/write`). The Helm chart
and Docker Compose bundle a single-node instance; you can point at your own
with `KUBEBOLT_METRICS_STORAGE_URL`. Without it, live CPU/memory still comes
from metrics-server and only the historical panels stay empty. Ingest is
capped by active series (1M by default) and the Overview warns when the cap
is near.

**Web** (`apps/web`, React 18 + TypeScript + Vite + Tailwind). TanStack Query
for server state, a WebSocket for change notifications (the socket carries
`{kind, namespace, name, uid}` notifications, never the objects), ReactFlow
for the Cluster Map, xterm.js for the terminal and CodeMirror 6 for YAML.
Routes are declared as *global* (Home, Fleet, Security, Administration) or
*cluster* scope, so global pages keep working when a cluster is down.

**Agent** (`packages/agent`, Go, optional). Outbound-only. Two topologies,
gated by `KUBEBOLT_AGENT_MODE`:

- **DaemonSet** — per-node kubelet/cAdvisor stats and, with Cilium, Hubble
  flow events (L4, HTTP, DNS); optional vmagent sidecar to scrape
  Prometheus-style `/metrics` endpoints; optional Kubernetes API proxy.
- **promread Deployment** — a single leader that reads an existing
  Prometheus (self-managed, Amazon Managed Prometheus, Azure Monitor managed
  Prometheus, Google Managed Prometheus) and forwards the samples.

Its RBAC tier (`metrics`, `reader`, `operator`) decides what the backend may
do through the tunnel. See [deploy/agent/README.md](../deploy/agent/README.md)
and the [agent chart](../deploy/helm/kubebolt-agent/README.md).

**Protocol** (`packages/proto`) — the protobuf definition of the agent
channel (`kubebolt.agent.v2`).

## Ways a cluster is connected

| Mode | How KubeBolt reaches it | What you get |
|---|---|---|
| **Kubeconfig / in-cluster** | Directly, with the credentials you give it | Everything your RBAC allows; add the agent for history and flows |
| **Agent proxy** | The agent dials out; API calls ride the tunnel | Same UI as a direct cluster, within the agent's RBAC tier |
| **Metrics-only** | The agent only ships samples | Dashboards and history; no inventory or actions |

## Data flow in one paragraph

On connect, the manager probes permissions, starts the permitted informers
and waits for them to sync. The metrics collector polls metrics-server every
30 s; the agent, remote_write and promread fill VictoriaMetrics. The insights
engine evaluates its rules on each cycle and records episode transitions;
notifications fire on new insights (and, if enabled, on resolved ones). The REST API serves lists
and details from the caches with metrics injected, the WebSocket tells the
browser what changed, and TanStack Query refetches only what it needs.
