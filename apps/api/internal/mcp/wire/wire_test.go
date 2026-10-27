package wire

import (
	"encoding/json"
	"errors"
	"testing"
)

// The types below are the server's wire structs exactly as they were before
// the extraction into this package (internal/mcp/protocol.go). The server
// marshals the wire types now; these tests prove that every payload it sends
// serializes to the same bytes, so no MCP host sees a difference.

type legacyResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *legacyRPCError `json:"error,omitempty"`
}

type legacyRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type legacyInitializeResult struct {
	ProtocolVersion string                   `json:"protocolVersion"`
	Capabilities    legacyServerCapabilities `json:"capabilities"`
	ServerInfo      legacyServerInfo         `json:"serverInfo"`
	Instructions    string                   `json:"instructions,omitempty"`
}

type legacyServerCapabilities struct {
	Tools   *legacyCapability `json:"tools,omitempty"`
	Prompts *legacyCapability `json:"prompts,omitempty"`
}

type legacyCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type legacyServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type legacyTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type legacyListToolsResult struct {
	Tools []legacyTool `json:"tools"`
}

type legacyCallToolResult struct {
	Content []legacyContent `json:"content"`
	IsError bool            `json:"isError,omitempty"`
}

type legacyContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func sameJSON(t *testing.T, name string, got, want interface{}) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: marshal new: %v", name, err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s: marshal legacy: %v", name, err)
	}
	if string(g) != string(w) {
		t.Errorf("%s: bytes changed\n got: %s\nwant: %s", name, g, w)
	}
}

func TestServerPayloadBytesUnchanged(t *testing.T) {
	schema := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"topic": map[string]interface{}{"type": "string"}}}

	sameJSON(t, "initialize",
		&Response{JSONRPC: JSONRPCVersion, ID: json.RawMessage("1"), Result: InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities:    ServerCapabilities{Tools: &Capability{}, Prompts: &Capability{}},
			ServerInfo:      ServerInfo{Name: "kubebolt-kobi", Version: "1"},
			Instructions:    "read-only",
		}},
		&legacyResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: legacyInitializeResult{
			ProtocolVersion: "2025-06-18",
			Capabilities:    legacyServerCapabilities{Tools: &legacyCapability{}, Prompts: &legacyCapability{}},
			ServerInfo:      legacyServerInfo{Name: "kubebolt-kobi", Version: "1"},
			Instructions:    "read-only",
		}})

	sameJSON(t, "tools/list",
		&Response{JSONRPC: JSONRPCVersion, ID: json.RawMessage(`"a"`), Result: ListToolsResult{Tools: []Tool{
			{Name: "get_kubebolt_docs", Description: "docs", InputSchema: schema},
			{Name: "empty", InputSchema: map[string]interface{}{"type": "object"}},
		}}},
		&legacyResponse{JSONRPC: "2.0", ID: json.RawMessage(`"a"`), Result: legacyListToolsResult{Tools: []legacyTool{
			{Name: "get_kubebolt_docs", Description: "docs", InputSchema: schema},
			{Name: "empty", InputSchema: map[string]interface{}{"type": "object"}},
		}}})

	sameJSON(t, "tools/call",
		&Response{JSONRPC: JSONRPCVersion, ID: json.RawMessage("7"), Result: CallToolResult{Content: TextContent(`{"ok":true}`), IsError: true}},
		&legacyResponse{JSONRPC: "2.0", ID: json.RawMessage("7"), Result: legacyCallToolResult{Content: []legacyContent{{Type: "text", Text: `{"ok":true}`}}, IsError: true}})

	sameJSON(t, "error",
		&Response{JSONRPC: JSONRPCVersion, ID: NullID, Error: &Error{Code: CodeParseError, Message: "parse error"}},
		&legacyResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &legacyRPCError{Code: -32700, Message: "parse error"}})

	// ping answers an empty object; omitempty on an interface{} holding
	// struct{}{} must still emit it.
	sameJSON(t, "ping",
		&Response{JSONRPC: JSONRPCVersion, ID: json.RawMessage("2"), Result: struct{}{}},
		&legacyResponse{JSONRPC: "2.0", ID: json.RawMessage("2"), Result: struct{}{}})
}

func TestIsNotification(t *testing.T) {
	var r Request
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), &r); err != nil {
		t.Fatal(err)
	}
	if !r.IsNotification() {
		t.Error("message without id must be a notification")
	}
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":0,"method":"ping"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.IsNotification() {
		t.Error("id 0 is a request, not a notification")
	}
}

// TestRemoteToolDecode covers the fields a remote server sends that the
// server-side type did not model: title, annotations, outputSchema.
func TestRemoteToolDecode(t *testing.T) {
	raw := `{"tools":[{"name":"get_instance","title":"Get","description":"d",
		"inputSchema":{"type":"object"},"outputSchema":{"type":"object","properties":{}},
		"annotations":{"readOnlyHint":true,"openWorldHint":false},"_meta":{"anthropic/alwaysLoad":true}},
		{"name":"run_script","description":"x","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false,"destructiveHint":true}},
		{"name":"bare","description":"x","inputSchema":{"type":"object"}}],"nextCursor":"p2"}`
	var res ListToolsResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if res.NextCursor != "p2" || len(res.Tools) != 3 {
		t.Fatalf("decoded %+v", res)
	}
	ro := res.Tools[0]
	if !ro.ReadOnly() || ro.Title != "Get" || len(ro.OutputSchema) == 0 {
		t.Errorf("read-only tool decoded as %+v", ro)
	}
	if ro.Annotations.OpenWorldHint == nil || *ro.Annotations.OpenWorldHint {
		t.Error("openWorldHint=false must decode as a non-nil false")
	}
	if ro.Annotations.DestructiveHint != nil {
		t.Error("absent destructiveHint must stay nil (its spec default is true)")
	}
	if res.Tools[1].ReadOnly() || res.Tools[2].ReadOnly() {
		t.Error("readOnlyHint false or absent must not be read-only")
	}
}

func TestCallToolParamsMeta(t *testing.T) {
	b, err := json.Marshal(CallToolParams{Name: "call", Arguments: json.RawMessage(`{"a":1}`), Meta: map[string]any{"AWS_REGION": "eu-west-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"name":"call","arguments":{"a":1},"_meta":{"AWS_REGION":"eu-west-1"}}` {
		t.Errorf("got %s", b)
	}
	b, _ = json.Marshal(CallToolParams{Name: "call"})
	if string(b) != `{"name":"call"}` {
		t.Errorf("empty meta/arguments must be omitted, got %s", b)
	}
}

func TestErrorIsAnError(t *testing.T) {
	var err error = &Error{Code: CodeInvalidParams, Message: "unknown tool: x"}
	var rpc *Error
	if !errors.As(err, &rpc) || rpc.Code != CodeInvalidParams {
		t.Fatal("errors.As must recover the JSON-RPC error")
	}
	if err.Error() != "jsonrpc error -32602: unknown tool: x" {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestInitializeParamsAlwaysCarriesCapabilities(t *testing.T) {
	b, _ := json.Marshal(InitializeParams{ProtocolVersion: ProtocolVersion, Capabilities: map[string]any{}, ClientInfo: Implementation{Name: "kubebolt", Version: "1"}})
	if string(b) != `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"kubebolt","version":"1"}}` {
		t.Errorf("got %s", b)
	}
}
