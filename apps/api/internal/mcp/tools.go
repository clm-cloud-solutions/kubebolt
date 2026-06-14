package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// ExecutorToolProvider adapts a copilot.Executor to the MCP ToolProvider,
// exposing ONLY Kobi's read-only tool catalogue.
//
// Read-only is enforced two ways:
//  1. ListTools returns GovernedToolDefinitions(false, false), which withholds
//     every propose_* (mutating) tool from the host.
//  2. CallTool checks the requested name against an allow-list built from the
//     same source before touching the executor — so even a client that calls
//     a mutating tool by name (bypassing tools/list) is rejected. This is the
//     real guarantee; (1) alone is just discovery hygiene.
type ExecutorToolProvider struct {
	exec    *copilot.Executor
	allowed map[string]struct{}
	defs    []copilot.ToolDefinition
}

// NewExecutorToolProvider builds the provider over an executor. The read-only
// catalogue is snapshotted once at construction — it's static for the process.
func NewExecutorToolProvider(exec *copilot.Executor) *ExecutorToolProvider {
	defs := copilot.GovernedToolDefinitions(false, false) // read-only catalogue
	allowed := make(map[string]struct{}, len(defs))
	for _, d := range defs {
		allowed[d.Name] = struct{}{}
	}
	return &ExecutorToolProvider{exec: exec, allowed: allowed, defs: defs}
}

// ListTools returns the read-only catalogue mapped to MCP Tool shape.
func (p *ExecutorToolProvider) ListTools() []Tool {
	return toolsFromDefinitions(p.defs)
}

// CallTool dispatches to the executor after enforcing the read-only allow-list.
func (p *ExecutorToolProvider) CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error) {
	if _, ok := p.allowed[name]; !ok {
		// Withheld or non-existent tool. Surface as a protocol error so the
		// host doesn't silently believe a mutation might have run.
		return CallToolResult{}, ErrUnknownTool
	}
	call := copilot.ToolCall{
		ID:    nextCallID(),
		Name:  name,
		Input: normalizeArgs(args),
	}
	res := p.exec.ExecuteCtx(ctx, call)
	return CallToolResult{
		Content: textContent(res.Content),
		IsError: res.IsError,
	}, nil
}

// toolsFromDefinitions maps copilot tool definitions to MCP Tool values.
func toolsFromDefinitions(defs []copilot.ToolDefinition) []Tool {
	out := make([]Tool, 0, len(defs))
	for _, d := range defs {
		schema := d.InputSchema
		if schema == nil {
			// MCP requires inputSchema to be a JSON Schema object; default to
			// an empty object schema for safety.
			schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		out = append(out, Tool{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: schema,
		})
	}
	return out
}

// normalizeArgs ensures the executor always receives a JSON object. MCP allows
// arguments to be omitted; the executor's parser expects a JSON object, so we
// substitute an empty object when arguments are absent or JSON null.
func normalizeArgs(args json.RawMessage) json.RawMessage {
	trimmed := trimSpace(args)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return json.RawMessage("{}")
	}
	return args
}

// trimSpace strips leading/trailing JSON whitespace without allocating for the
// common already-trimmed case.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isJSONSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isJSONSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// callIDCounter backs nextCallID. The id is internal bookkeeping only — it
// becomes copilot.ToolResult.ToolCallID, which MCP never surfaces — so a
// process-local monotonic counter is sufficient and cheap.
var callIDCounter atomic.Uint64

func nextCallID() string {
	return "mcp-" + strconv.FormatUint(callIDCounter.Add(1), 10)
}
