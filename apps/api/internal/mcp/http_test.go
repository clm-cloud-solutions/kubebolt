package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPHandlerPostReturnsJSON(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	h := Handler(s)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	m := decode(t, rec.Body.Bytes())
	if _, ok := m["result"]; !ok {
		t.Errorf("ping over HTTP should return a result: %s", rec.Body.String())
	}
}

func TestHTTPHandlerNotificationReturns202(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	h := Handler(s)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202 for a notification", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification response should have empty body, got %q", rec.Body.String())
	}
}

func TestHTTPHandlerGetNotAllowed(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	h := Handler(s)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Errorf("Allow = %q, want POST", allow)
	}
}

func TestHTTPHandlerContextFlowsToTool(t *testing.T) {
	tp := &fakeToolProvider{result: CallToolResult{Content: textContent("{}")}}
	s := newTestServer(tp)
	h := Handler(s)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_cluster_overview"}}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if !tp.lastCtxOK {
		t.Error("request context did not flow into the tool provider")
	}
	if tp.lastName != "get_cluster_overview" {
		t.Errorf("tool name = %q", tp.lastName)
	}
}
