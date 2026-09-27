//go:build !ee

package copilot

// kubebolt_docs — the OPEN-SOURCE edition's knowledge base behind
// get_kubebolt_docs. See kubebolt_docs.go for the seam: the Enterprise build
// replaces this file with kubebolt_docs_ee.go.
//
// Rule for this file: describe only what the OSS build ships. Surfaces that
// exist only in KubeBolt Cloud / Enterprise (Autopilot, plans, billing, AI
// credits, multiple teams/organizations, OAuth sign-up, cluster environment
// classification) keep a topic here — the model asks for them, and an
// "Unknown topic" makes it improvise — but the topic says plainly that the
// feature is not part of this edition and points at https://kubebolt.io.
//
// Menu paths must match apps/web/src/components/layout/Sidebar.tsx
// (adminItems) and the hub tabs in apps/web/src/pages/admin/*Hub*.tsx.
// Refreshed 2026-09 for OSS 2.1.
var kubebolt_docs = map[string]string{
	"overview": `KubeBolt (open-source edition) is a Kubernetes operations platform — one place to see, understand and operate your clusters — with Kobi, a bring-your-own-key AI copilot. Connect clusters from your kubeconfig, by importing one, or with the KubeBolt agent (Helm). You get dashboards, 24 rule-based insights with episode history, a live Cluster Map, resource views with logs/terminal/files/actions, Security findings from your own scanners, and Kobi, which investigates and proposes fixes you approve. Autopilot, plans, billing, AI credits and multiple teams/orgs are KubeBolt Cloud features (https://kubebolt.io), not part of this edition.`,

	"add-cluster": `Fleet → "Connect cluster" (or Clusters → "Add cluster"; admin only) offers two paths. Import kubeconfig — KubeBolt dials the apiserver directly; best with a ServiceAccount token and an apiserver reachable from the backend. Install agent — a wizard gives the Helm command; the agent dials back over gRPC and the cluster appears when it registers. Contexts in the kubeconfig KubeBolt started with (or the in-cluster ServiceAccount under Helm) show up automatically. Agent tokens: Administration → Agents & Ingest → Agent Tokens.`,

	"agents": `One Helm chart (kubebolt-agent): a DaemonSet ships node/pod metrics (plus Hubble flows where Cilium+Hubble run — off by default, hubble.enabled=true); an optional promread Deployment reads an existing Prometheus instead. rbac.mode: metrics (telemetry only), reader (default; live resource views through the agent tunnel), operator (exec/restart/scale/delete too; enable agent auth — strongly recommended). Administration → Agents & Ingest: Agent Tokens, Activity (who is sending), Integrations, Configuration (channel auth, rate limits, auto-registration, remote_write, tunnel timeouts).`,

	"plans-billing": `Plans, billing, invoices and plan limits are KubeBolt Cloud features (https://kubebolt.io); they are not available in the open-source edition. OSS has no plans, no invoices and no plan caps: the only limits are the ones the operator sets (ingest rate limits and the active-series cap in Administration → Agents & Ingest → Configuration), and Kobi's cost is whatever your own LLM provider bills for your API key.`,

	"credits": `AI credits are a KubeBolt Cloud concept (https://kubebolt.io) and do not exist in the open-source edition. OSS Kobi runs on your own API key (bring your own key): you pay your LLM provider directly and there is no credit pool or monthly ceiling. Administration → AI (Kobi) → Usage shows sessions, token spend and an estimated cost from public model pricing; the session budget and auto-compact settings live in Administration → AI (Kobi) → Configuration.`,

	"autopilot": `Autopilot — autonomous incident detection, root-cause investigation and guarded remediation — is a KubeBolt Cloud / Enterprise feature (https://kubebolt.io). It is not available in the open-source edition, and there is nothing to enable here. The OSS workflow is human-in-the-loop: an insight card or event → "Ask Kobi" → Kobi investigates with read tools and proposes a fix, which runs only after you approve the proposal card.`,

	"home-fleet": `Org-level pages. Home: strip cards (clusters, pods, monthly spend when OpenCost reports, critical findings), the Attention list (unreachable clusters, missing or silent agents, critical findings), the "While you were away" shift report, and Kobi, Cost and Security panels. Fleet: every cluster at a glance (health, findings, cost, nodes, pods), grid or table, worst first; "Connect cluster" lives here; click a cluster to switch to it. Security: scanner findings across the fleet.`,

	"teams-orgs": `The open-source edition runs a single organization with a single default team: every user is a member and sees every cluster. Roles: admin > editor > viewer. Manage users in Administration → Access → Users; the Teams tab shows the default team. Multiple organizations, multiple teams, cross-team cluster access and team-only users are KubeBolt Cloud / Enterprise features (https://kubebolt.io).`,

	"environments": `Classifying clusters as production / staging / development, and per-environment insight tuning, are KubeBolt Cloud / Enterprise features (https://kubebolt.io); the open-source edition has no environment classification. In OSS, insight rules have one global layer: tune thresholds and severities for all clusters in Administration → Insights → Rules.`,

	"security": `The Security hub (/security) aggregates findings from the cluster's own tooling: Trivy Operator (vulnerabilities, config audit, RBAC assessments, exposed secrets — the secret value is never stored), Kyverno policy reports, CIS benchmarks (ClusterComplianceReport) and Falco runtime events (pushed with a cluster-scoped ingest token). Lenses: Vulnerabilities, Configuration, Permissions, Compliance, Runtime. KubeBolt reads what those tools produce (swept every 10 min); it does not scan by itself.`,

	"navigation": `Org level: Home · Fleet · Security · Administration (Access, Agents & Ingest, AI (Kobi), Insights, System, API Tokens). Inside a cluster: Dashboard (Overview / Capacity, plus Reliability when Hubble flows exist and Cost when OpenCost reports), Cluster Map, then the sidebar groups Pinned (Insights, Applications, Pods, Nodes), Workloads, Traffic, Storage, Config, Extensions, Cluster (Clusters, Namespaces, RBAC, Events); every resource has a tabbed detail page. Keyboard: Cmd+K global search · Cmd+J toggle Kobi.`,

	"cluster-map": `The Cluster Map (/map) renders the topology graph interactively. Three layouts:
- Grid — compact grid of resources grouped by namespace
- Flow — horizontal dependency chain (Ingress/Gateway → HTTPRoute → Service → Workload → Pod)
- Traffic — caller → Service → Pod edges from observed Hubble flows (needs the agent with Hubble enabled)
Filter by resource type and namespace using the top bar. Nodes are draggable; pulse halos highlight unhealthy resources; toggle animations via the control bar. Namespaces are arranged in up to 3 columns.`,

	"resource-detail": `Every resource has a tabbed detail page at /:type/:namespace/:name. Common tabs:
- Overview — labels, annotations, conditions, metrics
- YAML — theme-aware highlighted viewer + editor mode (Editor+ role to save)
- Events — only events that reference this resource
- Monitor — metric gauges and trends (pods, workloads, nodes, PVCs)
Workload tabs: Pods, Logs, Terminal, Files, History, Related.
Pod tabs add Containers and Volumes.
Cluster-scoped resources use _ as namespace placeholder. On agent-connected clusters, live tabs need the agent in reader mode (Terminal and actions: operator).`,

	"pod-terminal": `Pod Terminal tab opens an interactive shell via exec. Auto-detects bash, falls back to sh. Multi-container pods show a container selector. Workload detail pages include a pod selector so you can terminal into any pod of the workload without leaving the page. Requires Editor+ role, and on agent-connected clusters the agent in operator mode.`,

	"pod-files": `The Files tab browses a pod's filesystem via exec (ls / find / cat). Navigate with breadcrumbs, view file content up to 1MB, download files as attachments. Works on distroless containers via a 'find' fallback. Handles permission-denied gracefully.`,

	"port-forward": `Per-pod port buttons appear in the pod detail page. Click to open a TCP forward from the KubeBolt host to that pod port. Active forwards show in the Topbar indicator (green cable icon); click it for a list and stop buttons. Forwards auto-clean on cluster switch. Note: forwards bind on the KubeBolt backend host, so they are only reachable when you run KubeBolt on your own machine.`,

	"resource-actions": `Workload detail pages expose actions (Editor+ role; on agent-connected clusters the agent must be in operator mode): Restart (rollout restart), Scale (replica input), Delete (typed-name confirmation, cascade/force options), plus rollout Set image / Set env / Set resources with a live rollout status panel. Pods add Restart-pod and Evict (PDB-aware). Kobi can propose these same actions in chat — they execute only after you approve the proposal card.`,

	"logs": `Pod Logs: tail 100/500/1000 lines with 10s auto-refresh, container selector for multi-container pods, syntax coloring (errors red, warnings yellow, timestamps blue). Workload detail pages include a pod selector so you can view logs for any pod of the workload. Logs are never persisted — fetched on demand.`,

	"search": `Cmd+K (or Ctrl+K) opens global search: name search across 24 resource types, grouped by kind with icons, keyboard navigation (arrows + enter). Results open the resource detail page. Opened from an org-level page (Home, Fleet, Security) it searches every cluster at once and labels each hit with its cluster.`,

	"insights": `Insights are rule-based diagnostics evaluated against live cluster state. 24 built-in rules: malfunctions (crash loops, OOM kills, image pull backoff, failing probes, node not ready, evictions, failed Helm releases…) and expectations (missing PDBs/NetworkPolicies, resource requests, CPU/memory pressure, expiring certs, HPA at max). Each card has severity, a suggested fix, "Ask Kobi", View episode and Mute. The Insights page has Active and History tabs; rule tuning lives in Administration → Insights.`,

	"admin-insights": `Administration → Insights (admin): Rules — the 24 rules with global overrides; malfunctions keep their severity and only the numeric threshold moves, expectations take a severity (off included). Silenced — every active mute (per cluster, rule and resource; for a time or until resolved), editable by Editor+. History — the org-wide episode record, including clusters that are gone. Per-environment tuning is a KubeBolt Cloud / Enterprise feature.`,

	"copilot": `Kobi is the AI copilot (Cmd+J or the bottom-right icon). It investigates with read tools (resources, metrics, logs, events, topology, docs) and can PROPOSE actions — restart, scale, rollback, set image/env/resources, HPA bounds, debug pod, delete — which run only after you approve the proposal card; Kobi then verifies the result. Admins can turn proposals off (or only destructive ones) in Administration → AI (Kobi) → Configuration. The open-source edition is bring-your-own-key: Anthropic or any OpenAI-compatible endpoint.`,

	"compact": `When the estimated conversation size exceeds the session budget threshold (default 80%), Kobi folds older turns into a summary using the cheap-tier model of the same provider, preserving the last turns intact. The Scissors button in the panel header triggers the same flow on demand (full reset keeping only a summary). Session size shows under the input box.`,

	"copilot-triggers": `Contextual "Ask Kobi" buttons appear across the product: insight cards (diagnose + fix), resource detail headers, warning events, and dashboard panels (top consumers, right-sizing, error hotspots, network drops…). Each pre-loads a prompt with the relevant context and opens the panel.`,

	"admin-users": `Administration → Access has three tabs. Users — create/edit/delete users, assign roles (Admin, Editor, Viewer), reset passwords; self-deletion and last-admin demotion are blocked; password minimum 8 chars. Teams — the single default team (multiple teams are a KubeBolt Cloud / Enterprise feature). Authentication — sign-in state and access/refresh token lifetimes (applied on restart).`,

	"admin-notifications": `Administration → System → Notifications configures Slack, Discord and email (SMTP) channels plus global settings (master toggle, minimum severity, cooldown, resolved-insight alerts, digest mode instant/hourly/daily). Each channel has a "Send test" button. Insights at or above the minimum severity are delivered through the enabled channels; with no channel configured, criticals are detected but not delivered.`,

	"admin-copilot-usage": `Administration → AI (Kobi) → Usage shows Kobi analytics: sessions, token spend (fresh vs cached), estimated cost from public model pricing, tool breakdown and a per-session drill-down. Range selector 24h / 7d / 30d. The numbers are estimates — your LLM provider's bill is authoritative (there are no AI credits in the open-source edition).`,

	"api-tokens": `Administration → API Tokens issues long-lived bearer tokens for non-interactive callers; the plaintext is shown once. API tokens (kbk_) are for your own integrations and CI/CD and also authenticate external MCP hosts to Kobi's read-only tools at /api/v1/mcp. Service tokens (kbs_) are for internal services and are rejected over the public edge. Agent tokens are separate: Administration → Agents & Ingest → Agent Tokens.`,

	"cost": `Cost views need OpenCost: once it reports, the Dashboard gains a Cost tab and Home/Fleet show monthly spend per cluster. The agent scrapes OpenCost's /metrics directly. Installing the bundled OpenCost from the agent wizard needs a Prometheus URL (reused from promread when that is the metrics source).`,

	"theme": `Light/dark theme toggle in the Topbar (sun/moon icon). Persisted in localStorage. All colors bind to CSS variables (--kb-*) so every component follows the theme. YAML viewer + CodeMirror editor switch themes too.`,

	"refresh-interval": `Each resource list has a configurable auto-refresh interval (5s, 10s, 30s, 1m, 2m). Selector lives in the DataFreshnessIndicator (top right of the list). Persisted per-user in localStorage; the default for everyone is set in Administration → System → General. Setting it lower increases load on the API server and on the Kubernetes informers.`,

	"multi-cluster": `All clusters appear in Fleet; switch with the Topbar cluster selector. Agent-connected clusters survive backend restarts (durable registration). A cluster can be metrics-only (agent in metrics mode — dashboards work, resource views don't) or fully connected (kubeconfig, or agent in reader/operator mode). The switch is async: a spinner shows while the new cluster's runtime warms up.`,

	"permissions": `RBAC is probed at connection time via SelfSubjectAccessReview across ~26 resource types. Two phases — cluster-wide first, then namespace-level fallback for namespace-scoped ServiceAccounts. Results drive which resource views are available: restricted resources are dimmed with a shield icon, summary panels show "No access", and endpoints return 403. View the probe at GET /api/v1/cluster/permissions.`,

	"auth": `Authentication is on by default (KUBEBOLT_AUTH_ENABLED=true). On first boot KubeBolt seeds the user "admin" with KUBEBOLT_ADMIN_PASSWORD, or prints a generated password once to the logs; KUBEBOLT_RESET_ADMIN_PASSWORD recovers a lost one. Sign-in is username + password with JWT access tokens and a refresh cookie; roles admin > editor > viewer. KUBEBOLT_AUTH_ENABLED=false lets every request through as admin. Self sign-up, email verification and OAuth are KubeBolt Cloud features (https://kubebolt.io).`,

	"ai-config": `The open-source edition is bring-your-own-key. Configure Kobi in Administration → AI (Kobi) → Configuration (or the first-run setup wizard): provider anthropic or openai (any OpenAI-compatible gateway is openai + base URL), model, API key, optional base URL — the FULL endpoint, used as-is (e.g. https://api.x.ai/v1/chat/completions) — fallback provider, action governance and auto-compact. KUBEBOLT_AI_* env vars are the boot baseline; UI values win. Without an API key the Kobi panel hides. Managed AI with no keys is KubeBolt Cloud.`,

	"distribution": `Editions: open source (this one — Apache-2.0, self-hosted, single organization), KubeBolt Cloud (hosted SaaS) and self-hosted Enterprise; Cloud and Enterprise add Autopilot, multiple teams/orgs and billing (https://kubebolt.io). Open source ships as a single binary (Linux/macOS/Windows), a multi-arch Docker image, a Helm chart (oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt), Homebrew and a krew kubectl plugin; the agent chart and image are public on GHCR.`,

	"clusters-upload": `Two ways to add a cluster besides your startup kubeconfig: import a kubeconfig (Clusters → "Add cluster" → Import kubeconfig — KubeBolt connects directly; imported clusters survive restarts) or install the agent (see add-cluster). Display names are editable on the Clusters page (Rename) either way.`,
}
