package mcp

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrUnknownTool is returned by a ToolProvider when asked to call a tool it
// does not expose (e.g. a withheld mutating tool). The server maps it to a
// JSON-RPC InvalidParams error.
var ErrUnknownTool = errors.New("unknown tool")

// ErrUnknownPrompt is the prompts/get analogue of ErrUnknownTool.
var ErrUnknownPrompt = errors.New("unknown prompt")

// ToolProvider supplies the tool catalogue and executes tool calls. It is the
// only coupling between the MCP transport layer and KubeBolt's cluster
// connector; the concrete implementation lives in tools.go.
type ToolProvider interface {
	// ListTools returns the tools advertised to the host. Must be stable
	// for the life of a session.
	ListTools() []Tool
	// CallTool executes the named tool with the given raw JSON arguments,
	// resolving cluster access from ctx. Returning ErrUnknownTool signals a
	// protocol-level InvalidParams; any other returned error becomes an
	// internal error. Tool-execution failures (cluster errors, forbidden,
	// etc.) are reported via CallToolResult.IsError, not the error return.
	CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error)
}

// PromptProvider supplies reusable prompts. Optional — pass nil to NewServer
// to omit prompt support (the prompts capability is then not advertised and
// prompts/* methods return MethodNotFound).
type PromptProvider interface {
	ListPrompts() []Prompt
	GetPrompt(name string, args map[string]string) (GetPromptResult, error)
}

// Server dispatches MCP JSON-RPC messages. It is transport-agnostic and
// stateless across messages, so a single Server instance is safe for
// concurrent use by the HTTP transport (each request is independent) and the
// stdio transport (serialized).
type Server struct {
	info    ServerInfo
	tools   ToolProvider
	prompts PromptProvider // optional
}

// NewServer builds a Server. tools is required; prompts may be nil.
func NewServer(info ServerInfo, tools ToolProvider, prompts PromptProvider) *Server {
	return &Server{info: info, tools: tools, prompts: prompts}
}

// HandleMessage parses a single JSON-RPC message and returns the marshaled
// response bytes. It returns (nil, nil) when the message is a notification
// (nothing to send back). It never returns a non-nil error for protocol-level
// problems — those are encoded as JSON-RPC error responses — so transports can
// treat a nil byte slice as "no reply" and otherwise just write the bytes.
func (s *Server) HandleMessage(ctx context.Context, raw []byte) ([]byte, error) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return marshalResponse(errorResponse(nullID, codeParseError, "parse error: "+err.Error()))
	}
	if req.JSONRPC != jsonRPCVersion {
		// Be lenient on a missing version, strict on a wrong one.
		if req.JSONRPC != "" {
			return marshalResponse(errorResponse(idOrNull(req.ID), codeInvalidRequest, "unsupported jsonrpc version"))
		}
	}

	// Notifications get handled for side effects (none today) and produce no
	// response.
	if req.isNotification() {
		return nil, nil
	}

	result, rpcErr := s.dispatch(ctx, &req)
	if rpcErr != nil {
		return marshalResponse(&response{JSONRPC: jsonRPCVersion, ID: idOrNull(req.ID), Error: rpcErr})
	}
	return marshalResponse(&response{JSONRPC: jsonRPCVersion, ID: idOrNull(req.ID), Result: result})
}

// dispatch routes a parsed request to its handler, returning either a result
// payload or a JSON-RPC error.
func (s *Server) dispatch(ctx context.Context, req *request) (interface{}, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params), nil
	case "ping":
		// Liveness check — empty result object.
		return struct{}{}, nil
	case "tools/list":
		return listToolsResult{Tools: s.tools.ListTools()}, nil
	case "tools/call":
		return s.handleCallTool(ctx, req.Params)
	case "prompts/list":
		if s.prompts == nil {
			return nil, &rpcError{Code: codeMethodNotFound, Message: "prompts are not supported"}
		}
		return listPromptsResult{Prompts: s.prompts.ListPrompts()}, nil
	case "prompts/get":
		if s.prompts == nil {
			return nil, &rpcError{Code: codeMethodNotFound, Message: "prompts are not supported"}
		}
		return s.handleGetPrompt(req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

func (s *Server) handleInitialize(params json.RawMessage) InitializeResult {
	var p initializeParams
	_ = json.Unmarshal(params, &p) // tolerate empty/garbage params

	// Echo the client's protocol version when it sent one, so a client on a
	// slightly older/newer recognized revision keeps talking to us; otherwise
	// advertise our own.
	version := p.ProtocolVersion
	if version == "" {
		version = ProtocolVersion
	}

	caps := serverCapabilities{Tools: &capability{}}
	if s.prompts != nil {
		caps.Prompts = &capability{}
	}
	return InitializeResult{
		ProtocolVersion: version,
		Capabilities:    caps,
		ServerInfo:      s.info,
		Instructions: "KubeBolt Kobi (read-only). Use these tools to inspect a live " +
			"Kubernetes cluster: overview, resources, YAML, describe, pod logs, " +
			"events, insights, topology, and time-series metrics. All tools are " +
			"read-only; no mutations are possible through this server.",
	}
}

func (s *Server) handleCallTool(ctx context.Context, params json.RawMessage) (interface{}, *rpcError) {
	var p callToolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call requires a tool name"}
	}

	res, err := s.tools.CallTool(ctx, p.Name, p.Arguments)
	if err != nil {
		if errors.Is(err, ErrUnknownTool) {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
		}
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	return res, nil
}

func (s *Server) handleGetPrompt(params json.RawMessage) (interface{}, *rpcError) {
	var p getPromptParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid prompts/get params: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "prompts/get requires a prompt name"}
	}
	res, err := s.prompts.GetPrompt(p.Name, p.Arguments)
	if err != nil {
		if errors.Is(err, ErrUnknownPrompt) {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown prompt: " + p.Name}
		}
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	return res, nil
}

// ----- helpers -----

func errorResponse(id json.RawMessage, code int, msg string) *response {
	return &response{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func marshalResponse(resp *response) ([]byte, error) {
	return json.Marshal(resp)
}

// idOrNull returns the request id, or JSON null when absent, so every response
// carries a valid id field per JSON-RPC.
func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return nullID
	}
	return id
}
