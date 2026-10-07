package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	anthropicDefaultURL = "https://api.anthropic.com/v1/messages"
	// Default to the latest Claude Sonnet. Users can override via
	// KUBEBOLT_AI_MODEL with any Claude model their account has access to.
	anthropicDefaultModel = "claude-sonnet-5"
	anthropicAPIVersion   = "2023-06-01"
	// extended-cache-ttl: pairs with cache_control.ttl="1h".
	anthropicExtendedCacheBeta = "extended-cache-ttl-2025-04-11"
)

func init() {
	RegisterProvider(&AnthropicProvider{
		client: &http.Client{Timeout: 120 * time.Second},
	})
}

// AnthropicProvider implements the Provider interface for Anthropic Claude.
type AnthropicProvider struct {
	client *http.Client
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// Anthropic API request/response types (subset).

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	// CacheControl, on the LAST block of the LAST message, makes the whole
	// conversation prefix (system + tools + prior turns) a cache breakpoint — so
	// the multi-round tool-calling loop re-reads the growing history at ~10% of
	// the input rate instead of reprocessing it each round (E.8).
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicCacheControl marks a content block as cacheable. The entry lives 5
// minutes after the last read by default; TTL "1h" extends that.
type anthropicCacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "" (=5m) | "1h"
}

// staticPrefixCache is the breakpoint for the parts that do not change between
// questions: the system prompt and the tool definitions, ~27k tokens together.
//
// Measured on real sessions: 68-89% of a session's cost was cache WRITE, not
// the conversation. A write bills 1.25x the input rate and a read 0.1x, so a
// write is 12.5x a read — and the 5-minute default expires between questions
// asked at a human pace. Two sessions a minute apart wrote 41,639 and 6,012
// tokens; the second one was cheap only because it caught the first one's
// cache still warm.
//
// The 1h TTL bills 2x on write and still 0.1x on read, so it pays for itself
// from the SECOND question in the hour:
//
//	cold 5m write   27k x $2.50/1M = $0.068   per question
//	cold 1h write   27k x $4.00/1M = $0.109   once
//	warm read       27k x $0.20/1M = $0.0054  every question after
//
// Six spaced questions: $0.41 today, $0.14 with the hour. The trade is real
// and stated: ONE isolated question costs ~60% more. KUBEBOLT_AI_CACHE_TTL=5m
// restores the old behaviour for installs whose usage is genuinely one-shot.
func staticPrefixCache() *anthropicCacheControl {
	return &anthropicCacheControl{Type: "ephemeral", TTL: anthropicStaticCacheTTL()}
}

// conversationCache is deliberately NOT extended. The breakpoint sits on the
// last message, so it moves every round and the entry it creates is never read
// after the conversation ends — paying 2x to keep it for an hour buys nothing.
// Rounds are seconds apart, which is what the 5-minute default is for.
func conversationCache() *anthropicCacheControl {
	return &anthropicCacheControl{Type: "ephemeral"}
}

// anthropicStaticCacheTTL reads the override once per call; "5m" (or any
// unrecognised value) means "send no ttl", which is the API default.
func anthropicStaticCacheTTL() string {
	if v := strings.TrimSpace(os.Getenv("KUBEBOLT_AI_CACHE_TTL")); v != "" && v != "1h" {
		return ""
	}
	return "1h"
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicSystemBlock is the content-block form of the system field. We
// use it instead of a plain string so we can attach cache_control and have
// the system prompt cached across rounds.
type anthropicSystemBlock struct {
	Type         string                 `json:"type"` // "text"
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicRequest mirrors the Messages API request body. System is typed
// as `any` because Anthropic accepts either a raw string or an array of
// content blocks — we use the array form to enable caching.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    any                `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"` // "disabled"
}

// anthropicThinkingOff reports whether a request may send thinking
// {"type":"disabled"} for this model. It is an allow-list on purpose: Claude
// Haiku 5.5 thinks by default and accepts disabling it (verified against the
// live API 2026-10-07), while Opus 5.5, Sonnet 5.5 and Fable reject the same
// body with a 400 — a guess here fails the call. Models that do not think by
// default have nothing to turn off.
func anthropicThinkingOff(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "claude-haiku-5-5")
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []anthropicContent `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      anthropicUsage     `json:"usage"`
}

// Chat sends a single request to the Anthropic API and returns the response.
func (p *AnthropicProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := req.Provider.Model
	if model == "" {
		model = anthropicDefaultModel
	}
	url := req.Provider.BaseURL
	if url == "" {
		url = anthropicDefaultURL
	}

	// Build the request body.
	//
	// Prompt caching: the system prompt and the tool definitions are stable
	// across rounds of the same session, so we mark them as cacheable. The
	// cache is ephemeral (5-minute TTL after last read) and reads are
	// billed at ~10% of the input rate. First-round writes cost ~125% of
	// input, so break-even sits at round 2 — a good trade for every
	// multi-round session.
	body := anthropicRequest{
		Model:     model,
		MaxTokens: req.MaxTokens,
		Messages:  toAnthropicMessages(req.Messages),
		Tools:     toAnthropicTools(req.Tools),
	}
	if req.NoThinking && anthropicThinkingOff(model) {
		body.Thinking = &anthropicThinking{Type: "disabled"}
	}
	if req.System != "" {
		body.System = []anthropicSystemBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: staticPrefixCache(),
		}}
	}
	if n := len(body.Tools); n > 0 {
		// Marking the last tool caches all tool definitions as a single prefix.
		body.Tools[n-1].CacheControl = staticPrefixCache()
	}
	// Also cache the conversation prefix (E.8): a breakpoint on the last block of
	// the last message means the next round reads the whole history from cache.
	// 3 breakpoints total (system + tools + history) — under Anthropic's limit of
	// 4. Anthropic ignores a breakpoint whose block is below the cache minimum, so
	// short first turns are harmless.
	markLastMessageCacheable(body.Messages)
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		slog.DebugContext(ctx, "anthropic request",
			slog.String("url", url),
			slog.String("model", model),
			slog.Int("maxTokens", req.MaxTokens),
			slog.Int("messages", len(body.Messages)),
			slog.Int("tools", len(body.Tools)),
			slog.String("body", string(bodyBytes)),
		)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", req.Provider.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
	if anthropicStaticCacheTTL() == "1h" {
		// Paired with the ttl field. The extended TTL shipped behind this beta
		// and has since graduated on the stable param, but a recognised beta
		// value is ignored where it is no longer needed, while a MISSING
		// required one 400s every request — so it rides along.
		httpReq.Header.Set("anthropic-beta", anthropicExtendedCacheBeta)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		slog.DebugContext(ctx, "anthropic response",
			slog.Int("status", resp.StatusCode),
			slog.String("body", string(respBody)),
		)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &ProviderHTTPError{
			StatusCode: resp.StatusCode,
			Provider:   "anthropic",
			Body:       string(respBody),
		}
	}

	var ar anthropicResponse
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Extract text and tool calls from the content blocks. A `thinking` block
	// (models that reason by default, e.g. Haiku 5.5) is skipped like any
	// other non-text block: it is not sent back on the next round, which the
	// API accepts, so a tool loop keeps working at the cost of that round's
	// reasoning.
	out := &ChatResponse{
		StopReason: ar.StopReason,
		// Tagged here, per call, because this is the only place that knows
		// how large THIS call's prompt was (two-rate-card models, see
		// ModelPricing.Long).
		Usage: TagLongContext(Usage{
			InputTokens:         ar.Usage.InputTokens,
			OutputTokens:        ar.Usage.OutputTokens,
			CacheCreationTokens: ar.Usage.CacheCreationInputTokens,
			CacheReadTokens:     ar.Usage.CacheReadInputTokens,
		}, "anthropic", model),
	}
	for _, block := range ar.Content {
		switch block.Type {
		case "text":
			out.Text += block.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}
	return out, nil
}

// toAnthropicMessages converts internal messages to Anthropic's content-block format.
func toAnthropicMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		// Skip system messages — Anthropic uses a separate top-level field
		if m.Role == RoleSystem {
			continue
		}

		var content []anthropicContent

		// Tool results are sent as a "user" message with tool_result blocks
		if len(m.ToolResults) > 0 {
			for _, tr := range m.ToolResults {
				content = append(content, anthropicContent{
					Type:      "tool_result",
					ToolUseID: tr.ToolCallID,
					Content:   tr.Content,
				})
			}
			out = append(out, anthropicMessage{Role: "user", Content: content})
			continue
		}

		// Assistant messages with tool calls
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			if m.Content != "" {
				content = append(content, anthropicContent{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				content = append(content, anthropicContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Input,
				})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: content})
			continue
		}

		// Plain text messages
		if m.Content != "" {
			role := "user"
			if m.Role == RoleAssistant {
				role = "assistant"
			}
			out = append(out, anthropicMessage{
				Role:    role,
				Content: []anthropicContent{{Type: "text", Text: m.Content}},
			})
		}
	}
	return out
}

// markLastMessageCacheable puts a cache breakpoint on the last content block of
// the last message, so the conversation prefix is cached for the next round of
// the tool-calling loop (E.8). No-op when there are no messages / no blocks.
func markLastMessageCacheable(msgs []anthropicMessage) {
	if len(msgs) == 0 {
		return
	}
	last := &msgs[len(msgs)-1]
	if len(last.Content) == 0 {
		return
	}
	last.Content[len(last.Content)-1].CacheControl = conversationCache()
}

func toAnthropicTools(tools []ToolDefinition) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, len(tools))
	for i, t := range tools {
		out[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	return out
}
