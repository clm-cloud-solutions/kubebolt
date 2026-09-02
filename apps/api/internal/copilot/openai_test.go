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

// Prompted-tool-calling models (Qwen/DeepSeek via the OpenAI-compat fallback
// bucket) emit <tool_call>{json}</tool_call> inside content; we read tool calls
// from the native tool_calls field, so those blocks must not leak into the
// user-visible answer. Regression for finding #49 / incident inc_6JBGfN5JDb.
func TestStripToolCallTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no tags", "just a normal answer", "just a normal answer"},
		{"empty", "", ""},
		{
			"single block only",
			"<tool_call>\n{\"type\":\"pods\",\"name\":\"shop-web\"}\n</tool_call>",
			"",
		},
		{
			"prose plus block",
			"Here is the root cause.\n<tool_call>{\"type\":\"pods\"}</tool_call>",
			"Here is the root cause.",
		},
		{
			"two blocks",
			"<tool_call>{\"a\":1}</tool_call>mid<tool_call>{\"b\":2}</tool_call>",
			"mid",
		},
		{
			"dangling opener (truncated stream)",
			"partial answer\n<tool_call>{\"type\":\"pod",
			"partial answer",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripToolCallTags(c.in); got != c.want {
				t.Fatalf("stripToolCallTags(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
