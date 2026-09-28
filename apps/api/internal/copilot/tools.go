package copilot

import "strings"

// GovernedToolDefinitions returns ToolDefinitions() filtered by the Sprint 1
// action-governance toggles. When actionsEnabled is false, ALL propose_*
// tools are withheld and Kobi reverts to read-only advisory. When
// destructiveEnabled is false, the destructive verbs (delete) are withheld;
// scale-to-0 can't be tool-filtered (it shares propose_scale_workload) so it
// is blocked server-side instead.
func GovernedToolDefinitions(actionsEnabled, destructiveEnabled bool) []ToolDefinition {
	all := ToolDefinitions()
	if actionsEnabled && destructiveEnabled {
		return all
	}
	out := make([]ToolDefinition, 0, len(all))
	for _, t := range all {
		isPropose := strings.HasPrefix(t.Name, "propose_")
		if !actionsEnabled && isPropose {
			continue
		}
		if !destructiveEnabled && t.Name == "propose_delete_resource" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ToolDefinitions returns the list of tools the copilot exposes to the LLM.
// Each tool maps to a KubeBolt API capability — execution happens server-side
// in the chat handler via the cluster connector.
func ToolDefinitions() []ToolDefinition {
	docTopics := KubebolDocsTopics()
	return []ToolDefinition{
		{
			Name:        "get_cluster_overview",
			Description: "Get cluster summary: resource counts, CPU/memory usage, health score, the most recent Warning events, and per namespace the workloads that need attention (fewer ready replicas than desired, pods not ready) plus those scaled to zero; healthy workloads are counted, not named. Use get_events for the full event stream and list_resources to name every workload.",
			InputSchema: emptyObject(),
		},
		{
			Name:        "list_resources",
			Description: "List Kubernetes resources by type with optional filtering. Each row is a summary for scanning — status, readiness, restarts, node, labels, owner, container images/state/last termination/resources, and only the conditions that are unhealthy; nodes include internalIP and podCIDR. Annotations, volumes, env, ports and probes are left out: use get_resource_detail for one object in full. Types: pods, deployments, statefulsets, daemonsets, replicasets, jobs, cronjobs, services, ingresses, endpoints, networkpolicies, ciliumnetworkpolicies, ciliumclusterwidenetworkpolicies, pdbs, gateways, httproutes, pvcs, pvs, storageclasses, configmaps, secrets, serviceaccounts, roles, clusterroles, rolebindings, clusterrolebindings, hpas, vpas, certificates (cert-manager), argocdapps (Argo CD Applications), nodes, namespaces, events. The optional CRDs (vpas, certificates, argocdapps, Cilium policies) return an empty list when the CRD is not installed OR could not be read, so empty there is not proof that none exist. Cilium policies (cilium.io/v2) carry the L3-L7 rules a standard NetworkPolicy can't; use them to confirm whether a deny rule actually blocks a flow.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strProp("Resource type (e.g. pods, deployments)"),
					"namespace": strProp("Filter by namespace"),
					"search":    strProp("Search by name"),
					"status":    strProp("Filter by status"),
					"sort":      strProp("Sort field"),
					"order":     strPropEnum("Order", []string{"asc", "desc"}),
					"page":      numProp("Page number (default 1)"),
					"limit":     numProp("Page size (default 50)"),
				},
				"required": []string{"type"},
			},
		},
		{
			Name:        "get_resource_detail",
			Description: "Get full details of a specific resource including live metrics",
			InputSchema: nsResourceSchema(),
		},
		{
			Name:        "get_resource_yaml",
			Description: "Get raw YAML of a resource (secrets are redacted automatically)",
			InputSchema: nsResourceSchema(),
		},
		{
			Name: "get_resource_describe",
			// The supported types list mirrors apps/api/internal/api/describe.go's
			// resourceTypeToGroupKind map. When that map gains an entry, this
			// description MUST be updated in lockstep — without the type listed
			// here the model refuses to attempt the call (it builds up an
			// allow-list from prior 400 responses) and misses the diagnostic
			// value entirely. SPEC §3.2.1 has the full coverage roadmap.
			Description: "Get kubectl describe output (events, conditions, detailed status, related resources). " +
				"Supported types: " +
				"workloads (pods, deployments, statefulsets, daemonsets, replicasets, jobs, cronjobs); " +
				"networking (services, ingresses, networkpolicies, endpoints, endpointslices, ingressclasses); " +
				"config (configmaps, secrets, serviceaccounts); " +
				"storage (pvcs, pvs, storageclasses); " +
				"cluster (nodes, namespaces, events); " +
				"RBAC (roles, clusterroles, rolebindings, clusterrolebindings); " +
				"policy/quota (resourcequotas, limitranges, poddisruptionbudgets, priorityclasses, hpas). " +
				"Best tool for troubleshooting scheduling issues, pending pods, quota exhaustion, network policy denials, and resource conditions.",
			InputSchema: nsResourceSchema(),
		},
		{
			Name: "get_pod_logs",
			Description: "Get logs from a pod container. Classify user intent: if the user wants to " +
				"read/view logs verbatim, omit 'grep'. If the user wants to investigate or diagnose a " +
				"problem, failure, or integration issue, pass 'grep' with domain-relevant keywords " +
				"(see system prompt for decision logic). Use 'since' for relative time windows " +
				"('15m', '1h'); use 'sinceTime' + 'endTime' (RFC3339) for absolute windows around past " +
				"incidents. Use 'previous=true' to read logs from the prior container instance after a " +
				"crash/restart — this is the ONLY way to see what happened before the pod recycled. " +
				"Results capped at 500 lines / 48KB, newest preserved; response includes a 'truncated' " +
				"flag when cut. " +
				"MULTI-CONTAINER PODS: if you don't know whether the pod has multiple containers and " +
				"omit `container`, the tool returns just the container list (no logs) and asks you to " +
				"re-call with an explicit container. Use your judgment on the names to pick the right " +
				"one: skip init-style helpers (`certificates`, `configure`, `dependencies`, `wait-for-x`); " +
				"prefer the app container (often matches the deployment name like `webservice`, `api`, " +
				"or the pod-name root); pick a sidecar (`istio-proxy`, `linkerd-proxy`, `vault-agent`, " +
				"`fluentbit`) ONLY when the user's question is about that specific concern (traffic " +
				"policy, secret refresh, log shipping). Picking right on the first try saves a 20-50KB " +
				"round-trip vs hunting through multiple containers' logs.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": strProp("Pod namespace"),
					"name":      strProp("Pod name"),
					"container": strProp("Container name. REQUIRED on multi-container pods — when omitted on such pods, the tool returns only the container list (no logs) to let you pick the right one. Single-container pods auto-resolve."),
					"tailLines": numProp("Lines from end (default 200, max 500)"),
					"since":     strProp("Relative duration window, e.g. '15m', '1h', '2h' (optional; overridden by sinceTime when both set)"),
					"sinceTime": strProp("Absolute lower bound, RFC3339 e.g. '2026-05-10T14:00:00Z' (optional; pair with endTime for a closed window around a past incident)"),
					"endTime":   strProp("Absolute upper bound, RFC3339 e.g. '2026-05-10T16:00:00Z' (optional; requires sinceTime or since)"),
					"previous":  boolProp("Read logs from the previous container instance — use after a restart/crash to see pre-crash state (default false)"),
					"grep":      strProp("Regex/keyword to filter lines, case-insensitive (optional; only when user asks to filter or when investigating incidents)"),
				},
				"required": []string{"namespace", "name"},
			},
		},
		{
			Name:        "get_workload_pods",
			Description: "List pods owned by a workload (deployment, statefulset, daemonset, or job)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strPropEnum("Workload type", []string{"deployments", "statefulsets", "daemonsets", "jobs"}),
					"namespace": strProp("Workload namespace"),
					"name":      strProp("Workload name"),
				},
				"required": []string{"type", "namespace", "name"},
			},
		},
		{
			Name:        "get_workload_history",
			Description: "Get revision history of a workload (Deployment, StatefulSet, or DaemonSet)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strPropEnum("Workload type", []string{"deployments", "statefulsets", "daemonsets"}),
					"namespace": strProp("Workload namespace"),
					"name":      strProp("Workload name"),
				},
				"required": []string{"type", "namespace", "name"},
			},
		},
		{
			Name:        "get_cronjob_jobs",
			Description: "List Job children of a CronJob to investigate execution history",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": strProp("CronJob namespace"),
					"name":      strProp("CronJob name"),
				},
				"required": []string{"namespace", "name"},
			},
		},
		{
			Name:        "get_topology",
			Description: "Get the full cluster topology graph showing relationships between all resources",
			InputSchema: emptyObject(),
		},
		{
			Name:        "get_insights",
			Description: "Get active insights (issues detected by KubeBolt) with severity and recommendations",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"severity": strPropEnum("Severity filter", []string{"critical", "warning", "info"}),
					"resolved": map[string]interface{}{"type": "boolean", "description": "Include resolved insights"},
				},
			},
		},
		{
			Name: "offer_cluster_switch",
			// Deliberately NOT propose_*: that prefix is withheld when actions
			// are disabled, and this mutates nothing — it changes the
			// operator's view.
			Description: "Offer the operator a one-click switch to ANOTHER cluster, carrying the question with it so the new conversation opens already asking it. Use it when you have established something about a cluster that is not the selected one — typically from get_fleet_summary — and answering properly needs tools that only read the current cluster. Pass the question the operator actually asked, not a paraphrase. It REFUSES if the target is not connected, and tells you so: in that case say the cluster is unreachable and report what the fleet view already knows, rather than offering a switch that cannot answer.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cluster":     strProp("Target cluster — its display name, context or id, as it appeared in the fleet summary"),
					"question":    strProp("The question to open the new conversation with, in the operator's own words"),
					"established": strProp("Optional: one line of what is already known from here, e.g. \"the fleet view reports 2 criticals on this cluster\". Carried so the new conversation does not start cold."),
					"rationale":   strProp("Optional: why the switch is needed"),
				},
				"required": []string{"cluster", "question"},
			},
		},

		{
			Name: "get_fleet_summary",
			// The only tool that is not about one cluster. Everything else in
			// the catalogue answers about the selected one.
			Description: "Get active insights broken down PER CLUSTER across the whole organisation, worst first, with the totals. This is the only tool that sees more than the currently selected cluster: list_clusters gives names and connectivity, get_insights answers only about the one in scope. Use it for \"which cluster is worst?\", \"how much is broken overall?\", or any question asked from Home or Fleet rather than from inside one cluster. It reads the persisted store, so clusters that are DOWN are still counted — which is exactly when you want them.",
			InputSchema: emptyObject(),
		},

		{
			Name: "get_findings",
			// Posture first, rows on request. One production org carries 1,224
			// active findings; a list is 245KB against a 32KB cap and answers
			// nothing anyway.
			Description: "Get the cluster's SECURITY findings — CVEs and misconfigurations from Trivy, policy violations from Kyverno, CIS compliance controls. With no filter it returns the POSTURE: counts by severity, source and kind, how many workloads are affected, the worst one, and the container images carrying the most findings — rebuilding one image fixes every workload running it, which a list of workloads never says. Pass severity, kind, source, image or resource to get the matching rows instead, each with its remediation. Use cluster=\"all\" for the whole org. Defaults to ACTIVE findings; pass status=resolved for history. If the tool reports it is unavailable, that is NOT a clean cluster — say you cannot see findings here.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"severity": strPropEnum("Filter by severity", []string{"critical", "high", "medium", "low", "unknown"}),
					"source":   strProp("Filter by scanner, e.g. trivy or kyverno"),
					"kind":     strProp("Filter by finding kind, e.g. vulnerability, misconfiguration, compliance"),
					"image":    strProp("Filter to one container image — the unit an operator actually rebuilds"),
					"resource": strProp("Filter to one workload name"),
					"status":   strPropEnum("Lifecycle state (default: active)", []string{"active", "resolved"}),
					"cluster":  strProp("\"all\" for every cluster in the org (default: the current one)"),
					"limit":    numProp("Max rows when filtering (default 25, max 50)"),
				},
			},
		},
		{
			Name: "get_coverage",
			// First-hour tool: every other answer is only as complete as this.
			Description: "Get what KubeBolt can SEE of the current cluster right now: whether it is connected and how (agent, metrics-only, direct), which agents are connected, which metric sources are shipping (KubeBolt agent, node-exporter, kube-state-metrics, Hubble), which resource types RBAC denies or are not installed, and whether security scanners and Falco are reporting — plus a list of the GAPS that make other tools answer with less. Call it FIRST on a newly connected cluster, whenever a tool returns empty, stale or forbidden and you need to know whether that is the cluster or KubeBolt's view of it, and before telling anyone a cluster is healthy or clean. Missing data is not a healthy cluster.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name: "query_metrics",
			// The escape hatch get_workload_metrics is not.
			Description: "Run a PromQL query against KubeBolt's metrics store, confined to your organization and the CURRENT cluster (any tenant_id / cluster_id matcher you write is replaced by the server). Use it for what get_workload_metrics does not cover: restarts (kube_pod_container_status_restarts_total), PVC fill (kubelet_volume_stats_used_bytes / capacity), object state (kube_* from kube-state-metrics), HTTP rates and latency from Hubble (pod_flow_http_requests_total, pod_flow_http_latency_seconds_*), node load and pressure. Omit range for the value now, or pass range for a trend (min/avg/max/last plus up to 30 points per series). At most 20 series come back, largest last value first — aggregate with sum by (...) or topk to stay under that. Call get_coverage first if you are not sure a source is shipping; an empty result is not a zero.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": strProp("PromQL expression, e.g. sum by (namespace) (kube_pod_container_status_restarts_total)"),
					"range": strPropEnum("Trend window; omit for an instant query", []string{"15m", "1h", "6h", "24h", "7d"}),
				},
				"required": []string{"query"},
			},
		},
		{
			Name: "get_right_sizing",
			// The same engine the Capacity and Cost screens read.
			Description: "Get right-sizing recommendations for the current cluster's workloads — the same deterministic rules and numbers the Capacity and Cost screens show, from each workload's 7-day P95 of CPU and memory against its requests and limits: NEAR-LIMIT (P95 at 80%+ of the limit — raise the limit before it throttles or OOMs; critical), OVER-PROVISIONED (P95 under half the request — lower it; warning) and NO-SPECS (usage without requests or limits; info), each with a suggested value, plus the total CPU and memory reclaimable. Use it for \"are we oversized\", \"what can we save\", \"which workloads are about to hit their limits\". When it reports preliminary (under 2 days of history), say the figures are not yet reliable.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": strProp("Only workloads in this namespace"),
					"severity":  strPropEnum("Only this severity", []string{"critical", "warning", "info"}),
					"limit":     numProp("Max recommendations returned (default 15, max 50)"),
				},
			},
		},
		{
			Name: "get_runtime_events",
			// Falco's feed. Counts first: a noisy rule firing hundreds of times
			// is one fact, and the rows are a sample.
			Description: "Get RUNTIME security events — what Falco saw processes actually do: a shell spawned in a container, a sensitive file read, an unexpected outbound connection, a binary written at runtime. Returns counts by priority and the rules firing most, then the newest events with the pod, the behavior and its fields (command lines are shown with credentials hidden). Use it when the question is \"did something run that shouldn't have\", or to check whether a crash or a config change coincides with suspicious activity. get_findings covers what is VULNERABLE; this covers what HAPPENED. If the tool reports it is unavailable, that is NOT a quiet cluster — say you cannot see runtime events here.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"priority":   strPropEnum("Filter by Falco priority", []string{"Emergency", "Alert", "Critical", "Error", "Warning", "Notice", "Informational", "Debug"}),
					"source":     strProp("Filter by source integration, e.g. falco"),
					"namespace":  strProp("Only events in this namespace"),
					"pod":        strProp("Only events of pods whose name starts with this"),
					"sinceHours": numProp("How far back to look, in hours (default 24, max 720)"),
					"cluster":    strProp("\"all\" for every cluster in the org (default: the current one)"),
					"limit":      numProp("Max events returned (default 10, max 25)"),
				},
			},
		},
		{
			Name: "get_recent_deploys",
			// "What changed" is the first question of almost every diagnosis.
			Description: "Get the Deployment rollouts of the last hours across the cluster, newest first: which workload got a new ReplicaSet, when, and with which image. Use it early when something broke to answer \"what changed right before this\" — a rollout minutes before the first error is the first suspect, and one AFTER it is not the cause. Only Deployment rollouts that created a new ReplicaSet: a rollback to an existing revision, StatefulSets and DaemonSets are not listed, so an empty list is not proof that nothing changed — use get_workload_history for one workload's full story.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"sinceHours": numProp("How far back to look, in hours (default 24, max 168)"),
					"namespace":  strProp("Only rollouts in this namespace"),
					"name":       strProp("Only this Deployment"),
					"limit":      numProp("Max rollouts returned (default 25, max 100)"),
				},
			},
		},
		{
			Name: "get_finding_workloads",
			// The Security page's workload view, same ranking. The unit of work
			// is the workload: forty CVEs on one image are one rebuild.
			Description: "Get the security findings grouped by WORKLOAD, ranked the way to work through them: any workload with an exposed secret first (a leaked credential is already out), then by severity before volume — one critical outranks fifty highs. Each row says how many findings are fixable, what KINDS they are (CVEs to upgrade vs a credential to rotate are different mornings), which image to rebuild and how long it has been open. Also returns the leverage: the images carrying the most findings (one rebuild clears every workload running it), the configuration checks failing in the most places (one manifest pattern), and the compliance benchmarks. Use it for \"where do I start\" or \"which workloads are worst\"; use get_findings for the overall posture or specific rows. Use cluster=\"all\" for the whole org.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"group":    strPropEnum("Security lens: vulnerability (CVEs + exposed secrets), configuration, rbac, compliance", []string{"vulnerability", "configuration", "rbac", "compliance"}),
					"severity": strPropEnum("Count only findings of this severity", []string{"critical", "high", "medium", "low", "unknown"}),
					"kind":     strProp("Count only findings of this kind, e.g. vulnerability, exposed_secret, misconfiguration"),
					"status":   strPropEnum("Lifecycle state (default: active)", []string{"active", "resolved"}),
					"cluster":  strProp("\"all\" for every cluster in the org (default: the current one)"),
					"limit":    numProp("Max workload rows (default 15, max 50)"),
				},
			},
		},
		{
			Name: "get_finding_detail",
			// Re-reads the scanner live: the stored row keeps ONE remediation of
			// possibly seventeen packages, and only the COUNT of resources that
			// fail a CIS control.
			Description: "Get ONE security finding in full, re-read live from the scanner: for a CVE, every package carrying it in each image with installed and fixed versions (the stored row keeps only one), the base OS, the containers and how many pods run the image; for a CIS compliance control, what the control requires and WHICH resources fail it (the stored row keeps only the count). Take the fingerprint from a get_findings row. If the cluster cannot be reached it returns the stored finding with live=false — that is NOT a sign the finding is fixed.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"fingerprint": strProp("The finding's fingerprint, from a get_findings row"),
					"clusterId":   strProp("The row's clusterId when it came from get_findings with cluster=\"all\" (default: the current cluster)"),
				},
				"required": []string{"fingerprint"},
			},
		},
		{
			Name:        "get_insight_episodes",
			Description: "Get the HISTORY of insights — episodes that already resolved, expired or are still firing, with how they ended and how often they came back. get_insights shows only what is wrong RIGHT NOW; this answers \"has this happened before?\", \"how did it resolve last time?\", \"is it flapping?\" and, crucially, \"what happened on the cluster that is down?\" — it reads the stored history and does not need the cluster to be reachable. Use cluster=\"all\" to reach episodes of clusters that are no longer in the selector.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status":     strPropEnum("Lifecycle state", []string{"firing", "resolved", "expired"}),
					"severity":   strPropEnum("Filter on the episode's MAX severity", []string{"critical", "warning", "info"}),
					"rule":       strProp("Filter by rule id, e.g. crash-loop or oom-killed"),
					"cluster":    strProp("Cluster name, or \"all\" for every cluster in the org (default: the current one)"),
					"sinceHours": numProp("How far back to look, in hours (default 24, max 720)"),
					"limit":      numProp("Max episodes (default 25, max 50)"),
				},
			},
		},
		{
			Name: "get_insight_episode",
			// Recurrence rides along: "has this happened before" is the
			// question the episode is usually opened to settle, and making it
			// a second round-trip means the model often does not bother.
			Description: "Get ONE insight episode in full: its append-only timeline (when it opened, when severity moved, if a human silenced it, and WHO did each), the typed evidence the rule recorded when it fired, and its recurrence — the same rule on the same resource over time. Call it when you need to explain a specific episode rather than survey many. Get the id from get_insight_episodes, or from the insight the user opened.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": strProp("The episode id"),
				},
				"required": []string{"id"},
			},
		},
		{
			Name: "get_operational_episodes",
			// The description carries the WHEN, not just the WHAT: a tool the
			// model reaches for after it has already built a theory is a tool
			// that arrives too late to change it.
			Description: "Get operational bursts — groups of insights that fired together in the same window, already classified by their shared cause: node_rotation (nodes were replaced or rebooted), node_pressure (a node ran out of memory/disk), mass_rollout (many workloads redeployed at once), unknown_burst (they correlate in time but the cause is not one of the above). CALL THIS FIRST, before per-workload digging, whenever several resources broke at or near the same time, or the user asks why many things failed at once. A burst names the common cause, so you do not have to infer it from each workload separately — and it tells you when the workload is a victim rather than the culprit.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"sinceHours": numProp("How far back to look, in hours (default 24, max 168)"),
				},
			},
		},
		{
			Name:        "get_events",
			Description: "Get Kubernetes events, optionally filtered by type, namespace, or involved resource",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":         strPropEnum("Event type", []string{"Normal", "Warning"}),
					"namespace":    strProp("Filter by namespace"),
					"involvedKind": strProp("Filter by involved resource kind"),
					"involvedName": strProp("Filter by involved resource name"),
					"limit":        numProp("Max results (default 100)"),
				},
			},
		},
		{
			Name:        "search_resources",
			Description: "Global search across all resource types by name. Use when the user mentions a name without specifying the resource type.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"q": strProp("Search query"),
				},
				"required": []string{"q"},
			},
		},
		{
			Name:        "get_permissions",
			Description: "Get RBAC permissions detected for the current kubeconfig connection",
			InputSchema: emptyObject(),
		},
		{
			Name:        "list_clusters",
			Description: "List all available kubeconfig contexts (clusters)",
			InputSchema: emptyObject(),
		},
		{
			Name: "propose_restart_workload",
			Description: "Propose a rollout restart for a Deployment, StatefulSet, or DaemonSet. " +
				"This DOES NOT execute the restart — it returns a structured proposal that the UI " +
				"renders as a confirmation card. The user must click an explicit button to actually " +
				"trigger the restart, and execution runs under the user's RBAC role (not yours). " +
				"Use this only when a restart is a sensible remediation (crash-loops, stale config, " +
				"OOMKilled with transient cause). Always include a clear rationale.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strPropEnum("Workload type", []string{"deployments", "statefulsets", "daemonsets"}),
					"namespace": strProp("Workload namespace"),
					"name":      strProp("Workload name"),
					"rationale": strProp("Why a restart is the right action here. Shown to the user in the confirmation card."),
					"risk":      riskProp(),
				},
				"required": []string{"type", "namespace", "name", "rationale"},
			},
		},
		{
			Name: "propose_debug_pod",
			Description: "Propose attaching an ephemeral debug container to a running Pod (kubectl debug). " +
				"This DOES NOT execute — it returns a structured proposal the UI renders as a confirmation " +
				"card; the user clicks an explicit button and execution runs under their RBAC role. Use this " +
				"during triage when the target container is distroless / lacks a shell, or you need tools " +
				"(curl, ps, netstat) inside the pod's namespaces. The debug container shares the pod's " +
				"network + (optionally) process namespace. It persists until the pod is recreated — note " +
				"that in the rationale. PREFER passing a non-interactive 'command': the container runs it, " +
				"exits, and its output lands in the container logs so you can read it back with get_pod_logs. " +
				"Without a command the container opens an interactive shell that only a human at the Terminal " +
				"tab can drive — you cannot, and it produces no output you can read.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace":       strProp("Pod namespace"),
					"name":            strProp("Pod name"),
					"image":           strProp("Debug container image (e.g. busybox, nicolaka/netshoot). Defaults to busybox if omitted."),
					"targetContainer": strProp("Optional: the container whose process namespace to share (for inspecting that container's processes)."),
					"command":         strProp("Recommended: a single non-interactive, READ-ONLY diagnostic to run (e.g. \"dig +short shop-db; nc -zv shop-db 5432; echo exit=$?\"). Runs via sh -c, exits, and leaves its output in the container logs for get_pod_logs to read back. Omit only when a human must drive the Terminal interactively. Must never be a mutation."),
					"rationale":       strProp("Why a debug container is the right move here. Shown to the user in the confirmation card."),
					"risk":            riskProp(),
				},
				"required": []string{"namespace", "name", "rationale"},
			},
		},
		{
			Name: "propose_scale_workload",
			Description: "Propose scaling a Deployment or StatefulSet to a target replica count. " +
				"This DOES NOT execute the scale — it returns a structured proposal that the UI " +
				"renders as a confirmation card. The user must click an explicit button to actually " +
				"trigger the scale, and execution runs under the user's RBAC role (not yours). " +
				"Use this when the user asks to scale, when a workload is clearly under/over-provisioned, " +
				"or when scaling to 0 is the right pause action. Always include a rationale.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strPropEnum("Workload type", []string{"deployments", "statefulsets"}),
					"namespace": strProp("Workload namespace"),
					"name":      strProp("Workload name"),
					"replicas":  numProp("Target replica count (>= 0). Use 0 to pause the workload."),
					"rationale": strProp("Why this is the right replica count. Shown to the user in the confirmation card."),
					"risk":      riskProp(),
				},
				"required": []string{"type", "namespace", "name", "replicas", "rationale"},
			},
		},
		{
			Name: "propose_rollback_deployment",
			Description: "Propose rolling back a Deployment to a previous revision (equivalent to " +
				"`kubectl rollout undo`). DOES NOT execute — returns a structured proposal that the UI " +
				"renders as a confirmation card; the user must click Execute to actually trigger the " +
				"rollback, and execution runs under the user's RBAC role (not yours). " +
				"Use this when a recent deploy caused issues (crash-loops, errors after rollout, bad " +
				"image tag) and reverting is the fastest remediation. Always call get_workload_history " +
				"first to confirm the deployment has at least 2 revisions and to identify the right " +
				"target. Pass toRevision when you know the specific target; omit (or pass 0) to roll " +
				"back to the immediately previous revision (the default).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace":  strProp("Deployment namespace"),
					"name":       strProp("Deployment name"),
					"toRevision": numProp("Target revision number; omit or 0 to roll back to the previous revision"),
					"rationale":  strProp("Why a rollback is the right action here. Shown to the user in the confirmation card."),
					"risk":       riskProp(),
				},
				"required": []string{"namespace", "name", "rationale"},
			},
		},
		{
			Name: "propose_delete_resource",
			Description: "Propose deleting a Kubernetes resource (irreversible — there is no rollback). " +
				"DOES NOT execute — returns a structured proposal that the UI renders as a HIGH-RISK " +
				"confirmation card requiring the user to type the resource's namespace/name to confirm. " +
				"Execution runs under the user's Admin role (not yours). " +
				"WHEN to use: ONLY when the user explicitly asks to delete something, OR when a resource " +
				"is clearly orphaned/zombie (e.g. a Deployment whose ReplicaSets are all empty and the " +
				"user has confirmed it's no longer needed). NEVER propose delete as a default remediation " +
				"for crash-loops or errors — restart, scale, or rollback are almost always better. " +
				"WHITELIST: only deployments, statefulsets, daemonsets, services, configmaps, secrets, " +
				"jobs, cronjobs, pods, ingresses, hpas can be deleted via this tool. Namespaces, nodes, PVs, " +
				"PVCs, and RBAC resources are explicitly blocked — recommend kubectl for those. " +
				"The proposal payload includes a computed blast radius (owned pods, affected services, " +
				"orphaned HPAs, etc.) — read it and summarize the consequences in your text response so " +
				"the user understands what they are confirming.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type": strPropEnum("Resource type — restricted whitelist", []string{
						"deployments", "statefulsets", "daemonsets",
						"services", "configmaps", "secrets",
						"jobs", "cronjobs", "pods", "ingresses",
						"hpas",
					}),
					"namespace": strProp("Resource namespace"),
					"name":      strProp("Resource name"),
					"force":     map[string]interface{}{"type": "boolean", "description": "Skip grace period (force=true). Use sparingly — sets gracePeriodSeconds=0."},
					"orphan":    map[string]interface{}{"type": "boolean", "description": "Don't cascade-delete dependents (orphan=true)."},
					"rationale": strProp("Why deletion is the right action AND what consequences the user is accepting. Shown to the user in the confirmation card."),
					"risk":      riskProp(),
				},
				"required": []string{"type", "namespace", "name", "rationale"},
			},
		},
		{
			Name: "propose_set_resources",
			Description: "Propose updating CPU/memory requests and/or limits on a Deployment, StatefulSet, or " +
				"DaemonSet — the equivalent of `kubectl set resources`. DOES NOT execute; returns a proposal " +
				"that the UI renders as a confirmation card. " +
				"Use this for the resource-shape insights: OOMKilled (raise memory limit), CPU throttling " +
				"(raise CPU limit), memory pressure (raise memory), under-request (align requests with " +
				"steady-state usage), frequent restarts where the root cause is resource starvation. " +
				"Restart alone is NOT a fix for OOMKilled or throttling — the same crash repeats after the " +
				"restart. " +
				"ALWAYS call get_resource_detail first to read the current values and the live usage " +
				"snapshot, then propose a delta grounded in what you observed (do NOT guess values). " +
				"Send only the dimensions you want to change; empty/absent fields are left untouched. " +
				"This triggers a rolling update.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strPropEnum("Workload type", []string{"deployments", "statefulsets", "daemonsets"}),
					"namespace": strProp("Workload namespace"),
					"name":      strProp("Workload name"),
					"containers": map[string]interface{}{
						"type":        "array",
						"description": "Per-container resource patches. Only the dimensions you set are touched.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"container":     strProp("Container name on the pod template"),
								"initContainer": boolProp("Set true for an init container; default false"),
								"requests": map[string]interface{}{
									"type":        "object",
									"description": "Resource requests. Omit a dimension to leave it alone.",
									"properties": map[string]interface{}{
										"cpu":    strProp("CPU request (e.g. 100m, 0.5, 2)"),
										"memory": strProp("Memory request (e.g. 128Mi, 1Gi)"),
									},
								},
								"limits": map[string]interface{}{
									"type":        "object",
									"description": "Resource limits. Omit a dimension to leave it alone.",
									"properties": map[string]interface{}{
										"cpu":    strProp("CPU limit (e.g. 500m, 1)"),
										"memory": strProp("Memory limit (e.g. 256Mi, 2Gi)"),
									},
								},
							},
							"required": []string{"container"},
						},
					},
					"rationale": strProp("Why these values are right (cite observed usage / limit, not just symptoms). Shown on the card."),
					"risk":      riskProp(),
				},
				"required": []string{"type", "namespace", "name", "containers", "rationale"},
			},
		},
		{
			Name: "propose_set_image",
			Description: "Propose updating one or more container images on a Deployment, StatefulSet, or " +
				"DaemonSet — the equivalent of `kubectl set image`. DOES NOT execute; returns a proposal " +
				"that the UI renders as a confirmation card. " +
				"Use this for ImagePullBackOff / ErrImagePull when the new tag is known good (operator " +
				"provides it OR you have strong evidence the target tag will pull). " +
				"PREFER propose_rollback_deployment first when the PREVIOUS revision was healthy and the " +
				"current bad tag arrived in a deploy — rollback is strictly safer (reverts the entire pod " +
				"template, not just the image, so any other change in the same deploy reverts with it). " +
				"Only use set_image when rollback is not applicable (no prior good revision, or operator " +
				"asks specifically for a new tag). This triggers a rolling update.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strPropEnum("Workload type", []string{"deployments", "statefulsets", "daemonsets"}),
					"namespace": strProp("Workload namespace"),
					"name":      strProp("Workload name"),
					"images": map[string]interface{}{
						"type":        "array",
						"description": "Container/image pairs to patch.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"container": strProp("Container name on the pod template"),
								"image":     strProp("Full image reference (registry/repo:tag or digest)"),
							},
							"required": []string{"container", "image"},
						},
					},
					"rationale": strProp("Why this image change is the right fix. Shown on the card."),
					"risk":      riskProp(),
				},
				"required": []string{"type", "namespace", "name", "images", "rationale"},
			},
		},
		{
			Name: "propose_set_env",
			Description: "Propose adding/updating/removing environment variables on a Deployment, StatefulSet, " +
				"or DaemonSet — the equivalent of `kubectl set env`. DOES NOT execute; returns a proposal " +
				"that the UI renders as a confirmation card. " +
				"Use this for crash-loops whose root cause is clearly env-config (e.g. logs show `panic: " +
				"DATABASE_URL not set`, `invalid LOG_LEVEL=foo`, `required env BLAH missing`). " +
				"DO NOT use for credential-shaped variables — KubeBolt rejects literal `value` for env " +
				"names matching password|secret|token|key|credential; those changes go through " +
				"ConfigMap/Secret resources in the YAML editor. " +
				"Each env entry has an `action`: \"set\" (add or update; provide `value`) or \"remove\" " +
				"(drop the entry; just provide `name`). Only literal values are supported here — " +
				"`valueFrom` (ConfigMap/Secret/fieldRef) is reserved for the YAML editor. " +
				"This triggers a rolling update.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      strPropEnum("Workload type", []string{"deployments", "statefulsets", "daemonsets"}),
					"namespace": strProp("Workload namespace"),
					"name":      strProp("Workload name"),
					"containers": map[string]interface{}{
						"type":        "array",
						"description": "Per-container env edits.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"container":     strProp("Container name on the pod template"),
								"initContainer": boolProp("Set true for an init container; default false"),
								"env": map[string]interface{}{
									"type":        "array",
									"description": "Env entries to set or remove.",
									"items": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"name":   strProp("Env var name (C_IDENTIFIER: letters, digits, underscore; cannot start with a digit)"),
											"action": strPropEnum("set = add/update; remove = drop the entry", []string{"set", "remove"}),
											"value":  strProp("Literal value (required for set, ignored for remove). Do NOT put credentials here — use Secret refs in the YAML editor."),
										},
										"required": []string{"name", "action"},
									},
								},
							},
							"required": []string{"container", "env"},
						},
					},
					"rationale": strProp("Why this env change is the right fix (cite the error in the logs). Shown on the card."),
					"risk":      riskProp(),
				},
				"required": []string{"type", "namespace", "name", "containers", "rationale"},
			},
		},
		{
			Name: "propose_patch_hpa",
			Description: "Propose updating an HPA's minReplicas and/or maxReplicas bounds. DOES NOT execute; " +
				"returns a proposal that the UI renders as a confirmation card. " +
				"Use this when an HPA is pinned at maxReplicas under sustained pressure (the " +
				"hpaMaxedOutRule insight) AND the operator wants the workload to be able to scale " +
				"higher. Almost always the fix is to RAISE maxReplicas — lowering minReplicas as a " +
				"response to a max-pinned HPA is the wrong direction. " +
				"Server-side cap: maxReplicas must be <= 1000. At least one of minReplicas / " +
				"maxReplicas must be present; sending only one leaves the other untouched. " +
				"This does NOT trigger a rolling update — it only changes scaling math.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace":   strProp("HPA namespace"),
					"name":        strProp("HPA name"),
					"minReplicas": numProp("New minimum replicas (>= 0). Omit to leave unchanged."),
					"maxReplicas": numProp("New maximum replicas (>= 1, <= 1000 safety cap). Omit to leave unchanged."),
					"rationale":   strProp("Why this bound change is right (cite the pressure pattern). Shown on the card."),
					"risk":        riskProp(),
				},
				"required": []string{"namespace", "name", "rationale"},
			},
		},
		{
			Name: "get_workload_metrics",
			Description: "Query CPU, memory, and network metrics for a workload, pod, OR node over a time " +
				"range. Returns a compact summary {min, avg, max, p95} plus a downsampled sparkline " +
				"(~12 points) per requested metric. When CPU or memory is requested, also returns the " +
				"target's current resource bounds and a derived utilizationPercent — for workloads/pods " +
				"that's requests/limits from KSM; for nodes it's allocatable (\"request\") and capacity " +
				"(\"limit\"), so the LLM reads \"%of node capacity\" the same way it reads \"% of pod limit\". " +
				"Use this whenever the question is about behavior OVER TIME (saturation, throttling, " +
				"memory pressure, deploy-correlated changes, sizing, node health, hot-node identification). " +
				"Prefer it over get_resource_detail when the question is \"is X bad over time\" rather " +
				"than \"what is X right now\". " +
				"Before proposing propose_set_resources, ALWAYS call this first — the summary.max and " +
				"utilizationPercent are what justify the patched values in the rationale; a set_resources " +
				"proposal without metric-grounded rationale is a guess. " +
				"For Node kind: namespace is ignored (nodes are cluster-scoped), name is the node name as " +
				"reported by `kubectl get nodes`. Use kind=Node when the operator asks about node-level " +
				"saturation, node memory pressure, or \"which node is doing X\". " +
				"metric=filesystem is NODE-ONLY and returns % of the node's disk used, per real mountpoint " +
				"where node-exporter is present and one coarse series per node otherwise. Use it whenever the " +
				"question involves DiskPressure, evictions for ephemeral-storage, or \"the disk filled up\" — " +
				"that is a node fact, and guessing it from workload memory or from retention settings is how a " +
				"cause gets invented. Requesting it for a pod or workload is refused rather than approximated: " +
				"a pod's disk usage is not the node's. " +
				"Pod-level disk IO is still not exposed (unreliable on EKS with VPC CNI), and PVC fill has its " +
				"own path — the PVC's monitor.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"kind":      strPropEnum("Target kind. Case-sensitive. Use Pod / Deployment / StatefulSet / DaemonSet / Job / CronJob for workloads; Node for the node-level view.", []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Node"}),
					"namespace": strProp("Target namespace. Ignored when kind=Node (nodes are cluster-scoped) — pass any placeholder or omit."),
					"name":      strProp("Target name (workload name, pod name, or node name depending on kind)"),
					"metrics": map[string]interface{}{
						"type":        "array",
						"description": "Which metrics to query. At least one; up to four. Each call is one VM round-trip per metric, so request only what you need.",
						"items": map[string]interface{}{
							"type": "string",
							"enum": []string{"cpu", "memory", "network_rx", "network_tx", "filesystem"},
						},
						"minItems": 1,
						"maxItems": 4,
					},
					"range":        strPropEnum("Time range relative to now. Default 15m if omitted. The response is always ~12 points, so a wider range costs no more tokens — it costs RESOLUTION: each point becomes the peak of a longer step. Pick the smallest window that answers the question, and use the multi-day ranges for trends and sizing (\"is this growing\", \"what should the limit be\") rather than for incidents. If the organization's plan retains less history than requested, the range is narrowed and rangeAdjusted says so.", []string{"5m", "15m", "1h", "6h", "24h", "7d", "14d", "30d"}),
					"perContainer": boolProp("When true, split CPU/memory by container instead of aggregating to the workload. Ignored for network metrics (pod-level only) and for kind=Node (nodes don't have containers). Default false. Use when the operator is asking which container is responsible for a usage pattern."),
				},
				"required": []string{"kind", "namespace", "name", "metrics"},
			},
		},
		{
			Name: "get_kubebolt_docs",
			Description: "Return product documentation about KubeBolt itself (features, navigation, admin " +
				"pages, configuration). Use this ONLY when the user asks how to do something in the KubeBolt " +
				"UI, what a KubeBolt feature does, how to configure something, or how the product works. " +
				"Do NOT use for Kubernetes questions — answer those from your training. Available topics: " +
				strings.Join(docTopics, ", ") + ".",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic": strProp("Topic key from the list in the description. Unknown keys return the full topic list."),
				},
				"required": []string{"topic"},
			},
		},
	}
}

// ----- schema helpers -----

func emptyObject() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func strProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func strPropEnum(desc string, values []string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "string",
		"description": desc,
		"enum":        values,
	}
}

func numProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "number", "description": desc}
}

func boolProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": desc}
}

// riskProp is the shared schema for the risk argument across all proposal
// tools. The LLM picks the risk based on situational context (a rollback to
// a long-tested revision can be "low"; a delete-with-force is "high"); the
// executor falls back to a sensible default per action type when omitted.
// This keeps the card's risk badge consistent with the way the LLM
// describes the action in its accompanying text response.
func riskProp() map[string]interface{} {
	return map[string]interface{}{
		"type": "string",
		"enum": []string{"low", "medium", "high"},
		"description": "Risk level shown as a badge on the confirmation card. " +
			"low = routine, fully reversible, narrow blast radius (e.g. restart of a single workload). " +
			"medium = affects multiple pods or pauses traffic, brief impact window, judgment call. " +
			"high = irreversible or affects critical production paths (e.g. delete, drain). " +
			"Match this with how you describe the action in your text response — don't say 'low risk' in text and pass 'medium' here.",
	}
}

func nsResourceSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":      strProp("Resource type"),
			"namespace": strProp("Namespace (use _ for cluster-scoped resources)"),
			"name":      strProp("Resource name"),
		},
		"required": []string{"type", "namespace", "name"},
	}
}
