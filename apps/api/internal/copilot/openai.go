package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	openaiDefaultURL   = "https://api.openai.com/v1/chat/completions"
	openaiDefaultModel = "gpt-4o"
)

func init() {
	RegisterProvider(&OpenAIProvider{
		client: &http.Client{Timeout: 120 * time.Second},
	})
}

// OpenAIProvider implements Provider for OpenAI's chat completions API.
type OpenAIProvider struct {
	client *http.Client
}

func (p *OpenAIProvider) Name() string { return "openai" }

// OpenAI API types (subset).

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

type openaiMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"` // for reasoning models (Qwen, DeepSeek-R1, etc.)
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

type openaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"`
	} `json:"function"`
}

type openaiRequest struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	Tools     []openaiTool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openaiChoice struct {
	Index        int           `json:"index"`
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openaiResponse struct {
	ID      string         `json:"id"`
	Choices []openaiChoice `json:"choices"`
}

func (p *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := req.Provider.Model
	if model == "" {
		model = openaiDefaultModel
	}
	url := req.Provider.BaseURL
	if url == "" {
		url = openaiDefaultURL
	}

	body := openaiRequest{
		Model:     model,
		Messages:  toOpenAIMessages(req.System, req.Messages),
		Tools:     toOpenAITools(req.Tools),
		MaxTokens: req.MaxTokens,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Provider.APIKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &ProviderHTTPError{
			StatusCode: resp.StatusCode,
			Provider:   "openai",
			Body:       string(respBody),
		}
	}

	var or openaiResponse
	if err := json.Unmarshal(respBody, &or); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(or.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices in response")
	}
	choice := or.Choices[0]

	// Reasoning models (Qwen 3.x, DeepSeek-R1, GPT-o1, etc.) emit
	// reasoning_content separately from content. Fall back to it if content
	// is empty so the user at least sees what the model produced.
	text := choice.Message.Content
	if text == "" && choice.Message.ReasoningContent != "" && len(choice.Message.ToolCalls) == 0 {
		text = "_(reasoning only — model did not produce a final response)_\n\n" + choice.Message.ReasoningContent
	}

	out := &ChatResponse{
		Text:       text,
		StopReason: choice.FinishReason,
	}
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}
	return out, nil
}

func toOpenAIMessages(system string, msgs []Message) []openaiMessage {
	out := make([]openaiMessage, 0, len(msgs)+1)
	if system != "" {
		out = append(out, openaiMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		// Tool results — one openai message per result
		if len(m.ToolResults) > 0 {
			for _, tr := range m.ToolResults {
				out = append(out, openaiMessage{
					Role:       "tool",
					ToolCallID: tr.ToolCallID,
					Content:    tr.Content,
				})
			}
			continue
		}
		// Assistant with tool calls
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			tcs := make([]openaiToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				tcs[i] = openaiToolCall{
					ID:   tc.ID,
					Type: "function",
				}
				tcs[i].Function.Name = tc.Name
				tcs[i].Function.Arguments = string(tc.Input)
			}
			out = append(out, openaiMessage{
				Role:      "assistant",
				Content:   m.Content,
				ToolCalls: tcs,
			})
			continue
		}
		// Plain text message
		role := "user"
		if m.Role == RoleAssistant {
			role = "assistant"
		} else if m.Role == RoleSystem {
			role = "system"
		}
		out = append(out, openaiMessage{Role: role, Content: m.Content})
	}
	return out
}

func toOpenAITools(tools []ToolDefinition) []openaiTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openaiTool, len(tools))
	for i, t := range tools {
		out[i] = openaiTool{Type: "function"}
		out[i].Function.Name = t.Name
		out[i].Function.Description = t.Description
		out[i].Function.Parameters = t.InputSchema
	}
	return out
}
