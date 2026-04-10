package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// friendlyCopilotError translates raw provider errors into user-friendly messages.
func friendlyCopilotError(err error) string {
	var herr *copilot.ProviderHTTPError
	if errors.As(err, &herr) {
		switch {
		case herr.StatusCode == 401 || herr.StatusCode == 403:
			return fmt.Sprintf("%s authentication failed (HTTP %d). Check that KUBEBOLT_AI_API_KEY is valid for the configured provider.", herr.Provider, herr.StatusCode)
		case herr.StatusCode == 404:
			// Anthropic returns 404 when the model name is unknown for the account
			if strings.Contains(strings.ToLower(herr.Body), "model") || herr.Provider == "anthropic" {
				return fmt.Sprintf("%s returned 404 — the configured model is not available for your account. Set KUBEBOLT_AI_MODEL to a model your account has access to (e.g. claude-3-5-sonnet-latest, claude-sonnet-4-5, gpt-4o).", herr.Provider)
			}
			return fmt.Sprintf("%s endpoint not found (HTTP 404). Check KUBEBOLT_AI_BASE_URL.", herr.Provider)
		case herr.StatusCode == 429:
			return fmt.Sprintf("%s rate limit hit. Configure a fallback provider with KUBEBOLT_AI_FALLBACK_API_KEY to auto-retry.", herr.Provider)
		case herr.StatusCode >= 500:
			return fmt.Sprintf("%s upstream error (HTTP %d). Try again or configure a fallback provider.", herr.Provider, herr.StatusCode)
		}
		return fmt.Sprintf("%s error (HTTP %d): %s", herr.Provider, herr.StatusCode, truncate(herr.Body, 200))
	}
	return err.Error()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// CopilotChatRequest is the body POSTed by the frontend to /copilot/chat.
type CopilotChatRequest struct {
	Messages    []copilot.Message `json:"messages"`
	CurrentPath string            `json:"currentPath,omitempty"`
}

// HandleCopilotConfig returns the public copilot configuration (no API keys).
// This endpoint is reachable even when the cluster is not connected so the
// frontend can decide whether to render the chat panel.
func (h *handlers) HandleCopilotConfig(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"enabled":   h.copilotConfig.Enabled,
		"provider":  h.copilotConfig.Primary.Provider,
		"model":     h.copilotConfig.Primary.Model,
		"proxyMode": true,
	}
	if h.copilotConfig.Fallback != nil {
		resp["fallback"] = map[string]string{
			"provider": h.copilotConfig.Fallback.Provider,
			"model":    h.copilotConfig.Fallback.Model,
		}
	}
	respondJSON(w, http.StatusOK, resp)
}

// HandleCopilotChat runs a chat turn with the configured LLM provider.
// It manages the multi-step tool calling loop: the LLM may request tools,
// the handler executes them, appends results, and re-invokes the model.
//
// Response is streamed via Server-Sent Events with these event types:
//   meta       — fallback used, model info
//   tool_call  — emitted when the LLM invokes a tool (for UI indicator)
//   text       — final assistant text
//   error      — provider or tool error
//   done       — stream complete
func (h *handlers) HandleCopilotChat(w http.ResponseWriter, r *http.Request) {
	if !h.copilotConfig.Enabled {
		respondError(w, http.StatusServiceUnavailable, "copilot is not configured (KUBEBOLT_AI_API_KEY not set)")
		return
	}

	var req CopilotChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Messages) == 0 {
		respondError(w, http.StatusBadRequest, "messages array is empty")
		return
	}

	// Cluster must be connected for tool execution to work
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Build the system prompt with cluster context
	clusterName := h.manager.ActiveContext()
	systemPrompt := copilot.BuildSystemPrompt(clusterName, req.CurrentPath)

	executor := copilot.NewExecutor(h.manager)
	tools := copilot.ToolDefinitions()

	// Multi-step tool calling loop
	const maxRounds = 10
	messages := req.Messages
	usedFallback := false

	for round := 0; round < maxRounds; round++ {
		// Build the chat request for this round
		chatReq := copilot.ChatRequest{
			System:    systemPrompt,
			Messages:  messages,
			Tools:     tools,
			Provider:  h.copilotConfig.Primary,
			MaxTokens: h.copilotConfig.MaxTokens,
		}

		resp, err := h.callProvider(r, chatReq)

		// On recoverable error, try fallback if configured
		if err != nil && copilot.IsRecoverable(err) && h.copilotConfig.Fallback != nil && !usedFallback {
			log.Printf("Copilot primary failed (%v), retrying with fallback %s/%s",
				err, h.copilotConfig.Fallback.Provider, h.copilotConfig.Fallback.Model)
			chatReq.Provider = *h.copilotConfig.Fallback
			resp, err = h.callProvider(r, chatReq)
			if err == nil {
				usedFallback = true
				writeSSEEvent(w, flusher, "meta", map[string]bool{"fallback": true})
			}
		}

		if err != nil {
			log.Printf("Copilot chat error: %v", err)
			writeSSEEvent(w, flusher, "error", map[string]string{"error": friendlyCopilotError(err)})
			writeSSEEvent(w, flusher, "done", nil)
			return
		}

		// If the model produced text, send it to the user
		if resp.Text != "" {
			writeSSEEvent(w, flusher, "text", map[string]string{"text": resp.Text})
		}

		// If no tool calls, we're done
		if len(resp.ToolCalls) == 0 {
			writeSSEEvent(w, flusher, "done", nil)
			return
		}

		// Append the assistant message with tool calls
		messages = append(messages, copilot.Message{
			Role:      copilot.RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool and append results
		var toolResults []copilot.ToolResult
		for _, call := range resp.ToolCalls {
			writeSSEEvent(w, flusher, "tool_call", map[string]string{"toolName": call.Name})
			result := executor.Execute(call)
			toolResults = append(toolResults, result)
		}

		// Append tool results as a single message (Anthropic + OpenAI both expect grouped results)
		messages = append(messages, copilot.Message{
			Role:        copilot.RoleUser,
			ToolResults: toolResults,
		})

		// Continue the loop — model will see tool results and produce its next response
	}

	// Hit the max rounds limit
	writeSSEEvent(w, flusher, "error", map[string]string{
		"error": fmt.Sprintf("reached max tool call rounds (%d)", maxRounds),
	})
	writeSSEEvent(w, flusher, "done", nil)
}

// callProvider invokes the configured provider for one chat turn.
func (h *handlers) callProvider(r *http.Request, req copilot.ChatRequest) (*copilot.ChatResponse, error) {
	provider := copilot.GetProvider(req.Provider.Provider)
	if provider == nil {
		return nil, fmt.Errorf("unknown provider: %s", req.Provider.Provider)
	}
	return provider.Chat(r.Context(), req)
}

// writeSSEEvent writes a single SSE message with the given event name and JSON payload.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload interface{}) {
	if payload == nil {
		fmt.Fprintf(w, "event: %s\ndata: {}\n\n", event)
	} else {
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	}
	flusher.Flush()
}

// ensure config import is referenced (avoids unused import in test files)
var _ = config.CopilotConfig{}
