// Package mcp implements a read-only Model Context Protocol (MCP) server that
// exposes KubeBolt's Kobi tool catalogue to any MCP host (Claude Code, Cursor,
// CI/CD pipelines, other agents).
//
// The package is transport-agnostic: the core Server.HandleMessage consumes
// one JSON-RPC 2.0 message and produces one response. Two transports wrap it:
//
//   - HTTP (http.go) — the Streamable HTTP transport, mounted on the API
//     router inside the authenticated group. The request context carries the
//     resolved (tenant, cluster) RuntimeKey, so one endpoint serves every
//     tenant/cluster the API token is authorized for (works in OSS — single
//     "default" tenant — and in EE/SaaS unchanged).
//   - stdio (stdio.go) — newline-delimited JSON-RPC over stdin/stdout, driven
//     by the standalone `cmd/mcp` binary for local / single-operator use.
//
// Only read-only tools are exposed (GovernedToolDefinitions(false, false)).
// The mutating propose_* tools are withheld AND rejected at call time
// (see tools.go) so the read-only guarantee is enforced server-side, not just
// by hiding them from tools/list.
//
// We hand-roll the small JSON-RPC + MCP surface rather than pulling an SDK so
// the build stays self-contained (no new module download) and pure-Go, in
// keeping with the rest of the codebase.
package mcp

import "encoding/json"

const (
	// jsonRPCVersion is the only JSON-RPC version MCP uses.
	jsonRPCVersion = "2.0"

	// ProtocolVersion is the MCP revision this server implements. When a
	// client sends a different (but recognized) version in initialize, we
	// echo theirs back for compatibility; otherwise we answer with this one.
	ProtocolVersion = "2025-06-18"
)

// JSON-RPC 2.0 standard error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// request is a single inbound JSON-RPC message. A message with no id is a
// notification (no response is sent).
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the message is a notification (id absent).
func (r *request) isNotification() bool { return len(r.ID) == 0 }

// response is a single outbound JSON-RPC message. Exactly one of Result / Error
// is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// nullID is the id used in error responses when the request id could not be
// parsed (JSON-RPC requires id: null in that case).
var nullID = json.RawMessage("null")

// ----- MCP method payloads -----

// initializeParams is the subset of the initialize request we read. We ignore
// clientInfo and client capabilities — the server's behavior doesn't depend on
// them for a read-only tool/prompt server.
type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// InitializeResult is returned from initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

type serverCapabilities struct {
	Tools   *capability `json:"tools,omitempty"`
	Prompts *capability `json:"prompts,omitempty"`
}

// capability is the standard {listChanged} capability object. We never push
// list-changed notifications (the catalogue is static), so listChanged stays
// false, but the empty object signals the capability is supported.
type capability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo identifies this MCP server to the host.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Tool is the MCP representation of a callable tool.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type listToolsResult struct {
	Tools []Tool `json:"tools"`
}

// callToolParams is the params object of a tools/call request.
type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult is the result of a tools/call. Tool-level failures are
// reported via IsError=true with the error text in Content (so the host LLM
// can read and recover), NOT as a JSON-RPC error.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Content is a single content block. This server only emits text blocks.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// textContent wraps a string in the single-text-block slice MCP expects.
func textContent(s string) []Content {
	return []Content{{Type: "text", Text: s}}
}

// ----- prompts -----

// Prompt is the MCP representation of a reusable prompt template.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type listPromptsResult struct {
	Prompts []Prompt `json:"prompts"`
}

type getPromptParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// GetPromptResult is returned from prompts/get.
type GetPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptMessage is one message in a prompt. Role is "user" or "assistant".
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}
