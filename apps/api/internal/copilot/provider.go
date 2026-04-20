package copilot

import (
	"context"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Provider is the interface implemented by every LLM provider adapter.
//
// Chat sends a list of messages plus tool definitions and returns a channel
// that emits StreamEvents until the conversation is complete. The provider
// is responsible for streaming text deltas, tool_call events, and finally a
// "done" event when the model finishes.
//
// Tool execution is handled by the caller (the chat handler), not the
// provider. The provider only emits tool_call events; the caller invokes
// the tool, appends the result, and calls Chat again.
type Provider interface {
	// Name returns a stable identifier (e.g. "anthropic", "openai").
	Name() string

	// Chat performs a single round-trip with the LLM. It returns either a
	// final text response, a list of tool calls (which the caller must
	// execute), or an error.
	//
	// The implementation should NOT loop on tool calls. The caller manages
	// the multi-step loop.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// ChatRequest is what gets sent to a Provider.Chat call.
type ChatRequest struct {
	System    string
	Messages  []Message
	Tools     []ToolDefinition
	Provider  config.ProviderConfig
	MaxTokens int
}

// ChatResponse is the result of a single Chat call. Either Text is set
// (final answer) or ToolCalls is non-empty (need to execute and continue).
type ChatResponse struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string // "end_turn" | "tool_use" | "max_tokens" | "stop_sequence"
	Usage      Usage  // tokens consumed by this call, as reported by the provider
}

// providerRegistry maps provider names to their implementations.
var providerRegistry = map[string]Provider{}

// RegisterProvider adds a provider to the registry. Called from init() in
// each adapter file.
func RegisterProvider(p Provider) {
	providerRegistry[p.Name()] = p
}

// GetProvider returns the provider with the given name, or nil if unknown.
func GetProvider(name string) Provider {
	return providerRegistry[name]
}
