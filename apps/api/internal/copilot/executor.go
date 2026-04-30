package copilot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Limits applied to tool results to prevent context window blow-ups.
// Pod logs and full topology dumps can be enormous and quickly exhaust the
// LLM's context if multiple calls are made in sequence.
const (
	// Max bytes a generic tool result can occupy. ~32KB ≈ 8K tokens.
	maxToolResultBytes = 32 * 1024
	// Max lines we'll fetch from a pod log regardless of what the LLM asks.
	maxLogTailLines = 500
	// Max bytes we'll keep from pod logs after fetch+filter. Slightly larger
	// than the generic cap because log investigation is a primary use case
	// and lines are cheap to tokenize. ~48KB ≈ 12K tokens worst case.
	maxLogBytes = 48 * 1024
	// Default tail when the LLM doesn't specify one.
	defaultLogTailLines = 200
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
		grep := stringArg(args, "grep")
		since := stringArg(args, "since")

		tailLines := int64(intArg(args, "tailLines", defaultLogTailLines))
		if tailLines <= 0 {
			tailLines = defaultLogTailLines
		}
		if tailLines > maxLogTailLines {
			tailLines = maxLogTailLines
		}

		var sinceSeconds int64
		if since != "" {
			d, err := time.ParseDuration(since)
			if err != nil {
				res.Content = jsonString(map[string]string{"error": fmt.Sprintf("invalid since value %q: expected duration like '15m', '1h'", since)})
				res.IsError = true
				return res
			}
			if d > 0 {
				sinceSeconds = int64(d.Seconds())
			}
		}

		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		logs, err := conn.GetPodLogs(ns, name, container, tailLines, sinceSeconds)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = formatPodLogs(logs, grep)

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

	case "propose_restart_workload":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		switch t {
		case "deployments", "statefulsets", "daemonsets":
		default:
			res.Content = fmt.Sprintf(`{"error":"cannot restart %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		// Verify the target exists so we don't propose ghost actions. This is
		// a read against the local informer cache — cheap.
		if _, err := conn.GetResourceDetail(t, ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("restart_workload")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Summary = fmt.Sprintf("Restart %s %s/%s", strings.TrimSuffix(t, "s"), ns, name)
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "low")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_scale_workload":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		replicas := intArg(args, "replicas", -1)
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		if replicas < 0 {
			res.Content = `{"error":"replicas must be >= 0"}`
			res.IsError = true
			return res
		}
		switch t {
		case "deployments", "statefulsets":
		default:
			res.Content = fmt.Sprintf(`{"error":"cannot scale %s — only deployments and statefulsets"}`, t)
			res.IsError = true
			return res
		}
		if _, err := conn.GetResourceDetail(t, ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("scale_workload")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["replicas"] = replicas
		p.Summary = fmt.Sprintf("Scale %s %s/%s to %d replica(s)", strings.TrimSuffix(t, "s"), ns, name, replicas)
		p.Rationale = rationale
		// Default: scale-to-zero is medium (pauses the workload); other
		// scales are low. The LLM can override via the `risk` arg when
		// situational context warrants a different level.
		defaultRisk := "low"
		if replicas == 0 {
			defaultRisk = "medium"
		}
		p.Risk = resolveRisk(stringArg(args, "risk"), defaultRisk)
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_rollback_deployment":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		toRevision := intArg(args, "toRevision", 0)
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		// Verify the deployment exists.
		dep, err := conn.GetResourceDetail("deployments", ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target deployments/%s/%s not found: %w", ns, name, err))
			res.IsError = true
			return res
		}
		// Verify there is rollback history (>= 2 revisions). Without this
		// the action is impossible and the LLM should fall back to a
		// different remediation.
		history := conn.GetDeploymentHistory(ns, name)
		if len(history) < 2 {
			res.Content = jsonString(map[string]interface{}{
				"error":           fmt.Sprintf("deployment %s/%s has no rollback history (need >= 2 revisions, found %d)", ns, name, len(history)),
				"revisionsFound":  len(history),
				"hint":            "the deployment was never updated after creation; suggest a different remediation (restart, edit yaml, etc.)",
			})
			res.IsError = true
			return res
		}
		// Resolve current revision and the target.
		fromRev := 0
		if a, ok := dep["annotations"].(map[string]string); ok {
			if v := a["deployment.kubernetes.io/revision"]; v != "" {
				fromRev, _ = strconv.Atoi(v)
			}
		}
		resolvedTo := toRevision
		if resolvedTo == 0 {
			// Default: most recent revision != current.
			for _, h := range history {
				rstr, _ := h["revision"].(string)
				r, _ := strconv.Atoi(rstr)
				if r != fromRev && r > 0 {
					resolvedTo = r
					break
				}
			}
		} else {
			// Confirm specified revision exists in history.
			found := false
			for _, h := range history {
				rstr, _ := h["revision"].(string)
				r, _ := strconv.Atoi(rstr)
				if r == toRevision {
					found = true
					break
				}
			}
			if !found {
				res.Content = jsonString(map[string]interface{}{
					"error": fmt.Sprintf("revision %d not found in history of deployments/%s/%s", toRevision, ns, name),
				})
				res.IsError = true
				return res
			}
		}
		if resolvedTo == 0 || resolvedTo == fromRev {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("could not resolve a target revision distinct from the current (%d)", fromRev),
			})
			res.IsError = true
			return res
		}
		p := newProposal("rollback_deployment")
		p.Target = ProposalTarget{Type: "deployments", Namespace: ns, Name: name}
		p.Params["toRevision"] = resolvedTo
		if fromRev > 0 {
			p.Params["fromRevision"] = fromRev
		}
		if fromRev > 0 {
			p.Summary = fmt.Sprintf("Roll back deployment %s/%s from revision %d to revision %d", ns, name, fromRev, resolvedTo)
		} else {
			p.Summary = fmt.Sprintf("Roll back deployment %s/%s to revision %d", ns, name, resolvedTo)
		}
		p.Rationale = rationale
		// Default: medium (deployment-wide template change), but the LLM
		// can downgrade to "low" when the target revision is well-tested
		// and the change is small, or upgrade to "high" when rolling back
		// across many revisions or affecting critical production paths.
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_delete_resource":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		force := boolArg(args, "force")
		orphan := boolArg(args, "orphan")
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		// Whitelist enforcement at the executor level. Even if the LLM
		// somehow constructs a tool_call with a blocked type, we refuse
		// to materialize the proposal. Prompt injection defense: no
		// payload shape change can route around this switch.
		switch t {
		case "deployments", "statefulsets", "daemonsets",
			"services", "configmaps", "secrets",
			"jobs", "cronjobs", "pods", "ingresses",
			"hpas", "horizontalpodautoscalers":
			// allowed
		default:
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("resource type %q cannot be deleted via Copilot proposal — recommend kubectl directly", t),
				"hint":  "Allowed types: deployments, statefulsets, daemonsets, services, configmaps, secrets, jobs, cronjobs, pods, ingresses, hpas. Namespaces, nodes, PVs, PVCs, RBAC resources are blocked by design.",
			})
			res.IsError = true
			return res
		}
		// Verify the target exists.
		if _, err := conn.GetResourceDetail(t, ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}

		// Compute blast radius from the informer cache. Read-only; safe
		// to call from any tool. The LLM should read this and reflect
		// the consequences in its text response.
		blast := conn.ComputeDeleteBlastRadius(t, ns, name)

		p := newProposal("delete_resource")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["force"] = force
		p.Params["orphan"] = orphan
		p.Params["blastRadius"] = blast
		p.Summary = fmt.Sprintf("Delete %s %s/%s (irreversible)", strings.TrimSuffix(t, "s"), ns, name)
		p.Rationale = rationale
		// Default high — the LLM can technically downgrade per riskProp's
		// guidance, but for delete the tool description tells it to keep
		// high. Reversible is always false: deleting a resource is
		// irreversible (only "recoverable" if the user has the YAML
		// stored elsewhere, which we cannot verify).
		p.Risk = resolveRisk(stringArg(args, "risk"), "high")
		p.Reversible = false
		res.Content = jsonString(p)

	case "get_kubebolt_docs":
		topic := stringArg(args, "topic")
		res.Content = jsonString(map[string]string{
			"topic":   topic,
			"content": KubebolDocsGet(topic),
		})

	default:
		res.Content = fmt.Sprintf(`{"error":"unknown tool: %s"}`, call.Name)
		res.IsError = true
	}

	// Truncate oversized results to prevent context window blow-up.
	// Some tools (topology, describe, yaml) can return huge payloads that
	// quickly exhaust the LLM's context if multiple are made in sequence.
	// get_pod_logs handles its own smart truncation via formatPodLogs.
	if call.Name != "get_pod_logs" {
		res.Content = truncateToolResult(res.Content, call.Name)
	}

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

// formatPodLogs applies optional grep filtering and a byte cap that preserves
// the NEWEST log lines (truncates from the head, not the tail) aligned on line
// boundaries. The response always carries metadata so the LLM can decide
// whether to request a narrower window or a specific filter.
func formatPodLogs(raw, grep string) string {
	// Count original lines before any filtering
	originalLines := 0
	if raw != "" {
		originalLines = strings.Count(raw, "\n")
		if !strings.HasSuffix(raw, "\n") {
			originalLines++
		}
	}

	body := raw
	filterApplied := ""
	filteredOutLines := 0
	if grep != "" {
		re, err := regexp.Compile("(?i)" + grep)
		if err != nil {
			return jsonString(map[string]any{
				"error": fmt.Sprintf("invalid grep pattern %q: %s", grep, err.Error()),
			})
		}
		kept := make([]string, 0, 128)
		for _, line := range strings.Split(raw, "\n") {
			if line == "" {
				continue
			}
			if re.MatchString(line) {
				kept = append(kept, line)
			}
		}
		filterApplied = grep
		filteredOutLines = originalLines - len(kept)
		body = strings.Join(kept, "\n")
	}

	// Byte cap: preserve the TAIL (newest lines), not the head.
	truncated := false
	bytesDropped := 0
	if len(body) > maxLogBytes {
		head := len(body) - maxLogBytes
		// Advance to the next newline so we don't start mid-line.
		if nl := strings.IndexByte(body[head:], '\n'); nl >= 0 {
			head += nl + 1
		}
		bytesDropped = head
		body = body[head:]
		truncated = true
	}

	returnedLines := 0
	if body != "" {
		returnedLines = strings.Count(body, "\n")
		if !strings.HasSuffix(body, "\n") {
			returnedLines++
		}
	}

	payload := map[string]any{
		"logs":          body,
		"originalLines": originalLines,
		"returnedLines": returnedLines,
	}
	if filterApplied != "" {
		payload["grep"] = filterApplied
		payload["filteredOutLines"] = filteredOutLines
	}
	if truncated {
		payload["truncated"] = true
		payload["bytesDropped"] = bytesDropped
		payload["hint"] = "logs truncated to preserve context; use 'since' for a narrower window or 'grep' to filter"
	}
	return jsonString(payload)
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
