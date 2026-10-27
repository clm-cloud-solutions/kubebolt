package copilot

import (
	"encoding/json"
	"time"
)

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
	// Timestamp is when this turn happened. Stamped server-side on messages the
	// chat loop appends and on the operator message the turn answers
	// (StampTurn), and preserved from the client for the rest, so a persisted
	// conversation reconstructs its real timeline on resume / export instead
	// of collapsing to "now". The provider adapters never read it; an operator
	// message's "# Now" block is built from it (PrefixSessionNotes).
	// Optional for backward-compat.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// View is the page the operator was on when they sent this message
	// (current_view), stamped on the turn's message by StampTurn. Like
	// Timestamp it lets every operator message read the same on each later
	// turn, so the provider's cache keeps the history. Never sent to the LLM
	// on its own.
	View string `json:"view,omitempty"`
	// Trigger is the chat request's trigger on the user message it came with
	// ("insight", "action_stalled", "cluster_switch"…); empty when the operator
	// typed it. Kept on the message so a later turn still knows which words
	// were the operator's (OperatorLanguage). Never sent to the LLM.
	Trigger string `json:"trigger,omitempty"`
	// TurnID names the chat turn this message ANSWERED: stamped by the chat
	// handler on the final assistant message of a turn, the same id as the
	// turn's SessionRecord. A 👍/👎 is given against it (copilot_feedback).
	// Never sent to the LLM, like Timestamp.
	TurnID string `json:"turnId,omitempty"`
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
	// NotSent marks a connector call KubeBolt refused itself, on its own
	// allowlist, before anything reached the provider: it read nothing, so
	// the per-turn connector budget does not count it. Never stored.
	NotSent bool `json:"-"`
	// Status and DurationMs are what the chat showed for this call while it
	// ran (ToolResultOK…, ToolResultNotSent) and how long it took; stored so
	// a reloaded conversation reads the same. The provider adapters never
	// send them to the model.
	Status     string `json:"status,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	// Digest is the line the chat shows under the call once it is back:
	// what came back, counted and listed, never what it means
	// (digestToolResult; the connector bindings write their own). Computed
	// from the whole result before it is capped or stubbed, and never sent
	// to the model.
	Digest string `json:"digest,omitempty"`
	// Reason is the one line the chat shows for a call that did not read
	// (CallStatus): the error's first sentence, never the instructions a
	// result gives the model.
	Reason string `json:"reason,omitempty"`
}

// ToolDefinition describes a callable tool that the LLM can invoke.
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// StreamEvent is sent over SSE to the frontend during chat streaming.
type StreamEvent struct {
	Type     string `json:"type"` // "text" | "tool_call" | "tool_result" | "done" | "error" | "meta" | "usage"
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
	// CacheCreation1hTokens is the part of CacheCreationTokens written with a
	// ONE-HOUR TTL. Anthropic bills those at 2x base input, a 5-minute write at
	// 1.25x — and Kobi writes its static prefix (system prompt + tools) for an
	// hour (staticPrefixCache). Zero for providers without the split and for
	// records written before it.
	CacheCreation1hTokens int `json:"cacheCreation1hTokens,omitempty"`
	// ThinkingTokens is the part of OutputTokens the model spent reasoning
	// (Anthropic output_tokens_details.thinking_tokens, OpenAI
	// completion_tokens_details.reasoning_tokens). Already inside
	// OutputTokens and billed there: it only tells reasoning from answer.
	ThinkingTokens int `json:"thinkingTokens,omitempty"`

	// The part of the four totals above that came from calls billed at the
	// model's long-context rate card (ModelPricing.Long): a call whose prompt
	// passed ModelPricing.LongAbove is billed at that rate WHOLE. Which calls
	// were large is only known per call, so TagLongContext fills these where
	// the call is made and Add carries them through the sums. Zero for every
	// model with a single rate card, and for records written before them.
	LongInputTokens           int `json:"longInputTokens,omitempty"`
	LongOutputTokens          int `json:"longOutputTokens,omitempty"`
	LongCacheCreationTokens   int `json:"longCacheCreationTokens,omitempty"`
	LongCacheReadTokens       int `json:"longCacheReadTokens,omitempty"`
	LongCacheCreation1hTokens int `json:"longCacheCreation1hTokens,omitempty"`
}

// Total returns InputTokens + OutputTokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Add accumulates another Usage into this one.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheCreation1hTokens += other.CacheCreation1hTokens
	u.ThinkingTokens += other.ThinkingTokens
	u.LongInputTokens += other.LongInputTokens
	u.LongOutputTokens += other.LongOutputTokens
	u.LongCacheCreationTokens += other.LongCacheCreationTokens
	u.LongCacheReadTokens += other.LongCacheReadTokens
	u.LongCacheCreation1hTokens += other.LongCacheCreation1hTokens
}
