package copilot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Limits applied to tool results to prevent context window blow-ups.
// Pod logs and full topology dumps can be enormous and quickly exhaust the
// LLM's context if multiple calls are made in sequence.
const (
	// Max bytes a single tool result can occupy. ~32KB ≈ 8K tokens.
	maxToolResultBytes = 32 * 1024
	// Max lines we'll fetch from a pod log regardless of what the LLM asks.
	maxLogTailLines = 300
)

// Executor runs tool calls server-side using the active Connector and Engine.
// Each tool maps to existing connector/engine methods. Tool execution is
// internal to the backend — no HTTP round-trip from the chat handler to
// other endpoints.
type Executor struct {
	manager *cluster.Manager
}

// NewExecutor creates a new tool executor bound to a cluster manager.
func NewExecutor(manager *cluster.Manager) *Executor {
	return &Executor{manager: manager}
}

// Execute runs a single tool call and returns its result as a JSON string.
// Errors during execution are returned as ToolResult with IsError=true so the
// LLM can react gracefully.
func (e *Executor) Execute(call ToolCall) ToolResult {
	res := ToolResult{ToolCallID: call.ID}

	conn := e.manager.Connector()
	if conn == nil {
		res.Content = `{"error":"cluster not connected"}`
		res.IsError = true
		return res
	}

	args := parseArgs(call.Input)

	switch call.Name {
	case "get_cluster_overview":
		res.Content = jsonString(conn.GetOverview())

	case "list_resources":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		search := stringArg(args, "search")
		status := stringArg(args, "status")
		sort := stringArg(args, "sort")
		order := stringArg(args, "order")
		page := intArg(args, "page", 1)
		limit := intArg(args, "limit", 50)
		if t == "" {
			res.Content = `{"error":"type parameter is required"}`
			res.IsError = true
			return res
		}
		list := conn.GetResources(t, ns, search, status, sort, order, page, limit)
		if list.Forbidden {
			res.Content = fmt.Sprintf(`{"error":"forbidden: insufficient permissions to access %s","forbidden":true}`, t)
			res.IsError = true
			return res
		}
		res.Content = jsonString(list)

	case "get_resource_detail":
		t, ns, name := nsResourceArgs(args)
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = jsonString(detail)

	case "get_resource_yaml":
		t, ns, name := nsResourceArgs(args)
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		yamlBytes, err := conn.GetResourceYAML(t, ns, name)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = jsonString(map[string]string{"yaml": string(yamlBytes)})

	case "get_resource_describe":
		t, ns, name := nsResourceArgs(args)
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		describeOutput, err := describeResource(conn, t, ns, name)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = jsonString(map[string]string{"describe": describeOutput})

	case "get_pod_logs":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		container := stringArg(args, "container")
		// Hard cap on tailLines — even if the LLM asks for 1000, limit to
		// maxLogTailLines to prevent context blow-up.
		tailLines := int64(intArg(args, "tailLines", 100))
		if tailLines > maxLogTailLines {
			tailLines = maxLogTailLines
		}
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		logs, err := conn.GetPodLogs(ns, name, container, tailLines)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = jsonString(map[string]string{"logs": logs})

	case "get_workload_pods":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		var pods []map[string]interface{}
		switch t {
		case "deployments":
			pods = conn.GetDeploymentPods(ns, name)
		case "statefulsets":
			pods = conn.GetStatefulSetPods(ns, name)
		case "daemonsets":
			pods = conn.GetDaemonSetPods(ns, name)
		case "jobs":
			pods = conn.GetJobPods(ns, name)
		default:
			res.Content = fmt.Sprintf(`{"error":"unsupported workload type: %s"}`, t)
			res.IsError = true
			return res
		}
		res.Content = jsonString(map[string]interface{}{"pods": pods})

	case "get_workload_history":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		var history []map[string]interface{}
		if t == "deployments" {
			history = conn.GetDeploymentHistory(ns, name)
		} else {
			history = conn.GetWorkloadHistory(t, ns, name)
		}
		res.Content = jsonString(map[string]interface{}{"history": history})

	case "get_cronjob_jobs":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		jobs := conn.GetCronJobJobs(ns, name)
		res.Content = jsonString(map[string]interface{}{"jobs": jobs})

	case "get_topology":
		res.Content = jsonString(conn.GetTopology())

	case "get_insights":
		eng := e.manager.Engine()
		if eng == nil {
			res.Content = `{"error":"insights engine not available"}`
			res.IsError = true
			return res
		}
		severity := stringArg(args, "severity")
		resolved := boolArg(args, "resolved")
		items := eng.GetInsights(severity, resolved)
		res.Content = jsonString(map[string]interface{}{"items": items, "total": len(items)})

	case "get_events":
		eventType := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		involvedKind := stringArg(args, "involvedKind")
		involvedName := stringArg(args, "involvedName")
		limit := intArg(args, "limit", 100)
		events := conn.GetEvents(eventType, ns, involvedKind, involvedName, limit)
		res.Content = jsonString(events)

	case "search_resources":
		query := strings.ToLower(strings.TrimSpace(stringArg(args, "q")))
		if query == "" {
			res.Content = `{"results":[]}`
			return res
		}
		results := searchAllResources(conn, query, 50)
		res.Content = jsonString(map[string]interface{}{"results": results})

	case "get_permissions":
		perms := conn.Permissions()
		res.Content = jsonString(perms)

	case "list_clusters":
		clusters := e.manager.ListClusters()
		res.Content = jsonString(clusters)

	default:
		res.Content = fmt.Sprintf(`{"error":"unknown tool: %s"}`, call.Name)
		res.IsError = true
	}

	// Truncate oversized results to prevent context window blow-up.
	// Some tools (logs, topology, describe, yaml) can return huge payloads
	// that quickly exhaust the LLM's context if multiple are made in sequence.
	res.Content = truncateToolResult(res.Content, call.Name)

	return res
}

// truncateToolResult caps the size of tool result content. If truncated, it
// appends a clear notice so the LLM knows the data was cut.
func truncateToolResult(content, toolName string) string {
	if len(content) <= maxToolResultBytes {
		return content
	}
	truncated := content[:maxToolResultBytes]
	notice := fmt.Sprintf(
		`... [TRUNCATED: %s result was %d bytes, capped at %d bytes (~%dKB) to preserve context window. Request a smaller subset (fewer lines, narrower namespace, specific resource) for more detail.]`,
		toolName, len(content), maxToolResultBytes, maxToolResultBytes/1024,
	)
	// Wrap as a JSON-safe response so the LLM still gets a valid payload
	return jsonString(map[string]string{
		"truncated_result": truncated,
		"notice":           notice,
	})
}

// ----- helpers -----

func parseArgs(input json.RawMessage) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	var args map[string]interface{}
	if err := json.Unmarshal(input, &args); err != nil {
		return map[string]interface{}{}
	}
	return args
}

func stringArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return v
}

func intArg(args map[string]interface{}, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func boolArg(args map[string]interface{}, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func nsResourceArgs(args map[string]interface{}) (string, string, string) {
	t := stringArg(args, "type")
	ns := stringArg(args, "namespace")
	name := stringArg(args, "name")
	if ns == "_" {
		ns = ""
	}
	return t, ns, name
}

func jsonString(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal failed: %s"}`, err.Error())
	}
	return string(b)
}

func errJSON(err error) string {
	return jsonString(map[string]string{"error": err.Error()})
}

// searchAllResources reuses the same approach as the /search handler.
// We duplicate the resource type list here to avoid coupling the copilot
// package to the api package.
func searchAllResources(conn *cluster.Connector, query string, limit int) []map[string]interface{} {
	types := []string{
		"pods", "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs",
		"services", "ingresses", "configmaps", "secrets", "nodes", "namespaces",
		"pvcs", "pvs", "hpas", "storageclasses",
	}
	results := make([]map[string]interface{}, 0)
	for _, rt := range types {
		if len(results) >= limit {
			break
		}
		list := conn.GetResources(rt, "", query, "", "", "", 1, limit)
		for _, item := range list.Items {
			if len(results) >= limit {
				break
			}
			name, _ := item["name"].(string)
			ns, _ := item["namespace"].(string)
			status, _ := item["status"].(string)
			results = append(results, map[string]interface{}{
				"name":         name,
				"namespace":    ns,
				"kind":         normalizeKind(rt),
				"status":       status,
				"resourceType": rt,
			})
		}
	}
	return results
}

func normalizeKind(rt string) string {
	switch rt {
	case "pods":
		return "Pod"
	case "deployments":
		return "Deployment"
	case "statefulsets":
		return "StatefulSet"
	case "daemonsets":
		return "DaemonSet"
	case "jobs":
		return "Job"
	case "cronjobs":
		return "CronJob"
	case "services":
		return "Service"
	case "ingresses":
		return "Ingress"
	case "configmaps":
		return "ConfigMap"
	case "secrets":
		return "Secret"
	case "nodes":
		return "Node"
	case "namespaces":
		return "Namespace"
	case "pvcs":
		return "PersistentVolumeClaim"
	case "pvs":
		return "PersistentVolume"
	case "hpas":
		return "HorizontalPodAutoscaler"
	case "storageclasses":
		return "StorageClass"
	}
	return rt
}

// ensure unused models import survives compile
var _ = models.ResourceList{}
