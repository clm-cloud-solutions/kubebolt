package copilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// anthropicCapture runs one Chat against a fake Messages API and returns the
// request body it received and the parsed response.
func anthropicCapture(t *testing.T, req ChatRequest, reply string) (map[string]any, *ChatResponse) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	req.Provider.Provider = "anthropic"
	req.Provider.BaseURL = srv.URL
	req.Provider.APIKey = "test"
	if req.MaxTokens == 0 {
		req.MaxTokens = 32
	}
	req.Messages = []Message{{Role: RoleUser, Content: "hi"}}
	out, err := (&AnthropicProvider{client: srv.Client()}).Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return body, out
}

const okReply = `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn",
"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":2}}`

// A call that does not need reasoning asks for none, in the shape each model
// accepts: Haiku 5.5 takes "disabled", Sonnet 5.5 rejects it and takes
// "between_tools", Opus 5.5 takes neither — so it gets nothing, as does any
// model that does not reason by default.
func TestAnthropic_NoThinkingOnlyWhereAccepted(t *testing.T) {
	cases := []struct {
		model      string
		noThinking bool
		want       string // thinking.type in the body; "" = no thinking field
	}{
		{"claude-haiku-5-5", true, "disabled"},
		{"claude-haiku-5-5", false, ""},
		{"claude-sonnet-5-5", true, "between_tools"},
		{"claude-sonnet-5-5", false, ""},
		{"claude-opus-5-5", true, ""},
		{"claude-haiku-4-5", true, ""},
	}
	for _, c := range cases {
		body, _ := anthropicCapture(t, ChatRequest{
			Provider:   config.ProviderConfig{Model: c.model},
			NoThinking: c.noThinking,
		}, okReply)
		got := ""
		if th, ok := body["thinking"].(map[string]any); ok {
			got, _ = th["type"].(string)
		}
		if got != c.want {
			t.Errorf("%s noThinking=%v: thinking.type=%q, want %q", c.model, c.noThinking, got, c.want)
		}
	}
}

// A response that starts with a thinking block keeps working: the text and
// the tool call are read by type, the thinking block is skipped, and a call
// whose prompt passed 100K tokens is tagged for the long rate card.
func TestAnthropic_ThinkingBlockAndLongContextTag(t *testing.T) {
	reply := `{"id":"m","type":"message","role":"assistant","stop_reason":"tool_use",
"content":[{"type":"thinking","thinking":"","signature":"sig"},
{"type":"text","text":"Checking the events."},
{"type":"tool_use","id":"t1","name":"get_events","input":{"namespace":"shop"}}],
"usage":{"input_tokens":3000,"output_tokens":400,"cache_read_input_tokens":110000}}`
	_, out := anthropicCapture(t, ChatRequest{Provider: config.ProviderConfig{Model: "claude-haiku-5-5"}}, reply)
	if out.Text != "Checking the events." || len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "get_events" {
		t.Fatalf("text/tool calls must be read by type around the thinking block, got %+v", out)
	}
	if out.Usage.LongCacheReadTokens != 110_000 || out.Usage.LongInputTokens != 3_000 || out.Usage.LongOutputTokens != 400 {
		t.Errorf("a 113K-token prompt must be tagged long, got %+v", out.Usage)
	}
}

// The API splits cache writes by TTL; the adapter keeps the 1h part, which
// bills at 2x input instead of the 5-minute 1.25x.
func TestAnthropic_ReadsOneHourCacheWrites(t *testing.T) {
	reply := `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn",
"content":[{"type":"text","text":"ok"}],
"usage":{"input_tokens":4,"output_tokens":4,"cache_creation_input_tokens":3819,
"cache_creation":{"ephemeral_5m_input_tokens":1553,"ephemeral_1h_input_tokens":2266}}}`
	_, out := anthropicCapture(t, ChatRequest{Provider: config.ProviderConfig{Model: "claude-sonnet-5"}}, reply)
	if out.Usage.CacheCreationTokens != 3819 || out.Usage.CacheCreation1hTokens != 2266 {
		t.Errorf("want 3819 writes of which 2266 for 1h, got %+v", out.Usage)
	}
}
