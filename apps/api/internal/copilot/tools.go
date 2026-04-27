package copilot

import "strings"

// ToolDefinitions returns the list of tools the copilot exposes to the LLM.
// Each tool maps to a KubeBolt API capability — execution happens server-side
// in the chat handler via the cluster connector.
func ToolDefinitions() []ToolDefinition {
	docTopics := KubebolDocsTopics()
	return []ToolDefinition{
		{
			Name:        "get_cluster_overview",
			Description: "Get cluster summary: resource counts, CPU/memory usage, health score, recent events, namespace workloads",
			InputSchema: emptyObject(),
		},
		{
			Name:        "list_resources",
			Description: "List Kubernetes resources by type with optional filtering. Types: pods, deployments, statefulsets, daemonsets, jobs, cronjobs, services, ingresses, gateways, httproutes, endpoints, pvcs, pvs, storageclasses, configmaps, secrets, hpas, nodes, namespaces, events",
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
			Name:        "get_resource_describe",
			Description: "Get kubectl describe output for any resource. Includes events, conditions, and detailed status. Best tool for troubleshooting scheduling issues, pending pods, and resource conditions.",
			InputSchema: nsResourceSchema(),
		},
		{
			Name: "get_pod_logs",
			Description: "Get logs from a pod container. Classify user intent: if the user wants to " +
				"read/view logs verbatim, omit 'grep'. If the user wants to investigate or diagnose a " +
				"problem, failure, or integration issue, pass 'grep' with domain-relevant keywords " +
				"(see system prompt for decision logic). Use 'since' for time-windowed queries. " +
				"Results capped at 500 lines / 48KB, newest preserved; response includes a 'truncated' " +
				"flag when cut.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": strProp("Pod namespace"),
					"name":      strProp("Pod name"),
					"container": strProp("Container name (required for multi-container pods)"),
					"tailLines": numProp("Lines from end (default 200, max 500)"),
					"since":     strProp("Duration window, e.g. '15m', '1h', '2h' (optional; combine with tailLines)"),
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
				"jobs, cronjobs, pods, ingresses can be deleted via this tool. Namespaces, nodes, PVs, " +
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
