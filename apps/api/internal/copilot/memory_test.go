package copilot

import "testing"

func TestApproxTokens_Empty(t *testing.T) {
	if got := ApproxTokens(nil); got != 0 {
		t.Errorf("want 0 for nil messages, got %d", got)
	}
	if got := ApproxTokens([]Message{}); got != 0 {
		t.Errorf("want 0 for empty messages, got %d", got)
	}
}

func TestApproxTokens_CountsContent(t *testing.T) {
	// 4 chars per token heuristic. 40 chars → ~10 tokens.
	msgs := []Message{
		{Role: RoleUser, Content: "0123456789012345678901234567890123456789"}, // 40 chars
	}
	got := ApproxTokens(msgs)
	if got != 10 {
		t.Errorf("want 10 tokens for 40 chars, got %d", got)
	}
}

func TestApproxTokens_CountsToolCallsAndResults(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{Name: "get_pods", Input: []byte("{\"ns\":\"default\"}")},
		}},
		{Role: RoleUser, ToolResults: []ToolResult{
			{ToolCallID: "t1", Content: "1234567890123456"}, // 16 chars
		}},
	}
	// name=8 + input=16 = 24, tool_result=16 → 40 chars → 10 tokens
	if got := ApproxTokens(msgs); got != 10 {
		t.Errorf("want 10 tokens, got %d", got)
	}
}

func TestApproxTokens_SkipsEmptyFields(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: ""},
		{Role: RoleAssistant, Content: ""},
	}
	if got := ApproxTokens(msgs); got != 0 {
		t.Errorf("want 0 for empty content, got %d", got)
	}
}
