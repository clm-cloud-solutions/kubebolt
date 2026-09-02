package copilot

import (
	"fmt"
	"sort"
	"strings"
)

// kubebolt_docs is a terse product knowledge base the copilot can query via
// the get_kubebolt_docs tool. Entries are hand-curated — short enough to
// burn few tokens when returned, specific enough to actually answer the
// user. Prefer action-oriented wording ("click X", "press Cmd+Y") over
// marketing.
//
// When adding topics: keep each entry under ~500 chars, lowercase keys,
// kebab-case multi-word keys. New topics are picked up automatically by
// the get_kubebolt_docs tool (topic list is derived from this map).
//
// Refreshed 2026-08-31 for the EE/Cloud surface (V2.0.x-E2): the previous
// content was frozen at OSS 1.0 (12 rules, kubeconfig-upload as THE way to
// add clusters, read-only copilot) and Kobi was confidently describing a
// product that no longer exists. Never hardcode prices here — they drift;
// point at Billing → Plans instead.
var kubebolt_docs = map[string]string{
	"overview": `KubeBolt is instant Kubernetes monitoring plus AI operations. Connect a cluster in minutes by installing the KubeBolt agent (Helm); get dashboards, 24 rule-based insights, a live Cluster Map, Kobi (AI copilot that investigates and can propose fixes) and Autopilot (beta, autonomous investigation). Org level: Home, Fleet, Security. Editions: KubeBolt Cloud (SaaS), self-hosted Enterprise, and open source.`,

	"add-cluster": `To connect a cluster: Fleet (or Clusters page) → "+ Connect cluster". The wizard gives you a Helm command that installs the KubeBolt agent with your org's ingest token — run it against the target cluster and the cluster appears when the agent registers (usually under 2 minutes). The agent's RBAC mode decides depth: metrics (dashboards only), reader (live resource views), operator (actions too). Self-hosted installs can also upload a kubeconfig for a direct connection.`,

	"agents": `Two agent workloads, one Helm chart: a DaemonSet ships node/pod metrics (and Hubble network flows where Cilium+Hubble are enabled — Hubble is OFF by default, enable with hubble.enabled=true); an optional promread Deployment reads an existing Prometheus (AMP/Azure Monitor/GMP) instead. RBAC modes: metrics (telemetry only), reader (live resource views via the agent tunnel), operator (restart/scale/delete actions too). The agent is the only path KubeBolt uses to reach your API server in Cloud.`,

	"plans-billing": `Plans (per-org): Free — 2 clusters, 10 nodes, 150 pods, 25k active metric series, 3 users, 1 team, 15-day retention, monthly AI credits; limits are HARD (at the cap, ingestion truncates). Paid plans (Team and up) raise users/series/retention/credits and their caps are SOFT — crossing one signals, never blocks. Current pricing, upgrade and invoices: Administration → Billing → Plan & usage. Currency (EUR/USD) is chosen at first checkout; 30-day money-back guarantee.`,

	"credits": `AI credits are the single unit for all AI usage: Kobi chats and Autopilot runs draw from one monthly pool set by your plan (plus any bonus). Track usage in Administration → Billing → Plan & usage — an amber banner warns when the pool runs low; at the ceiling AI pauses until the pool renews on the 1st. Admins can set their own spend limit below the plan ceiling.`,

	"autopilot": `Autopilot (beta) investigates incidents autonomously: it watches insights, opens an incident, runs a root-cause investigation and — depending on mode — proposes or applies a fix. Modes: suggest_only (diagnose + recommend), approve_and_execute (act after human approval), autonomous (act within guardrails: blocked namespaces, approval required for destructive ops, auto-rollback). Enable per-org in Administration → AI & Autopilot. Runs consume AI credits; each incident keeps a full timeline and can export a postmortem.`,

	"home-fleet": `Org-level surfaces: Home — the arrival page (KPIs across clusters, "Needs your attention", plan usage strip). Fleet — every connected cluster with live status; "+ Connect cluster" lives here; click a cluster to enter its dashboards. Security — aggregated security findings across the fleet. Administration hubs: Access (users, teams), Agents & Ingest, AI & Autopilot, Billing, System.`,

	"teams-orgs": `An organization contains teams; clusters are assigned to a team (Fleet → cluster → team) and users see the clusters of their teams. Roles: admin > editor > viewer per org; platform admin is the operator-level role above orgs. Manage users and teams in Administration → Access.`,

	"environments": `A cluster can be classified production / staging / development (Administration → Clusters, admin only). The classification is a billing-relevant attribute — it will drive differentiated node-hour rates and per-environment insight tuning — so it is a closed set and read-only everywhere else. An unclassified cluster shows a "classify this cluster" prompt.`,

	"security": `The Security hub aggregates findings from the cluster's own security tooling — vulnerability scans (Trivy operator), policy reports (Kyverno), runtime events (Falco) — normalized into one list with severity and source, plus a per-source reporting status ("3 of 4 sources reporting"). KubeBolt pulls what the tools already produce; it does not scan by itself.`,

	"navigation": `Org level: Home · Fleet · Security · Administration (Access, Agents & Ingest, AI & Autopilot, Billing, System). Inside a cluster: Dashboard (Overview / Capacity / Reliability sub-tabs — Reliability appears when Hubble flows exist), Insights, Autopilot, Cluster Map, and one list per resource kind with tabbed detail pages. Keyboard: Cmd+K global search · Cmd+J toggle Kobi.`,

	"cluster-map": `The Cluster Map (/map) renders the topology graph interactively. Two layouts:
- Grid — compact grid of resources grouped by namespace
- Flow — horizontal dependency chain (Ingress/Gateway → HTTPRoute → Service → Workload → Pod)
Filter by resource type and namespace using the top bar. Nodes are draggable; pulse halos highlight unhealthy resources; toggle animations via the control bar. Namespaces are arranged in up to 3 columns.`,

	"resource-detail": `Every resource has a tabbed detail page at /:type/:namespace/:name. Common tabs:
- Overview — labels, annotations, conditions, metrics
- YAML — theme-aware highlighted viewer + editor mode (Editor+ role to save)
- Events — only events that reference this resource
- Monitor — metric gauges and trends
Workload-only tabs: Pods, Logs, Terminal, Files, History, Related.
Pod-only tabs add Containers, Volumes, Files, Terminal.
Cluster-scoped resources use _ as namespace placeholder. Live tabs (Logs/Terminal/Files/actions) need the agent in reader/operator mode.`,

	"pod-terminal": `Pod Terminal tab opens an interactive shell via exec. Auto-detects bash, falls back to sh. Multi-container pods show a container selector. Workload detail pages include a pod selector so you can terminal into any pod of the workload without leaving the page. Requires Editor+ role and an agent in operator mode (or a direct connection).`,

	"pod-files": `The Files tab browses a pod's filesystem via exec (ls / find / cat). Navigate with breadcrumbs, view file content up to 1MB, download files as attachments. Works on distroless containers via a 'find' fallback. Handles permission-denied gracefully.`,

	"port-forward": `Per-pod port buttons appear in the pod detail page. Click to open a TCP forward from the KubeBolt host to that pod port. Active forwards show in the Topbar indicator (green cable icon); click it for a list and stop buttons. Forwards auto-clean on cluster switch. Note: forwards bind on the KubeBolt backend host, so they are only reachable when you run KubeBolt on your own machine.`,

	"resource-actions": `Workload detail pages expose actions (Editor+ role, agent in operator mode): Restart (rollout restart), Scale (replica input), Delete (typed-name confirmation, cascade/force options), plus rollout Set image / Set env / Set resources with a live rollout status panel. Pods add Restart-pod and Evict (PDB-aware). Kobi can propose these same actions in chat — they execute only after you approve the proposal card.`,

	"logs": `Pod Logs: tail 100/500/1000 lines with 10s auto-refresh, container selector for multi-container pods, syntax coloring (errors red, warnings yellow, timestamps blue). Workload detail pages include a pod selector so you can view logs for any pod of the workload. Logs are never persisted — fetched on demand.`,

	"search": `Cmd+K (or Ctrl+K) opens global search. Debounced search across 16 resource types by name, grouped by kind with icons, keyboard navigation (arrows + enter). Results open the resource detail page. Useful when you know the name but not the type.`,

	"insights": `Insights are rule-based diagnostics evaluated against live cluster state. 24 built-in rules cover malfunctions (crash loops, OOM kills, image pull backoff, zero replicas, failing probes, node not ready, evictions, failed Helm releases…) and policy expectations (missing PDBs/NetworkPolicies, resource under-requests, CPU/memory pressure, expiring certs, HPA at max). Severity critical/warning/info with a suggested fix; each card has an "Ask Kobi" button, and insights feed Autopilot's incident detection.`,

	"copilot": `Kobi is the AI copilot (Cmd+J or the bottom-right icon). It investigates using read tools (resources, metrics, logs, events, topology) and can PROPOSE actions — restart, scale, set image/env/resources, rollback, delete — which run only after you approve the proposal card, then Kobi verifies the result. Admins can disable action proposals (or just destructive ones) in governance settings. In KubeBolt Cloud, Kobi runs on your plan's AI credits; self-hosted/OSS brings its own API key (Anthropic or OpenAI).`,

	"compact": `When the estimated conversation size exceeds the session budget threshold (default 80%), Kobi folds older turns into a summary using the cheap-tier model of the same provider, preserving the last turns intact. The Scissors button in the panel header triggers the same flow on demand (full reset keeping only a summary). Session size shows under the input box.`,

	"copilot-triggers": `Contextual "Ask Kobi" buttons appear across the product: insight cards (diagnose + fix), resource detail headers, warning events, and dashboard panels (top consumers, right-sizing, error hotspots, network drops…). Each pre-loads a prompt with the relevant context and opens the panel.`,

	"admin-users": `Administration → Access manages Users and Teams: create/edit/delete users, assign roles (Admin, Editor, Viewer), reset passwords, and organize teams (clusters are assigned to teams). Self-deletion and last-admin demotion are blocked. Password minimum: 8 chars.`,

	"admin-notifications": `Administration → System → Notifications configures Slack, Discord and email channels plus global settings (master toggle, minimum severity, cooldown, resolved-insight alerts, email digest modes). Insights at or above the minimum severity are delivered through the enabled channels; without any configured route, criticals are detected but not delivered — the banner says so.`,

	"admin-copilot-usage": `Administration → AI & Autopilot → Usage & Credits shows AI analytics: sessions, token spend (fresh vs cached), credit consumption by Kobi vs Autopilot, tool breakdown, per-session drill-down. Range selector 24h / 7d / 30d. Credit totals here are what Billing → Plan & usage charges against.`,

	"theme": `Light/dark theme toggle in the Topbar (sun/moon icon). Persisted in localStorage. All colors bind to CSS variables (--kb-*) so every component follows the theme. YAML viewer + CodeMirror editor switch themes too.`,

	"refresh-interval": `Each resource list has a configurable auto-refresh interval (5s, 10s, 30s, 1m, 2m). Selector lives in the DataFreshnessIndicator (top right of the list). Persisted per-user in localStorage. Setting it lower increases load on the API server and on the Kubernetes informers.`,

	"multi-cluster": `All of your org's clusters live in Fleet; switch with the Topbar cluster selector. Agent-connected clusters survive backend restarts (durable registration). A cluster can be metrics-only (agent in metrics mode — dashboards work, resource views don't) or fully connected (reader/operator mode). The switch is async: a spinner shows while the new cluster's runtime warms up.`,

	"permissions": `RBAC is probed at connection time via SelfSubjectAccessReview across ~26 resource types. Two phases — cluster-wide first, then namespace-level fallback for namespace-scoped ServiceAccounts. Results drive which resource views are available: restricted resources are dimmed with a shield icon, summary panels show "No access", and endpoints return 403. View the probe at GET /api/v1/cluster/permissions.`,

	"auth": `KubeBolt Cloud: sign up with email (verification required before AI features) or OAuth; JWT access tokens with refresh cookie; roles admin > editor > viewer per org. Self-hosted/OSS: enable with KUBEBOLT_AUTH_ENABLED=true — the admin user is auto-seeded on first boot; when disabled, all routes pass through as admin.`,

	"ai-config": `In KubeBolt Cloud the AI is managed — no keys to configure; usage draws from plan credits. Self-hosted/OSS is BYOK via env vars: KUBEBOLT_AI_PROVIDER (anthropic|openai), KUBEBOLT_AI_API_KEY, KUBEBOLT_AI_MODEL, KUBEBOLT_AI_FALLBACK_* (auto-retry on rate limits/5xx), and the KUBEBOLT_AI_AUTO_COMPACT / SESSION_BUDGET memory-management family. Without a key the panel hides.`,

	"distribution": `KubeBolt Cloud is the hosted SaaS — sign up, connect clusters with the agent. Self-hosted Enterprise ships by annual license. Open source ships as a single binary (Linux/macOS/Windows), multi-arch Docker image, Helm chart (oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt), Homebrew and a krew kubectl plugin; the agent chart and image are public on GHCR.`,

	"clusters-upload": `Adding clusters is agent-first: Fleet → "+ Connect cluster" gives you the Helm command that installs the KubeBolt agent with your ingest token (see the add-cluster topic). Self-hosted installs can additionally upload a kubeconfig on the Clusters page for a direct connection; uploaded clusters survive restarts, and display names are editable either way.`,
}

// kubeboltDocsAliases maps the topic names the LLM plausibly GUESSES to the
// real keys. Aliases are invisible in the topic list — the canonical names
// live there — but they catch pattern-following guesses: in vivo the model
// asked for "admin-billing" (extrapolating the admin-* family) and got
// "Unknown topic", then improvised an answer about plans.
var kubeboltDocsAliases = map[string]string{
	"admin-billing":   "plans-billing",
	"billing-plans":   "plans-billing",
	"pricing":         "plans-billing",
	"subscription":    "plans-billing",
	"connect-cluster": "add-cluster",
	"cluster-connect": "add-cluster",
	"onboarding":      "add-cluster",
	"kobi":            "copilot",
	"admin-access":    "admin-users",
}

// KubebolDocsTopics returns the list of known topic keys for the tool
// description — lets the LLM discover available topics without a round-trip.
func KubebolDocsTopics() []string {
	topics := make([]string, 0, len(kubebolt_docs))
	for k := range kubebolt_docs {
		topics = append(topics, k)
	}
	sort.Strings(topics)
	return topics
}

// KubebolDocsGet returns the doc for a topic. When the topic is unknown,
// returns a short message plus the list of valid topics so the LLM can
// retry. Fuzzy matching is intentionally forgiving — fold case, normalize
// whitespace and underscores, and try prefix matches. Keeps the tool
// useful even when the LLM guesses a slightly-off key.
func KubebolDocsGet(topic string) string {
	key := normalizeDocKey(topic)
	if key == "" {
		return kubebolDocsUnknown("", KubebolDocsTopics())
	}
	if doc, ok := kubebolt_docs[key]; ok {
		return doc
	}
	if canonical, ok := kubeboltDocsAliases[key]; ok {
		return kubebolt_docs[canonical]
	}
	// Prefix fallback
	for k, doc := range kubebolt_docs {
		if strings.HasPrefix(k, key) || strings.HasPrefix(key, k) {
			return doc
		}
	}
	// Substring fallback
	for k, doc := range kubebolt_docs {
		if strings.Contains(k, key) {
			return doc
		}
	}
	return kubebolDocsUnknown(topic, KubebolDocsTopics())
}

func normalizeDocKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

func kubebolDocsUnknown(topic string, topics []string) string {
	prefix := "Unknown topic"
	if topic != "" {
		prefix = fmt.Sprintf("Unknown topic %q", topic)
	}
	return fmt.Sprintf("%s. Available topics: %s", prefix, strings.Join(topics, ", "))
}
