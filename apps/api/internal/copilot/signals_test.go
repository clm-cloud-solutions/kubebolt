package copilot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

func TestNormalizeStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn": StopEndTurn, "stop_sequence": StopEndTurn, "stop": StopEndTurn,
		"tool_use": StopToolUse, "tool_calls": StopToolUse, "function_call": StopToolUse,
		"max_tokens": StopMaxTokens, "length": StopMaxTokens,
		"model_context_window_exceeded": StopMaxTokens,
		"refusal": StopRefusal, "content_filter": StopRefusal,
		"pause_turn": StopOther, "": StopOther, "something_new": StopOther,
	}
	for raw, want := range cases {
		if got := NormalizeStopReason(raw); got != want {
			t.Errorf("NormalizeStopReason(%q) = %q, want %q", raw, got, want)
		}
	}
}

type fakeNetErr struct{ timeout bool }

func (e fakeNetErr) Error() string   { return "net" }
func (e fakeNetErr) Timeout() bool   { return e.timeout }
func (e fakeNetErr) Temporary() bool { return false }

func TestClassifyProviderError(t *testing.T) {
	httpErr := func(code int, body string) error {
		return &ProviderHTTPError{StatusCode: code, Provider: "anthropic", Body: body}
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"429", httpErr(429, `{"type":"error","error":{"type":"rate_limit_error"}}`), ProviderErrRateLimit},
		{"529", httpErr(529, `{"type":"error","error":{"type":"overloaded_error"}}`), ProviderErrOverloaded},
		{"503 overloaded body", httpErr(503, `{"error":{"type":"overloaded_error"}}`), ProviderErrOverloaded},
		{"500", httpErr(500, `{"error":{"type":"api_error"}}`), ProviderErrServer},
		{"504", httpErr(504, ""), ProviderErrTimeout},
		{"401", httpErr(401, ""), ProviderErrAuth},
		{"403", httpErr(403, ""), ProviderErrAuth},
		{"404", httpErr(404, ""), ProviderErrNotFound},
		{"400", httpErr(400, `{"error":{"type":"invalid_request_error"}}`), ProviderErrBadRequest},
		{"wrapped 429", fmt.Errorf("round 3: %w", httpErr(429, "")), ProviderErrRateLimit},
		{"client timeout", &url.Error{Op: "Post", URL: "https://x", Err: fakeNetErr{timeout: true}}, ProviderErrTimeout},
		{"deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), ProviderErrTimeout},
		{"refused", &net.OpError{Op: "dial", Err: fakeNetErr{}}, ProviderErrNetwork},
		{"parse", errors.New("parse response: unexpected EOF"), ProviderErrOther},
	}
	for _, c := range cases {
		if got := ClassifyProviderError(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := ClassifyProviderError(nil); got != "" {
		t.Errorf("nil error classified as %q", got)
	}
}

// A call that returns after the client left counts nowhere: the cancel is the
// client's, and counting it as a timeout would page someone for a closed tab.
func TestCallSignals_Observe(t *testing.T) {
	var s CallSignals
	s.Observe(&ChatResponse{StopReason: "tool_use"}, nil, false)
	s.Observe(&ChatResponse{StopReason: "end_turn"}, nil, false)
	s.Observe(&ChatResponse{StopReason: "refusal"}, nil, false)
	s.Observe(nil, &ProviderHTTPError{StatusCode: 429}, false)
	s.Observe(nil, &url.Error{Op: "Post", URL: "https://x", Err: context.Canceled}, true)

	want := map[string]int{StopToolUse: 1, StopEndTurn: 1, StopRefusal: 1}
	if fmt.Sprint(s.StopReasons) != fmt.Sprint(want) {
		t.Errorf("StopReasons = %v, want %v", s.StopReasons, want)
	}
	if fmt.Sprint(s.ProviderErrors) != fmt.Sprint(map[string]int{ProviderErrRateLimit: 1}) {
		t.Errorf("ProviderErrors = %v, want only the 429", s.ProviderErrors)
	}
}

// The executor has no structured status, so the shapes below are the ones it
// actually returns (executor.go), not invented ones.
func TestClassifyToolResult(t *testing.T) {
	cases := []struct {
		name string
		r    ToolResult
		want string
	}{
		{"list with items", ToolResult{Content: `{"items":[{"name":"web"}],"total":1}`}, ToolResultOK},
		{"empty list", ToolResult{Content: `{"items":[],"total":0}`}, ToolResultEmpty},
		{"empty events", ToolResult{Content: `{"events":[]}`}, ToolResultEmpty},
		{"empty logs", ToolResult{Content: `{"logs":""}`}, ToolResultEmpty},
		{"empty array", ToolResult{Content: `[]`}, ToolResultEmpty},
		{"nothing", ToolResult{Content: "  "}, ToolResultEmpty},
		{"plain text", ToolResult{Content: "line 1\nline 2"}, ToolResultOK},
		{"truncated", ToolResult{Content: `{"truncated_result":"…","notice":"cut"}`}, ToolResultOK},
		{"forbidden", ToolResult{IsError: true,
			Content: `{"error":"forbidden: insufficient permissions to access secrets","forbidden":true}`}, ToolResultDenied},
		{"not found", ToolResult{IsError: true, Content: `{"error":"deployment \"web\" not found"}`}, ToolResultNotFound},
		{"timeout", ToolResult{IsError: true, Content: `{"error":"context deadline exceeded"}`}, ToolResultTimeout},
		{"needs proxy", ToolResult{IsError: true,
			Content: `{"error":"This needs live cluster access. Enable the KubeBolt agent-proxy","needsProxy":true}`}, ToolResultError},
		{"raw error text", ToolResult{IsError: true, Content: "exec failed: exit code 2"}, ToolResultError},
		{"not installed", ToolResult{Content: `{"notInstalled":true,"message":"Trivy is not installed"}`}, ToolResultNotFound},
		{"error key without flag", ToolResult{Content: `{"error":"pod \"x\" not found"}`}, ToolResultNotFound},
	}
	for _, c := range cases {
		if got := ClassifyToolResult(c.r); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestToolSource(t *testing.T) {
	cases := map[string]string{
		"get_pod_logs":                     ToolSourceKubeBolt,
		"mcp__aws__cloudwatch_query":       "mcp:aws",
		"mcp__gcp-logging__list_log_entry": "mcp:gcp-logging",
		"mcp__":                            ToolSourceKubeBolt,
		"mcp__broken":                      ToolSourceKubeBolt,
	}
	for name, want := range cases {
		if got := ToolSource(name); got != want {
			t.Errorf("ToolSource(%q) = %q, want %q", name, got, want)
		}
	}
}

// The thinking count is read from the field the live API sends
// (output_tokens_details.thinking_tokens), and stays inside output_tokens.
func TestAnthropic_ThinkingTokensParsed(t *testing.T) {
	reply := `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn",
"content":[{"type":"text","text":"ok"}],
"usage":{"input_tokens":10,"output_tokens":250,"output_tokens_details":{"thinking_tokens":198}}}`
	_, out := anthropicCapture(t, ChatRequest{Provider: config.ProviderConfig{Model: "claude-haiku-5-5"}}, reply)
	if out.Usage.ThinkingTokens != 198 || out.Usage.OutputTokens != 250 {
		t.Errorf("usage = %+v, want thinking 198 inside output 250", out.Usage)
	}
}

func TestOpenAI_ReasoningTokensParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
"usage":{"prompt_tokens":100,"completion_tokens":80,"total_tokens":180,
"completion_tokens_details":{"reasoning_tokens":64}}}`))
	}))
	t.Cleanup(srv.Close)
	out, err := GetProvider("openai").Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Provider: config.ProviderConfig{Provider: "openai", Model: "gpt-5", APIKey: "k", BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.Usage.ThinkingTokens != 64 || out.Usage.OutputTokens != 80 {
		t.Errorf("usage = %+v, want reasoning 64 inside completion 80", out.Usage)
	}
	if out.StopReason != "stop" || NormalizeStopReason(out.StopReason) != StopEndTurn {
		t.Errorf("stop reason %q should normalize to end_turn", out.StopReason)
	}
}

func TestUsageAdd_CarriesThinkingTokens(t *testing.T) {
	u := Usage{OutputTokens: 10, ThinkingTokens: 4}
	u.Add(Usage{OutputTokens: 5, ThinkingTokens: 3})
	if u.ThinkingTokens != 7 {
		t.Errorf("ThinkingTokens = %d, want 7", u.ThinkingTokens)
	}
}
