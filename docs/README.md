# KubeBolt documentation

The user documentation — quick start, concepts, configuration and API
reference — lives at **<https://kubebolt.io/docs>**. This folder holds the
operator guides, integration recipes and engineering references that travel
with the code, so they are versioned with each release.

## Start here

| | |
|---|---|
| [Project README](../README.md) | What KubeBolt is, quick start, editions |
| [Architecture](architecture.md) | Components, how clusters connect, data flow |
| [Compatibility](COMPATIBILITY.md) | Which agent pairs with which KubeBolt, and the metric schema |
| [Changelog](../CHANGELOG.md) · [release notes](releases/) | What changed in every version |

## Install and run

| | |
|---|---|
| [Helm chart — kubebolt](../deploy/helm/kubebolt/README.md) | Values reference for the backend, web UI and bundled VictoriaMetrics |
| [Helm chart — kubebolt-agent](../deploy/helm/kubebolt-agent/README.md) | Values reference for the agent |
| [Agent guide](../deploy/agent/README.md) | When you need the agent, RBAC tiers, install paths |
| [Deployment scenarios](deployment-scenarios.md) | What to install given what the cluster already runs |
| [Agent scraping](agent-scraping.md) | The optional vmagent sidecar: scrape Prometheus-style endpoints without running Prometheus |
| [Amazon EKS](guides/eks.md) · [Google GKE](guides/gke.md) · [Azure AKS](guides/aks.md) · [OpenShift](guides/openshift.md) | Platform guides |

## Kobi

| | |
|---|---|
| [Kobi Copilot](guides/copilot.md) | Enabling Kobi, configuration, actions and guardrails |
| [AI providers](guides/copilot-providers.md) | Anthropic and OpenAI-compatible providers, models, self-hosted LLMs |
| [MCP server](guides/kobi-mcp.md) | Kobi's read-only tools in Claude Code, Cursor and other MCP clients |

## Integrations

| | |
|---|---|
| [Prometheus remote_write](integrations/prometheus.md) | Send samples from an existing Prometheus to KubeBolt |
| [Self-managed Prometheus (read-only)](integrations/self-managed-prom-readonly.md) | Let the agent read your Prometheus instead |
| [Amazon Managed Prometheus](integrations/aws-amp.md) · [Azure Monitor managed Prometheus](integrations/azure-managed-prometheus.md) · [Google Managed Prometheus](integrations/gcp-managed-prometheus.md) | Managed Prometheus on each cloud |
| [OpenCost](integrations/opencost.md) | Cost dashboard and savings |
| [Trivy Operator](integrations/trivy.md) · [Kyverno](integrations/kyverno.md) · [Falco](integrations/falco.md) | Security & Compliance sources |

## Try it

| | |
|---|---|
| [Incident simulations](incident-simulations/README.md) | Break things on purpose: scenarios that trigger insights and give Kobi something to investigate, plus a three-tier demo shop |

## Engineering records

Kept for history and because code comments cite them. They are not
maintained as user documentation.

| | |
|---|---|
| [SPEC.md](SPEC.md) | The original technical specification and phase roadmap |
| [Insight rule lifecycle audit](06-insights-rule-lifecycle-audit.md) | How rules re-fire (resolved) |
| [Agent proxy design](architecture/sprint-a5-agent-proxy.md) | The gRPC tunnel design (Spanish) |
| [Archive](archive/README.md) | Earlier design notes, prototypes and studies |
