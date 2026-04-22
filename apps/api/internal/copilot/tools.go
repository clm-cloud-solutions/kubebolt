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
