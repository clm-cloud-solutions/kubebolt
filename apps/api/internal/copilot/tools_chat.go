package copilot

// chatDescriptions are the chat's wording of read-only tools the MCP
// catalogue also serves. Kobi's prompt and skills already carry how to use
// them, so its copy says what the tool returns and the one rule that must hold
// at the call; Autopilot's agents read the full text, which has none of that
// around it. A key here must name a tool in ToolDefinitions (tested).
var chatDescriptions = map[string]string{
	"get_resource_describe": "Get kubectl describe output: events, conditions, detailed status and related " +
		"resources, for any workload, networking, config, storage, cluster, RBAC or policy/quota type. Best " +
		"for scheduling, pending pods, quota, policy denials and conditions.",

	"get_external_connections": "List the endpoints OUTSIDE the cluster that workloads talk to — managed " +
		"databases, caches, queues, load balancers, cloud APIs — from observed flows, each classified: " +
		"provider, managed service, the resource name and region the host encodes, whether it is cluster " +
		"plumbing (infrastructure: never the dependency to chase), the callers, and, when the org has a " +
		"connector for that provider, the connector tools to start with (firstTools; prefix them as in your " +
		"tool list). Call it after the in-cluster evidence and before any cloud connector tool. Pass the " +
		"failing workload: without flow data it falls back to the hosts its configuration names. " +
		"sourcePresent=false means no flow data, not no connections. An inFleet row is an address a cluster " +
		"of this organization exposes: the dependency is that workload, and when it is another cluster " +
		"(sameCluster false) follow the row's note.",

	"get_fleet_summary": "Get active insights PER CLUSTER across the organisation, worst first, with the " +
		"totals: the only tool that sees beyond the selected cluster (list_clusters gives names and " +
		"connectivity only). It reads the stored state, so clusters that are down still count.",

	"get_findings": "Get the cluster's SECURITY findings: CVEs and misconfigurations (Trivy), policy " +
		"violations (Kyverno), CIS controls. With no filter, the posture: counts, affected workloads, the " +
		"worst one and the images carrying the most findings. Filter by severity, kind, source, image or " +
		"resource for rows with their remediation. cluster=\"all\" for the org; status=resolved for " +
		"history. Unavailable is NOT a clean cluster.",

	"get_insight_episodes": "Get the HISTORY of insights: episodes resolved, expired or still firing, how " +
		"they ended and how often they came back — has this happened before, is it flapping, what happened " +
		"on a cluster that is down. It reads stored history; cluster=\"all\" reaches clusters no longer in " +
		"the selector.",

	"get_insight_episode": "Get ONE insight episode in full: its timeline (opened, severity changes, " +
		"silences, and who did each), the evidence the rule recorded, and its recurrence. The id comes from " +
		"get_insight_episodes or the insight the operator opened.",

	"get_workload_metrics": "Get CPU, memory, network or node-disk usage of a workload, pod or node over " +
		"a time range: {min, avg, max, p95} and a ~12-point sparkline per metric. With cpu or memory it " +
		"also returns the current bounds and utilizationPercent — of requests/limits for a pod or " +
		"workload, of allocatable/capacity for a node. metric=filesystem is node-only (% of the node's " +
		"disk per mountpoint) and refused for a pod or workload. Pod disk IO and PVC fill are not here: " +
		"the PVC has its own monitor.",

	"get_operational_episodes": "Get bursts of insights that fired together, classified by shared cause: " +
		"node_rotation, node_pressure, mass_rollout, unknown_burst. Call it FIRST whenever several resources " +
		"broke at about the same time: the burst names the common cause and tells you when a workload is a " +
		"victim, not the culprit.",
}

// chatProperties are the chat's wording of some of those tools' arguments,
// by tool and argument, on the same terms.
var chatProperties = map[string]map[string]string{
	"get_workload_metrics": {
		"kind":         "Target kind, case-sensitive; Node for the node-level view.",
		"namespace":    "Target namespace; ignored for Node.",
		"metrics":      "One to four; each is a round-trip, so ask only for what you need.",
		"range":        "Time range back from now (default 15m). Always ~12 points: a wider range costs resolution, not tokens. When the plan keeps less history it is narrowed and rangeAdjusted says so.",
		"perContainer": "Split CPU/memory by container (not for network or nodes). Default false.",
	},
}

// chatDescription is the chat's wording of a tool, or "" when it shares the
// catalogue's.
func chatDescription(name string) string { return chatDescriptions[name] }

// withChatProperties rewrites the argument descriptions chatProperties names.
// The schema is built fresh by every ToolDefinitions call, so the catalogue's
// copy is untouched.
func withChatProperties(def ToolDefinition) ToolDefinition {
	props, _ := def.InputSchema["properties"].(map[string]interface{})
	for name, desc := range chatProperties[def.Name] {
		if p, ok := props[name].(map[string]interface{}); ok {
			p["description"] = desc
		}
	}
	return def
}
