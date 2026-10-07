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

// Haiku 5.5 reasons by default. A call that does not need it asks for none —
// but only where the model accepts {"type":"disabled"}: Opus 5.5, Sonnet 5.5
// and Fable reject that body with a 400, so for them nothing is sent.
func TestAnthropic_NoThinkingOnlyWhereAccepted(t *testing.T) {
	cases := []struct {
		model      string
		noThinking bool
		want       bool // thinking:{type:disabled} in the body
	}{
		{"claude-haiku-5-5", true, true},
		{"claude-haiku-5-5", false, false},
		{"claude-sonnet-5-5", true, false},
		{"claude-opus-5-5", true, false},
		{"claude-haiku-4-5", true, false},
	}
	for _, c := range cases {
		body, _ := anthropicCapture(t, ChatRequest{
			Provider:   config.ProviderConfig{Model: c.model},
			NoThinking: c.noThinking,
		}, okReply)
		th, has := body["thinking"].(map[string]any)
		if has != c.want || (has && th["type"] != "disabled") {
			t.Errorf("%s noThinking=%v: thinking=%v, want disabled=%v", c.model, c.noThinking, body["thinking"], c.want)
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
