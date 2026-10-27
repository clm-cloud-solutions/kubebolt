package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/extclassify"
)

// The way in goes where Kobi looks for it. In three runs of
// alb-targets-unhealthy (2026-10-22) Kobi read the Ingress with
// get_resource_yaml, get_resource_describe or get_resource_detail, never with
// get_external_connections, and found the target group by deduction: two of
// its five connector calls, gone before the network ACL that dropped the
// traffic. A read of an Ingress or of a LoadBalancer Service now carries the
// entry get_external_connections gives for it: the address classified, and on
// EKS the target groups of its pods.

// entryReadTools are the reads of one object that get the entry.
var entryReadTools = map[string]bool{"get_resource_yaml": true, "get_resource_describe": true, "get_resource_detail": true}

const noteEntryOnRead = "entry is how requests from outside the cluster reach the pods behind this object: the address its status holds, classified, and on EKS the target groups those pods are registered in. " +
	"elbv2 DescribeTargetHealth with a TargetGroupArn says whether the load balancer reaches them, and targets that time out while the pods are Ready point at the network between the two."

// withEntryOnRead adds the entry to a successful read of an Ingress or a
// LoadBalancer Service, as the fields "entry" and "entryNote" of a JSON
// object answer.
func (h *handlers) withEntryOnRead(ctx context.Context, call copilot.ToolCall, result copilot.ToolResult) copilot.ToolResult {
	if result.IsError || !entryReadTools[call.Name] {
		return result
	}
	var in struct {
		Type      string `json:"type"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if json.Unmarshal(call.Input, &in) != nil || in.Namespace == "" || in.Name == "" {
		return result
	}
	kind := entryKind(in.Type)
	if kind == "" {
		return result
	}
	if !strings.HasPrefix(strings.TrimSpace(result.Content), "{") {
		return result
	}
	sc := h.flowScopeFor(ctx)
	if sc.conn == nil {
		return result
	}
	v := h.externalViewFor(ctx, sc)
	withTargetGroups := extclassify.NormalizeProvider(v.provider) == extclassify.ProviderAWS
	p, problem, ok := sc.conn.EntryPointFor(ctx, kind, in.Namespace, in.Name, withTargetGroups)
	if !ok {
		return result
	}
	if content, ok := withEntry(result.Content, p, problem, v); ok {
		result.Content = content
	}
	return result
}

// withEntry adds the entry of p to a JSON object answer. ok is false when
// there is nothing to add (no address yet) or the answer is not an object.
func withEntry(content string, p cluster.EntryPoint, problem string, v *externalView) (string, bool) {
	if p.Address == "" {
		return "", false
	}
	var answer map[string]any
	if json.Unmarshal([]byte(content), &answer) != nil || answer == nil {
		return "", false
	}
	answer["entry"] = entryRows([]cluster.EntryPoint{p}, v)[0]
	note := noteEntryOnRead
	if problem != "" {
		note += " Its target groups could not be read: " + problem + "."
	}
	answer["entryNote"] = note
	b, err := json.Marshal(answer)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// entryKind maps the type a read names to the kind of an entry point, "" for
// any other type.
func entryKind(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "ingresses", "ingress", "ing":
		return "Ingress"
	case "services", "service", "svc":
		return "Service"
	}
	return ""
}
