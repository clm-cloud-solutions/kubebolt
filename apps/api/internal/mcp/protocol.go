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

import "github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"

// The JSON-RPC and MCP wire types live in the leaf package wire, shared with
// the MCP client (internal/mcpclient). The aliases below keep this package's
// names — exported and unexported — so the server, its callers and its tests
// read exactly as before the extraction.

const (
	// jsonRPCVersion is the only JSON-RPC version MCP uses.
	jsonRPCVersion = wire.JSONRPCVersion

	// ProtocolVersion is the MCP revision this server implements. When a
	// client sends a different (but recognized) version in initialize, we
	// echo theirs back for compatibility; otherwise we answer with this one.
	ProtocolVersion = wire.ProtocolVersion
)

// JSON-RPC 2.0 standard error codes.
const (
	codeParseError     = wire.CodeParseError
	codeInvalidRequest = wire.CodeInvalidRequest
	codeMethodNotFound = wire.CodeMethodNotFound
	codeInvalidParams  = wire.CodeInvalidParams
	codeInternalError  = wire.CodeInternalError
)

type (
	// request is a single inbound JSON-RPC message. A message with no id is
	// a notification (no response is sent).
	request = wire.Request
	// response is a single outbound JSON-RPC message. Exactly one of Result
	// / Error is set.
	response = wire.Response
	rpcError = wire.Error

	// initializeParams is the initialize request; the server reads only its
	// protocolVersion — behaviour doesn't depend on clientInfo or client
	// capabilities for a read-only tool/prompt server.
	initializeParams = wire.InitializeParams
	// InitializeResult is returned from initialize.
	InitializeResult   = wire.InitializeResult
	serverCapabilities = wire.ServerCapabilities
	// capability is the standard {listChanged} capability object. We never
	// push list-changed notifications (the catalogue is static), so
	// listChanged stays false, but the empty object signals the capability
	// is supported.
	capability = wire.Capability
	// ServerInfo identifies this MCP server to the host.
	ServerInfo = wire.ServerInfo

	// Tool is the MCP representation of a callable tool.
	Tool            = wire.Tool
	listToolsResult = wire.ListToolsResult
	// callToolParams is the params object of a tools/call request.
	callToolParams = wire.CallToolParams
	// CallToolResult is the result of a tools/call. Tool-level failures are
	// reported via IsError=true with the error text in Content (so the host
	// LLM can read and recover), NOT as a JSON-RPC error.
	CallToolResult = wire.CallToolResult
	// Content is a single content block. This server only emits text blocks.
	Content = wire.Content
)

// nullID is the id used in error responses when the request id could not be
// parsed (JSON-RPC requires id: null in that case).
var nullID = wire.NullID

// textContent wraps a string in the single-text-block slice MCP expects.
func textContent(s string) []Content { return wire.TextContent(s) }

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
