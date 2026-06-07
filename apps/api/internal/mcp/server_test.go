package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeToolProvider is a deterministic ToolProvider for transport/dispatch
// tests — no cluster involved.
type fakeToolProvider struct {
	tools     []Tool
	lastName  string
	lastArgs  json.RawMessage
	lastCtxOK bool
	result    CallToolResult
	err       error
}

func (f *fakeToolProvider) ListTools() []Tool { return f.tools }

func (f *fakeToolProvider) CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error) {
	f.lastName = name
	f.lastArgs = args
	f.lastCtxOK = ctx != nil
	if f.err != nil {
		return CallToolResult{}, f.err
	}
	return f.result, nil
}

func newTestServer(tp ToolProvider) *Server {
	return NewServer(ServerInfo{Name: "test", Version: "0"}, tp, NewKobiPromptProvider())
}

// decode unmarshals a server response into a generic map for assertions.
func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("response is not valid JSON: %v\nraw: %s", err, raw)
	}
	return m
}

func call(t *testing.T, s *Server, msg string) []byte {
	t.Helper()
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage returned transport error: %v", err)
	}
	return resp
}

func TestInitialize(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	m := decode(t, resp)

	if m["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", m["jsonrpc"])
	}
	result, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result object: %s", resp)
	}
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want echoed 2025-06-18", result["protocolVersion"])
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities missing tools: %v", caps)
	}
	if _, ok := caps["prompts"]; !ok {
		t.Errorf("capabilities missing prompts (prompt provider was set): %v", caps)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "test" {
		t.Errorf("serverInfo.name = %v, want test", info["name"])
	}
}

func TestInitializeDefaultsProtocolVersion(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	result := decode(t, resp)["result"].(map[string]any)
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want default %s", result["protocolVersion"], ProtocolVersion)
	}
}

func TestInitializeWithoutPromptProvider(t *testing.T) {
	s := NewServer(ServerInfo{Name: "t", Version: "0"}, &fakeToolProvider{}, nil)
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	caps := decode(t, resp)["result"].(map[string]any)["capabilities"].(map[string]any)
	if _, ok := caps["prompts"]; ok {
		t.Errorf("prompts capability advertised with no prompt provider: %v", caps)
	}
	// prompts/list must then be method-not-found.
	resp = call(t, s, `{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`)
	if errObj, ok := decode(t, resp)["error"].(map[string]any); !ok || errObj["code"].(float64) != codeMethodNotFound {
		t.Errorf("prompts/list without provider should be MethodNotFound, got %s", resp)
	}
}

func TestPing(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{"jsonrpc":"2.0","id":"abc","method":"ping"}`)
	m := decode(t, resp)
	if m["id"] != "abc" {
		t.Errorf("id = %v, want echoed abc", m["id"])
	}
	if _, ok := m["result"]; !ok {
		t.Errorf("ping should return a result: %s", resp)
	}
}

func TestToolsList(t *testing.T) {
	tp := &fakeToolProvider{tools: []Tool{
		{Name: "a", Description: "tool a", InputSchema: map[string]interface{}{"type": "object"}},
		{Name: "b", Description: "tool b", InputSchema: map[string]interface{}{"type": "object"}},
	}}
	s := newTestServer(tp)
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result := decode(t, resp)["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2: %s", len(tools), resp)
	}
}

func TestToolsCallSuccess(t *testing.T) {
	tp := &fakeToolProvider{result: CallToolResult{Content: textContent(`{"ok":true}`)}}
	s := newTestServer(tp)
	resp := call(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_cluster_overview","arguments":{"x":1}}}`)
	result := decode(t, resp)["result"].(map[string]any)

	if tp.lastName != "get_cluster_overview" {
		t.Errorf("provider got name %q", tp.lastName)
	}
	if string(tp.lastArgs) != `{"x":1}` {
		t.Errorf("provider got args %s", tp.lastArgs)
	}
	if !tp.lastCtxOK {
		t.Errorf("provider received nil context")
	}
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("want 1 content block, got %d: %s", len(content), resp)
	}
	first := content[0].(map[string]any)
	if first["type"] != "text" || first["text"] != `{"ok":true}` {
		t.Errorf("unexpected content block: %v", first)
	}
	if _, hasErr := result["isError"]; hasErr {
		t.Errorf("isError should be omitted on success: %s", resp)
	}
}

func TestToolsCallToolErrorIsResultNotRPCError(t *testing.T) {
	// An executor-level failure (e.g. cluster not connected) is surfaced via
	// isError=true in the RESULT, never as a JSON-RPC error, so the host LLM
	// can read it.
	tp := &fakeToolProvider{result: CallToolResult{Content: textContent(`{"error":"cluster not connected"}`), IsError: true}}
	s := newTestServer(tp)
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_insights"}}`)
	m := decode(t, resp)
	if _, ok := m["error"]; ok {
		t.Fatalf("tool error must not be a JSON-RPC error: %s", resp)
	}
	result := m["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("isError = %v, want true: %s", result["isError"], resp)
	}
}

func TestToolsCallUnknownToolIsRPCError(t *testing.T) {
	tp := &fakeToolProvider{err: ErrUnknownTool}
	s := newTestServer(tp)
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"propose_delete_resource"}}`)
	errObj, ok := decode(t, resp)["error"].(map[string]any)
	if !ok {
		t.Fatalf("unknown tool should be a JSON-RPC error: %s", resp)
	}
	if errObj["code"].(float64) != codeInvalidParams {
		t.Errorf("code = %v, want InvalidParams", errObj["code"])
	}
}

func TestToolsCallMissingName(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`)
	errObj, ok := decode(t, resp)["error"].(map[string]any)
	if !ok || errObj["code"].(float64) != codeInvalidParams {
		t.Errorf("missing name should be InvalidParams: %s", resp)
	}
}

func TestUnknownMethod(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	errObj, ok := decode(t, resp)["error"].(map[string]any)
	if !ok || errObj["code"].(float64) != codeMethodNotFound {
		t.Errorf("unknown method should be MethodNotFound: %s", resp)
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp, err := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("notification must produce no response, got: %s", resp)
	}
}

func TestParseError(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{not valid json`)
	m := decode(t, resp)
	errObj, ok := m["error"].(map[string]any)
	if !ok || errObj["code"].(float64) != codeParseError {
		t.Errorf("malformed JSON should be ParseError: %s", resp)
	}
	if m["id"] != nil {
		t.Errorf("parse error id should be null, got %v", m["id"])
	}
}

func TestWrongJSONRPCVersion(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{"jsonrpc":"1.0","id":1,"method":"ping"}`)
	errObj, ok := decode(t, resp)["error"].(map[string]any)
	if !ok || errObj["code"].(float64) != codeInvalidRequest {
		t.Errorf("wrong jsonrpc version should be InvalidRequest: %s", resp)
	}
}

func TestUnknownPromptIsRPCError(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	resp := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"nope"}}`)
	errObj, ok := decode(t, resp)["error"].(map[string]any)
	if !ok || errObj["code"].(float64) != codeInvalidParams {
		t.Errorf("unknown prompt should be InvalidParams: %s", resp)
	}
	// Sanity: the provider's GetPrompt returns ErrUnknownPrompt for unknown names.
	if _, err := (&KobiPromptProvider{}).GetPrompt("nope", nil); !errors.Is(err, ErrUnknownPrompt) {
		t.Errorf("GetPrompt(nope) err = %v, want ErrUnknownPrompt", err)
	}
}
