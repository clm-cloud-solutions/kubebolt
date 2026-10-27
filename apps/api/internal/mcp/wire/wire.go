// Package wire holds the JSON-RPC 2.0 and Model Context Protocol message
// types shared by KubeBolt's own MCP server (internal/mcp) and its MCP client
// (internal/mcpclient).
//
// It is a leaf package on purpose — it imports only encoding/json — so the
// client can speak the same wire format as the server without importing the
// server, which pulls in the copilot executor and, through it, the cluster
// manager. Both sides share one definition of every message, which keeps the
// client honest in the tests that run it against the real server.
//
// The server marshals these types, so every field added for the client's
// sake is omitempty: the bytes KubeBolt's /mcp answers with must not change
// because a client learned to read more of the protocol.
package wire

import (
	"encoding/json"
	"strconv"
)

const (
	// JSONRPCVersion is the only JSON-RPC version MCP uses.
	JSONRPCVersion = "2.0"

	// ProtocolVersion is the MCP revision KubeBolt implements, as a server
	// and as the version its client asks for in initialize.
	ProtocolVersion = "2025-06-18"
)

// JSON-RPC 2.0 standard error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// NullID is the id used in error responses when the request id could not be
// parsed (JSON-RPC requires id: null in that case).
var NullID = json.RawMessage("null")

// Request is a single JSON-RPC message sent to a server. A message with no id
// is a notification (no response is sent).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the message is a notification (id absent).
func (r *Request) IsNotification() bool { return len(r.ID) == 0 }

// Response is a single outbound JSON-RPC message. Exactly one of Result /
// Error is set. Result stays interface{} because the server hands it any
// payload type to marshal; a client decodes into RawResponse instead.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// RawResponse is the decoding twin of Response: Result is kept as raw JSON so
// the caller unmarshals it into the payload type the method implies (decoding
// into interface{} would yield maps and lose the typed shape).
type RawResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object. It implements error so a client can
// return the server's answer as is and callers can errors.As it back.
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return "jsonrpc error " + strconv.Itoa(e.Code) + ": " + e.Message
}

// ----- MCP method payloads -----

// Implementation names a client or a server (clientInfo / serverInfo).
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// ServerInfo identifies an MCP server to the host.
type ServerInfo = Implementation

// InitializeParams is the params object of an initialize request. The server
// reads only ProtocolVersion; the client fills all of it. Capabilities is not
// omitempty because the spec requires the object, even when empty.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      Implementation `json:"clientInfo"`
}

// InitializeResult is returned from initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ServerCapabilities is the subset of server capabilities KubeBolt emits or
// reads. Unknown capabilities in a remote server's answer are ignored.
type ServerCapabilities struct {
	Tools     *Capability `json:"tools,omitempty"`
	Prompts   *Capability `json:"prompts,omitempty"`
	Resources *Capability `json:"resources,omitempty"`
}

// Capability is the standard {listChanged} capability object. An empty object
// signals the capability is supported.
type Capability struct {
	ListChanged bool `json:"listChanged,omitempty"`
	Subscribe   bool `json:"subscribe,omitempty"`
}

// Tool is the MCP representation of a callable tool. The fields after
// InputSchema exist for the client: remote servers annotate their tools, and
// dropping the annotations on decode would silently lose readOnlyHint.
type Tool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"inputSchema"`
	Title        string                 `json:"title,omitempty"`
	OutputSchema json.RawMessage        `json:"outputSchema,omitempty"`
	Annotations  *ToolAnnotations       `json:"annotations,omitempty"`
}

// ReadOnly reports whether the server declared the tool read-only. A missing
// hint is not read-only: the spec's default for readOnlyHint is false.
func (t *Tool) ReadOnly() bool {
	return t.Annotations != nil && t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint
}

// ToolAnnotations are the server's hints about a tool's behaviour. They are
// pointers because absent and false mean different things (an absent
// destructiveHint defaults to true in the spec).
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// ListToolsParams is the params object of a tools/list request.
type ListToolsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// ListToolsResult is the result of tools/list. NextCursor is set when the
// server paginates.
type ListToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// CallToolParams is the params object of a tools/call request. Meta carries
// request metadata some servers read (the AWS MCP Server takes the target
// region from _meta.AWS_REGION).
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      map[string]any  `json:"_meta,omitempty"`
}

// CallToolResult is the result of a tools/call. Tool-level failures are
// reported via IsError=true with the error text in Content (so the host LLM
// can read and recover), NOT as a JSON-RPC error.
type CallToolResult struct {
	Content           []Content       `json:"content"`
	IsError           bool            `json:"isError,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
}

// Content is a single content block. Only text blocks are modelled: a block
// of another type (image, resource) decodes with its Type and an empty Text,
// and consumers must treat it as omitted rather than as empty text.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextContent wraps a string in the single-text-block slice MCP expects.
func TextContent(s string) []Content {
	return []Content{{Type: "text", Text: s}}
}
