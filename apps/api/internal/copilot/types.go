package copilot

import "encoding/json"

// Role represents the message author.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message is a single conversation turn.
type Message struct {
	Role        Role         `json:"role"`
	Content     string       `json:"content,omitempty"`
	ToolCalls   []ToolCall   `json:"toolCalls,omitempty"`
	ToolResults []ToolResult `json:"toolResults,omitempty"`
}

// ToolCall represents an LLM-issued request to invoke a tool.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is the response from executing a tool call.
type ToolResult struct {
	ToolCallID string `json:"toolCallId"`
	Content    string `json:"content"`
	IsError    bool   `json:"isError,omitempty"`
}

// ToolDefinition describes a callable tool that the LLM can invoke.
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// StreamEvent is sent over SSE to the frontend during chat streaming.
type StreamEvent struct {
	Type     string `json:"type"`               // "text" | "tool_call" | "tool_result" | "done" | "error" | "meta" | "usage"
	Text     string `json:"text,omitempty"`
	ToolName string `json:"toolName,omitempty"`
	Error    string `json:"error,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
}

// Usage captures token consumption reported by the LLM provider for a single
// Chat call. Cache tokens apply to providers that support prompt caching
// (Anthropic); they are included in InputTokens when the provider counts
// them that way, otherwise tracked separately for cost attribution.
type Usage struct {
	InputTokens         int `json:"inputTokens"`
	OutputTokens        int `json:"outputTokens"`
	CacheCreationTokens int `json:"cacheCreationTokens,omitempty"`
	CacheReadTokens     int `json:"cacheReadTokens,omitempty"`
}

// Total returns InputTokens + OutputTokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Add accumulates another Usage into this one.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.CacheReadTokens += other.CacheReadTokens
}
