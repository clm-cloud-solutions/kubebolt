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
var kubebolt_docs = map[string]string{
	"overview": `KubeBolt is a zero-config Kubernetes monitoring and management dashboard. It auto-detects clusters from kubeconfig (or in-cluster ServiceAccount), introspects resources through shared informers, and surfaces insights, metrics, topology, and pod-level operations (logs, terminal, port-forward, file browser) in a web UI. BYO kubeconfig or deploy via Helm/Docker/binary — no agent, no persistence by default.`,

	"navigation": `Main surfaces:
- Overview (/) — cluster summary, health, recent insights, workload cards
- Cluster Map (/map) — visual topology graph (Grid or Flow layout)
- Resource lists — one route per kind (/pods, /deployments, /services, ...)
- Resource Detail — tabbed pages at /:type/:namespace/:name
- Insights (/insights) — rule-based diagnostics with Ask Copilot buttons
- Admin (admin-only) — Users, Notifications, Copilot Usage analytics
Keyboard: Cmd+K global search · Cmd+J toggle Copilot.`,

	"cluster-map": `The Cluster Map (/map) renders the topology graph interactively. Two layouts:
- Grid — compact grid of resources grouped by namespace
- Flow — horizontal dependency chain (Ingress/Gateway → HTTPRoute → Service → Workload → Pod)
Filter by resource type and namespace using the top bar. Nodes are draggable; pulse halos highlight unhealthy resources; toggle animations via the control bar. Namespaces are arranged in up to 3 columns.`,

	"resource-detail": `Every resource has a tabbed detail page at /:type/:namespace/:name. Common tabs:
- Overview — labels, annotations, conditions, metrics
- YAML — theme-aware highlighted viewer + editor mode (Editor+ role to save)
- Events — only events that reference this resource
- Monitor — SVG donut gauges from Metrics Server
Workload-only tabs: Pods, Logs, Terminal, Files, History, Related.
Pod-only tabs add Containers, Volumes, Files, Terminal.
Cluster-scoped resources use _ as namespace placeholder.`,

	"pod-terminal": `Pod Terminal tab opens an interactive shell via SPDY exec. Auto-detects bash, falls back to sh. Multi-container pods show a container selector. Workload detail pages include a pod selector so you can terminal into any pod of the workload without leaving the page. Uses xterm.js. Requires Editor+ role.`,

	"pod-files": `The Files tab browses a pod's filesystem via exec (ls / find / cat). Navigate with breadcrumbs, view file content up to 1MB, download files as attachments. Works on distroless containers via a 'find' fallback. Handles permission-denied gracefully.`,

	"port-forward": `Per-pod port buttons appear in the pod detail page. Click to open a TCP forward from the KubeBolt host to that pod port. Active forwards show in the Topbar indicator (green cable icon); click it for a list and stop buttons. Forwards auto-clean on cluster switch.`,

	"resource-actions": `Workload detail pages expose three actions (Editor+ role):
- Restart — rollout restart via annotation patch (Deployments, StatefulSets, DaemonSets)
- Scale — popover with replica count input (Deployments, StatefulSets)
- Delete — confirmation modal with typed name; options for cascade and force.
Actions hit the API which uses the cluster dynamic client.`,

	"logs": `Pod Logs: tail 100/500/1000 lines with 10s auto-refresh, container selector for multi-container pods, syntax coloring (errors red, warnings yellow, timestamps blue). Workload detail pages include a pod selector so you can view logs for any pod of the workload. Logs are never persisted — fetched on demand.`,

	"search": `Cmd+K (or Ctrl+K) opens global search. Debounced search across 16 resource types by name, grouped by kind with icons, keyboard navigation (arrows + enter). Results open the resource detail page. Useful when you know the name but not the type.`,

	"insights": `Insights are rule-based diagnostics evaluated by the backend engine against live cluster state. 12 built-in rules cover crash loops, OOMs, CPU throttling, memory pressure, pending pods, node issues, misconfiguration. Each insight has severity (critical/warning/info) and a suggested fix. Shown in the Overview cards and the /insights page; each has an "Ask Copilot" button that launches a pre-filled diagnostic prompt.`,

	"copilot": `The Copilot is an AI chat panel (Cmd+J or the bottom-right icon) that can fetch cluster data via tools. It only reads — never modifies. Uses your KUBEBOLT_AI_API_KEY (BYOK) with Anthropic or OpenAI. Features:
- Multi-step tool loop (up to 10 rounds per question)
- Contextual "Ask Copilot" buttons in insights, resource detail pages, events, services, nodes
- Auto-compact when conversation approaches context budget
- Manual "new session with summary" button (scissors icon) to reset keeping context`,

	"compact": `When the estimated conversation size exceeds KUBEBOLT_AI_SESSION_BUDGET_TOKENS × KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD (default 80%), the Copilot folds older turns into a summary using the cheap-tier model of the same provider. Preserves the last N turns intact (KUBEBOLT_AI_COMPACT_PRESERVE_TURNS, default 3). The Scissors button in the panel header triggers the same flow on demand with resetAll=true (full reset keeping only a summary).`,

	"copilot-triggers": `Contextual "Ask Copilot" buttons appear at these surfaces:
- Insights — diagnose and suggest a fix
- Resource detail (Pods, Deployments, StatefulSets, Services, Nodes) in header — investigate the resource
- Events page — warning events get a button per row
Each button pre-loads a template prompt with the relevant context (cluster, namespace, name, symptom) and launches the panel. Prompts are versioned in services/copilot/triggers.ts.`,

	"admin-users": `Admin → Users (/admin/users) lets admins create / edit / delete users, assign roles (Admin, Editor, Viewer), and reset passwords. Self-deletion and last-admin demotion are blocked. Password minimum: 8 chars. Passwords hashed with bcrypt cost 12. Enabled only when KUBEBOLT_AUTH_ENABLED is set.`,

	"admin-notifications": `Admin → Notifications (/admin/notifications) configures Slack, Discord and email channels plus global settings (master toggle, base URL for links, resolved-insight alerts). Each channel enabled when its webhook/SMTP credentials are set. Insights at or above KUBEBOLT_NOTIFICATIONS_MIN_SEVERITY trigger notifications; cooldown prevents re-alerting. Email supports digest modes (off / daily / hourly).`,

	"admin-copilot-usage": `Admin → Copilot Usage (/admin/copilot-usage) shows analytics for all Copilot sessions: counts, token spend (fresh vs cached), estimated USD cost, tool breakdown, per-session drill-down with compact events. Range selector 24h / 7d / 30d. Data persists in BoltDB for 30 days or 5000 entries (whichever first). Pricing is list-price based, approximate. Requires auth enabled.`,

	"theme": `Light/dark theme toggle in the Topbar (sun/moon icon). Persisted in localStorage. All colors bind to CSS variables (--kb-*) so every component follows the theme. YAML viewer + CodeMirror editor switch themes too.`,

	"refresh-interval": `Each resource list has a configurable auto-refresh interval (5s, 10s, 30s, 1m, 2m). Selector lives in the DataFreshnessIndicator (top right of the list). Persisted per-user in localStorage. Setting it lower increases load on the API server and on the Kubernetes informers.`,

	"multi-cluster": `KubeBolt reads all contexts from your kubeconfig(s) plus any uploaded via the Clusters page, plus in-cluster ServiceAccount when deployed in a cluster. Switch with the Topbar cluster selector. The switch is async: informers spin up for the new cluster's allowed resources, metrics collector restarts, topology rebuilds. Copilot transcript clears on successful switch.`,

	"permissions": `RBAC is probed at connection time via SelfSubjectAccessReview: 22 resource types × list verb. Two phases — cluster-wide first, then namespace-level fallback for namespace-scoped ServiceAccounts. Results drive informer startup (only permitted resources), Sidebar icons (restricted resources dimmed with shield), summary panels ("No access"), and 403 responses from resource endpoints. View the probe at GET /api/v1/cluster/permissions.`,

	"auth": `Authentication is optional. Enable with KUBEBOLT_AUTH_ENABLED=true. Admin user is auto-seeded on first boot (password printed to stderr or set via KUBEBOLT_AUTH_INITIAL_ADMIN_PASSWORD). JWT access tokens (15m default), httpOnly refresh cookie. Roles: admin > editor > viewer. When disabled, all routes pass through as admin.`,

	"ai-config": `The Copilot uses your API key (BYOK). Env vars:
- KUBEBOLT_AI_PROVIDER (anthropic | openai)
- KUBEBOLT_AI_API_KEY, KUBEBOLT_AI_MODEL, KUBEBOLT_AI_BASE_URL
- KUBEBOLT_AI_MAX_TOKENS (default 4096)
- KUBEBOLT_AI_FALLBACK_* for auto-retry on rate limits / 5xx
- KUBEBOLT_AI_AUTO_COMPACT, KUBEBOLT_AI_SESSION_BUDGET_TOKENS, KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD, KUBEBOLT_AI_COMPACT_MODEL, KUBEBOLT_AI_COMPACT_PRESERVE_TURNS for memory management.
Enabled only when an API key is set; otherwise the panel hides.`,

	"distribution": `KubeBolt ships as:
- Single binary (Linux/macOS/Windows, amd64/arm64) with embedded frontend
- Docker image (multi-arch) — docker run with kubeconfig mount
- Helm chart — oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt
- Homebrew — brew install clm-cloud-solutions/tap/kubebolt
- kubectl plugin via krew (kubectl kubebolt)
In-cluster mode auto-detects the ServiceAccount token; kubeconfig mode reads ~/.kube/config or $KUBECONFIG. All releases signed with cosign.`,

	"clusters-upload": `Admins can add clusters at runtime from the Clusters page (/clusters): upload a kubeconfig file (stored encrypted in BoltDB), edit display names, or remove clusters. Uploaded clusters survive restarts. Per-cluster connection status shown as badges.`,
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
