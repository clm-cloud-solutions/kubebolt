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
		node := stringArg(args, "node")
		list := conn.GetResources(t, ns, search, status, node, sort, order, page, limit)
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
		sinceTimeStr := stringArg(args, "sinceTime")
		endTimeStr := stringArg(args, "endTime")
		previous := boolArg(args, "previous")

		tailLines := int64(intArg(args, "tailLines", defaultLogTailLines))
		if tailLines <= 0 {
			tailLines = defaultLogTailLines
		}
		if tailLines > maxLogTailLines {
			tailLines = maxLogTailLines
		}

		q := cluster.LogQuery{
			Container: container,
			TailLines: tailLines,
			Previous:  previous,
		}
		if since != "" {
			d, err := time.ParseDuration(since)
			if err != nil {
				res.Content = jsonString(map[string]string{"error": fmt.Sprintf("invalid since value %q: expected duration like '15m', '1h'", since)})
				res.IsError = true
				return res
			}
			if d > 0 {
				q.SinceSeconds = int64(d.Seconds())
			}
		}
		if sinceTimeStr != "" {
			t, err := time.Parse(time.RFC3339, sinceTimeStr)
			if err != nil {
				res.Content = jsonString(map[string]string{"error": fmt.Sprintf("invalid sinceTime %q: expected RFC3339 like '2026-05-10T14:00:00Z'", sinceTimeStr)})
				res.IsError = true
				return res
			}
			q.SinceTime = t
		}
		if endTimeStr != "" {
			t, err := time.Parse(time.RFC3339, endTimeStr)
			if err != nil {
				res.Content = jsonString(map[string]string{"error": fmt.Sprintf("invalid endTime %q: expected RFC3339 like '2026-05-10T16:00:00Z'", endTimeStr)})
				res.IsError = true
				return res
			}
			q.EndTime = t
		}

		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}

		// Resolve the container name BEFORE calling the apiserver.
		// Without this, multi-container pods (gitlab-webservice has 5
		// containers, every istio-injected pod has 2+) fail with a
		// human-readable error the LLM has to parse to retry.
		//
		// Two-step flow for multi-container pods, to keep token cost
		// in check: instead of auto-picking the first container and
		// shipping its 20-50KB of logs (which the LLM might then
		// supersede with a second call to a different container),
		// we return ONLY the container list on the first call and
		// let the LLM pick using its world-knowledge of common
		// container-naming conventions. The LLM then re-calls with
		// `container=<name>` and gets the logs in a single round.
		// Net savings: ~25-50% on multi-container pods compared to
		// the auto-fetch approach. Single-container pods are
		// transparently auto-resolved (no extra round-trip there
		// because there's no choice to make).
		extraMeta := map[string]any{}
		if detail, dErr := conn.GetResourceDetail("pods", ns, name); dErr == nil {
			containerNames := extractPodContainerNames(detail)

			if container == "" {
				switch len(containerNames) {
				case 0:
					// No container info available (pod detail empty
					// or unusual shape). Fall through to GetPodLogs
					// and let the apiserver's error surface.
				case 1:
					// Single-container pod: auto-resolve. No choice
					// to make, so no round-trip needed.
					q.Container = containerNames[0]
					extraMeta["containerSelected"] = containerNames[0]
				default:
					// Multi-container, no container specified.
					// Return the list + nudge the LLM to pick using
					// the container names + the symptom in the
					// user's question. Common heuristics the LLM
					// applies automatically: skip init-style names
					// (`certificates`, `configure`, `dependencies`,
					// `wait-for-x`), prefer app-named containers
					// (`webservice`, `api`, the deployment's name),
					// recognize sidecar patterns (`istio-proxy`,
					// `linkerd-proxy`, `vault-agent`, `fluentbit`)
					// and pick the app container unless the user's
					// question is specifically about the sidecar.
					res.Content = jsonString(map[string]any{
						"availableContainers": containerNames,
						"podContainerCount":   len(containerNames),
						"hint": fmt.Sprintf(
							"pod %s/%s has %d containers — no logs returned on this call. Pick the container whose logs match the symptom you're investigating (e.g., for HTTP errors prefer the app container, not init helpers like 'configure' / 'certificates' / 'dependencies'; for traffic policy issues, prefer 'istio-proxy' or similar sidecars). Re-call get_pod_logs with container=<name> from availableContainers to actually read the logs.",
							ns, name, len(containerNames)),
					})
					return res
				}
			} else {
				// User-supplied container — validate against the pod
				// so we fail fast with a useful error instead of
				// surfacing the apiserver's "not found" 404.
				valid := false
				for _, n := range containerNames {
					if n == container {
						valid = true
						break
					}
				}
				if !valid && len(containerNames) > 0 {
					res.Content = jsonString(map[string]any{
						"error":               fmt.Sprintf("container %q not found in pod %s/%s", container, ns, name),
						"availableContainers": containerNames,
					})
					res.IsError = true
					return res
				}
				extraMeta["containerSelected"] = container
			}
		}
		// If the pod fetch failed (network blip, pod just deleted),
		// fall through with whatever container the caller passed.
		// GetPodLogs will surface the underlying error if any.

		logs, err := conn.GetPodLogs(ns, name, q)
		if err != nil {
			// Attach the meta we collected so the LLM still knows
			// what containers are available even when the read
			// failed for some other reason.
			payload := map[string]any{"error": err.Error()}
			for k, v := range extraMeta {
				payload[k] = v
			}
			res.Content = jsonString(payload)
			res.IsError = true
			return res
		}
		res.Content = formatPodLogs(logs, grep, extraMeta)

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

	case "propose_debug_pod":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		image := stringArg(args, "image")
		if image == "" {
			image = "busybox"
		}
		targetContainer := stringArg(args, "targetContainer")
		rationale := stringArg(args, "rationale")
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		if _, err := conn.GetResourceDetail("pods", ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target pod %s/%s not found: %w", ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("debug_pod")
		p.Target = ProposalTarget{Type: "pods", Namespace: ns, Name: name}
		p.Params["image"] = image
		if targetContainer != "" {
			p.Params["targetContainer"] = targetContainer
		}
		p.Summary = fmt.Sprintf("Attach debug container (%s) to pod %s/%s", image, ns, name)
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		// Ephemeral containers can't be removed without recreating the pod.
		p.Reversible = false
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

	case "propose_set_resources":
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
			res.Content = fmt.Sprintf(`{"error":"cannot set-resources on %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		containers, err := parseSetResourcesContainers(args["containers"])
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		if len(containers) == 0 {
			res.Content = `{"error":"containers is required and must be non-empty"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		// Validate every requested container name exists on the pod
		// template. Reject early with the valid list so the LLM can
		// retry with a correct name on the next turn instead of seeing
		// a 400 after the user clicks Execute.
		validNames := extractContainerNames(detail)
		for _, c := range containers {
			if !validNames[c["container"].(string)] {
				res.Content = jsonString(map[string]interface{}{
					"error":          fmt.Sprintf("container %q not found on %s/%s/%s", c["container"], t, ns, name),
					"validContainers": namesAsSlice(validNames),
				})
				res.IsError = true
				return res
			}
		}
		p := newProposal("set_resources")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["containers"] = containers
		p.Summary = fmt.Sprintf("Set resources on %s %s/%s (%d container%s)", strings.TrimSuffix(t, "s"), ns, name, len(containers), pluralS(len(containers)))
		p.Rationale = rationale
		// Default medium — spec-mutating + triggers a rolling update,
		// so heavier than restart. LLM can override per riskProp.
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_set_image":
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
			res.Content = fmt.Sprintf(`{"error":"cannot set-image on %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		images, err := parseSetImageEntries(args["images"])
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		if len(images) == 0 {
			res.Content = `{"error":"images is required and must be non-empty"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		// Build name → current image map so we can both validate the
		// container exists AND short-circuit if every requested image
		// already matches. Without the short-circuit Kobi would render a
		// useless card whose Execute is a no-op.
		currentImages := extractContainerImages(detail)
		if len(currentImages) == 0 {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("could not read current container images for %s/%s/%s", t, ns, name),
			})
			res.IsError = true
			return res
		}
		allUnchanged := true
		for _, img := range images {
			containerName := img["container"].(string)
			requestedImage := img["image"].(string)
			currentImage, ok := currentImages[containerName]
			if !ok {
				res.Content = jsonString(map[string]interface{}{
					"error":           fmt.Sprintf("container %q not found on %s/%s/%s", containerName, t, ns, name),
					"validContainers": mapKeys(currentImages),
				})
				res.IsError = true
				return res
			}
			if requestedImage != currentImage {
				allUnchanged = false
			}
		}
		if allUnchanged {
			res.Content = jsonString(map[string]interface{}{
				"error": "no image change requested — every container already runs the requested image",
				"hint":  "if the workload is failing despite identical images, the cause is elsewhere (probes, env, resources)",
			})
			res.IsError = true
			return res
		}
		p := newProposal("set_image")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["images"] = images
		p.Summary = fmt.Sprintf("Set image on %s %s/%s (%d container%s)", strings.TrimSuffix(t, "s"), ns, name, len(images), pluralS(len(images)))
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_set_env":
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
			res.Content = fmt.Sprintf(`{"error":"cannot set-env on %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		envContainers, err := parseSetEnvContainers(args["containers"])
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		if len(envContainers) == 0 {
			res.Content = `{"error":"containers is required and must be non-empty"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		validNames := extractContainerNames(detail)
		nSet, nRemove := 0, 0
		for _, c := range envContainers {
			containerName := c["container"].(string)
			if !validNames[containerName] {
				res.Content = jsonString(map[string]interface{}{
					"error":           fmt.Sprintf("container %q not found on %s/%s/%s", containerName, t, ns, name),
					"validContainers": namesAsSlice(validNames),
				})
				res.IsError = true
				return res
			}
			envList, _ := c["env"].([]map[string]interface{})
			for _, e := range envList {
				envName, _ := e["name"].(string)
				action, _ := e["action"].(string)
				if envName == "" {
					res.Content = jsonString(map[string]interface{}{
						"error": fmt.Sprintf("env entry on container %q has empty name", containerName),
					})
					res.IsError = true
					return res
				}
				switch action {
				case "set":
					nSet++
					// Credential guardrail: refuse a literal value for any
					// env var whose NAME looks credential-shaped. The LLM
					// cannot be trusted to handle credentials by direct
					// value — the right path is Secret/CM refs in the
					// YAML editor.
					if credentialNameRE.MatchString(envName) {
						if v, ok := e["value"].(string); ok && v != "" {
							res.Content = jsonString(map[string]interface{}{
								"error":   fmt.Sprintf("refusing to set literal value on credential-shaped env var %q on container %q", envName, containerName),
								"hint":    "use the YAML editor to bind this env var to a Secret/ConfigMap (valueFrom.secretKeyRef / configMapKeyRef)",
								"pattern": "names matching password|secret|token|key|credential are blocked at the Copilot layer",
							})
							res.IsError = true
							return res
						}
					}
				case "remove":
					nRemove++
				default:
					res.Content = jsonString(map[string]interface{}{
						"error": fmt.Sprintf("env entry %q on container %q has invalid action %q — must be \"set\" or \"remove\"", envName, containerName, action),
					})
					res.IsError = true
					return res
				}
			}
		}
		p := newProposal("set_env")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["containers"] = envContainers
		// triggerRollout=true so existing pods pick up literal-value
		// changes immediately. The set-env endpoint applies the
		// rollout-restart annotation when this is set.
		p.Params["triggerRollout"] = true
		p.Summary = fmt.Sprintf("Set env on %s %s/%s (%d set, %d remove)", strings.TrimSuffix(t, "s"), ns, name, nSet, nRemove)
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_patch_hpa":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		// At least one bound must be present. We accept the keys whether
		// they arrive as JSON number (float64) or as an absent value.
		_, hasMin := args["minReplicas"]
		_, hasMax := args["maxReplicas"]
		if !hasMin && !hasMax {
			res.Content = `{"error":"at least one of minReplicas or maxReplicas is required"}`
			res.IsError = true
			return res
		}
		minR := intArg(args, "minReplicas", -1)
		maxR := intArg(args, "maxReplicas", -1)
		// Validate the side the caller actually set.
		if hasMin && minR < 0 {
			res.Content = `{"error":"minReplicas must be >= 0"}`
			res.IsError = true
			return res
		}
		if hasMax && maxR < 1 {
			res.Content = `{"error":"maxReplicas must be >= 1"}`
			res.IsError = true
			return res
		}
		if hasMax && maxR > hpaMaxReplicasCap {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("maxReplicas must be <= %d (safety cap)", hpaMaxReplicasCap),
				"hint":  "if you genuinely need more than that, scale via the YAML editor and add cluster-ops review",
			})
			res.IsError = true
			return res
		}
		if hasMin && hasMax && maxR < minR {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("maxReplicas (%d) must be >= minReplicas (%d)", maxR, minR),
			})
			res.IsError = true
			return res
		}
		// Verify the HPA exists (informer cache lookup).
		if _, err := conn.GetResourceDetail("hpas", ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target hpas/%s/%s not found: %w", ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("patch_hpa")
		p.Target = ProposalTarget{Type: "hpas", Namespace: ns, Name: name}
		if hasMin {
			p.Params["minReplicas"] = minR
		}
		if hasMax {
			p.Params["maxReplicas"] = maxR
		}
		// Summary line: prefer "max X → Y" when the caller is bumping
		// maxReplicas (the common case), fall back to a generic line.
		switch {
		case hasMax && hasMin:
			p.Summary = fmt.Sprintf("Patch HPA %s/%s (min=%d, max=%d)", ns, name, minR, maxR)
		case hasMax:
			p.Summary = fmt.Sprintf("Patch HPA %s/%s (max=%d)", ns, name, maxR)
		case hasMin:
			p.Summary = fmt.Sprintf("Patch HPA %s/%s (min=%d)", ns, name, minR)
		}
		p.Rationale = rationale
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

	case "get_workload_metrics":
		out, err := e.execGetWorkloadMetrics(call, args, conn)
		if err != nil {
			res.Content = err.Error()
			res.IsError = true
			return res
		}
		res.Content = out

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
//
// `extra` carries caller-supplied metadata that gets merged into the
// response payload — used by the get_pod_logs executor case to surface
// containerSelected / availableContainers / containerAutoSelected /
// hint so the LLM can re-query a different container without parsing
// human-readable error strings.
func formatPodLogs(raw, grep string, extra map[string]any) string {
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
	// Merge caller meta last. If the caller also set "hint" (e.g.
	// container auto-selected note), the truncation hint takes
	// precedence because it's more actionable — operators care more
	// about "your data is incomplete" than "I picked a container".
	for k, v := range extra {
		if _, present := payload[k]; !present {
			payload[k] = v
		}
	}
	return jsonString(payload)
}

// extractPodContainerNames returns the names of the regular containers
// (not init/ephemeral) of a pod as exposed by the connector's
// GetResourceDetail. Mirrors what the apiserver's "a container name
// must be specified, choose one of: [...]" error lists — the
// auto-selection logic in get_pod_logs uses this to pre-empt that
// error class on multi-container pods.
func extractPodContainerNames(detail map[string]interface{}) []string {
	cs, ok := detail["containers"].([]map[string]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if n, ok := c["name"].(string); ok && n != "" {
			out = append(out, n)
		}
	}
	return out
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
		list := conn.GetResources(rt, "", query, "", "", "", "", 1, limit)
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

// hpaMaxReplicasCap is the safety ceiling Kobi enforces on
// propose_patch_hpa. Mirrors maxReplicasSafetyCap in the api package
// — they MUST stay in sync. If the api-layer cap changes, this
// constant has to move with it; the load-bearing test in
// actions_hpa_test.go (TestMaxReplicasSafetyCapDefined) pins the
// canonical value to 1000 and is the source of truth.
const hpaMaxReplicasCap = 1000

// credentialNameRE matches env var NAMES that look credential-shaped.
// We refuse a literal `value` on these (the operator must bind a
// Secret/ConfigMap reference via the YAML editor). The pattern is
// intentionally conservative — only the most obvious "this is a
// secret" naming conventions, to avoid false positives that would
// frustrate operators tweaking legit non-secret env vars.
var credentialNameRE = regexp.MustCompile(`(?i)(password|secret|token|key|credential)`)

// parseSetResourcesContainers normalizes the LLM-provided containers
// array into a clean []map[string]interface{} where each row is
// shape-checked. We rebuild instead of passing through so the
// downstream JSON (in the ActionProposal params) is stable regardless
// of what the LLM padded the call with.
func parseSetResourcesContainers(raw interface{}) ([]map[string]interface{}, error) {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("containers must be an array")
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("containers[%d] must be an object", i)
		}
		containerName, _ := m["container"].(string)
		if containerName == "" {
			return nil, fmt.Errorf("containers[%d].container is required", i)
		}
		row := map[string]interface{}{"container": containerName}
		if v, ok := m["initContainer"].(bool); ok && v {
			row["initContainer"] = true
		}
		if req, ok := m["requests"].(map[string]interface{}); ok {
			cleaned := cleanQuantityMap(req)
			if len(cleaned) > 0 {
				row["requests"] = cleaned
			}
		}
		if lim, ok := m["limits"].(map[string]interface{}); ok {
			cleaned := cleanQuantityMap(lim)
			if len(cleaned) > 0 {
				row["limits"] = cleaned
			}
		}
		if row["requests"] == nil && row["limits"] == nil {
			return nil, fmt.Errorf("containers[%d] must set at least one of requests/limits", i)
		}
		out = append(out, row)
	}
	return out, nil
}

// cleanQuantityMap keeps only the cpu/memory keys with non-empty
// string values. Drops everything else so the proposal payload is
// not contaminated by stray fields the LLM may have added.
func cleanQuantityMap(m map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	if v, ok := m["cpu"].(string); ok && v != "" {
		out["cpu"] = v
	}
	if v, ok := m["memory"].(string); ok && v != "" {
		out["memory"] = v
	}
	return out
}

// parseSetImageEntries normalizes the LLM-provided images array.
func parseSetImageEntries(raw interface{}) ([]map[string]interface{}, error) {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("images must be an array")
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("images[%d] must be an object", i)
		}
		containerName, _ := m["container"].(string)
		image, _ := m["image"].(string)
		if containerName == "" {
			return nil, fmt.Errorf("images[%d].container is required", i)
		}
		if image == "" {
			return nil, fmt.Errorf("images[%d].image is required", i)
		}
		out = append(out, map[string]interface{}{
			"container": containerName,
			"image":     image,
		})
	}
	return out, nil
}

// parseSetEnvContainers normalizes the LLM-provided env containers
// array. The env entry list inside each container is kept as
// []map[string]interface{} so the executor case can iterate without
// having to reshape again. valueFrom is not exposed here — only
// literal value or remove — per the file-level comment on the env
// tool description (literal values + Secret refs split between Kobi
// and the YAML editor).
func parseSetEnvContainers(raw interface{}) ([]map[string]interface{}, error) {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("containers must be an array")
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("containers[%d] must be an object", i)
		}
		containerName, _ := m["container"].(string)
		if containerName == "" {
			return nil, fmt.Errorf("containers[%d].container is required", i)
		}
		envRaw, ok := m["env"].([]interface{})
		if !ok || len(envRaw) == 0 {
			return nil, fmt.Errorf("containers[%d].env is required and must be non-empty", i)
		}
		envOut := make([]map[string]interface{}, 0, len(envRaw))
		for j, e := range envRaw {
			em, ok := e.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("containers[%d].env[%d] must be an object", i, j)
			}
			row := map[string]interface{}{}
			if v, ok := em["name"].(string); ok {
				row["name"] = v
			}
			if v, ok := em["action"].(string); ok {
				row["action"] = v
			}
			if v, ok := em["value"].(string); ok {
				row["value"] = v
			}
			envOut = append(envOut, row)
		}
		row := map[string]interface{}{
			"container": containerName,
			"env":       envOut,
		}
		if v, ok := m["initContainer"].(bool); ok && v {
			row["initContainer"] = true
		}
		out = append(out, row)
	}
	return out, nil
}

// extractContainerNames returns the set of container names from a
// resource detail map. Used to validate that propose_set_resources /
// propose_set_env target a real container before emitting a card.
// Covers both normal and init containers (init lives under the
// `initContainers` key in pod-level details; for workloads we don't
// expose initContainers separately yet, so init-name validation is
// best-effort — the backend handler will reject unknown init
// containers cleanly at Execute time).
func extractContainerNames(detail map[string]interface{}) map[string]bool {
	out := map[string]bool{}
	if cs, ok := detail["containers"].([]map[string]interface{}); ok {
		for _, c := range cs {
			if n, ok := c["name"].(string); ok && n != "" {
				out[n] = true
			}
		}
	}
	return out
}

// extractContainerImages returns container-name → image map from a
// resource detail. Used by propose_set_image for the no-op short-
// circuit + container existence check.
func extractContainerImages(detail map[string]interface{}) map[string]string {
	out := map[string]string{}
	if cs, ok := detail["containers"].([]map[string]interface{}); ok {
		for _, c := range cs {
			n, _ := c["name"].(string)
			img, _ := c["image"].(string)
			if n != "" {
				out[n] = img
			}
		}
	}
	return out
}

func namesAsSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ensure unused models import survives compile
var _ = models.ResourceList{}
