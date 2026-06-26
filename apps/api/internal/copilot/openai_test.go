package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

// OpenAI rejects a missing/null content ("expected a string, got null"). That
// happened when an Anthropic-built history (tool-call turns, empty tool outputs)
// was routed to an OpenAI-compatible model (the error fallback). Every
// serialized message must carry content as a string, even "" (Anthropic
// tolerates the omission, OpenAI does not).
func TestToOpenAIMessages_AlwaysSerializesContent(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hola"},
		// Assistant turn that only called a tool — no text content.
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "list_pods", Input: json.RawMessage(`{}`)}}},
		// Tool result with empty output.
		{ToolResults: []ToolResult{{ToolCallID: "c1", Content: ""}}},
	}
	out := toOpenAIMessages("you are kobi", msgs)
	if len(out) < 4 {
		t.Fatalf("got %d messages, want >=4 (system + 3)", len(out))
	}
	for i, m := range out {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal msg %d: %v", i, err)
		}
		if !strings.Contains(string(b), `"content":`) {
			t.Errorf("msg %d (role=%s) has no content field: %s — OpenAI rejects missing content", i, m.Role, b)
		}
		if strings.Contains(string(b), `"content":null`) {
			t.Errorf("msg %d (role=%s) serialized content as null: %s", i, m.Role, b)
		}
	}
}
