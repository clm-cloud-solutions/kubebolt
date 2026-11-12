package mcpgateway

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
)

// resultBlocked: the platform's blocked list stopped the call (debt-09).
const resultBlocked = "blocked"

// operationOf names the operation a call targets: what the binding says for a
// router tool (AWS `service.operation`, the Azure command), else the tool.
func operationOf(b mcpcatalog.Binding, tool string, args json.RawMessage) string {
	if n, ok := b.(mcpcatalog.OperationNamer); ok {
		if op := n.OperationOf(tool, args); op != "" {
			return op
		}
	}
	return tool
}

// blockedByPlatform applies the platform's emergency list of blocked
// operations (debt-09) before the provider and before the org's rate budget,
// for Kobi and Autopilot alike. It fails closed: when the list cannot be read
// the call is refused, so the switch never opens because the store failed.
// A refusal reads as not sent: it reached nothing and costs no budget.
func (g *Gateway) blockedByPlatform(rec *callRecord, def *mcpcatalog.ConnectorDef, b mcpcatalog.Binding, tool string, args json.RawMessage) (copilot.ToolResult, bool) {
	if g.st == nil {
		return copilot.ToolResult{}, false
	}
	op := operationOf(b, tool, args)
	blocked, err := g.st.OperationBlocked(def.ID, op)
	if err != nil {
		g.log.Warn("mcp gateway: could not read the blocked operations; refusing the call",
			slog.String("org", rec.org), slog.String("connector", def.ID), slog.String("error", err.Error()))
		rec.result, rec.reason = resultUnavailable, "blocked operations unreadable"
		return errResult(fmt.Sprintf("The %s connector is unavailable right now. Continue without it.", def.Name)), true
	}
	if !blocked {
		return copilot.ToolResult{}, false
	}
	rec.result, rec.reason = resultBlocked, "blocked by the platform"
	rec.extra = map[string]any{"operation": op}
	return copilot.ToolResult{
		Content: fmt.Sprintf("Refused: %s is blocked on this KubeBolt installation by its administrators. Do not try it again; continue without it.", op),
		IsError: true,
		NotSent: true,
	}, true
}
