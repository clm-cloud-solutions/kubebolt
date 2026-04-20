package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	anthropicDefaultURL = "https://api.anthropic.com/v1/messages"
	// Default to the latest Claude Sonnet. Users can override via
	// KUBEBOLT_AI_MODEL with any Claude model their account has access to.
	anthropicDefaultModel = "claude-sonnet-4-6"
	anthropicAPIVersion   = "2023-06-01"
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
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
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

	// Build the request body
	body := anthropicRequest{
		Model:     model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
		Messages:  toAnthropicMessages(req.Messages),
		Tools:     toAnthropicTools(req.Tools),
	}
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

	// Extract text and tool calls from the content blocks
	out := &ChatResponse{
		StopReason: ar.StopReason,
		Usage: Usage{
			InputTokens:         ar.Usage.InputTokens,
			OutputTokens:        ar.Usage.OutputTokens,
			CacheCreationTokens: ar.Usage.CacheCreationInputTokens,
			CacheReadTokens:     ar.Usage.CacheReadInputTokens,
		},
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
