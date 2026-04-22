package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
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
	// Trigger identifies where the session originated: "manual" when the
	// user typed into the chat, or a specific trigger name (e.g.
	// "insight", "not_ready_resource") for contextual "Ask Copilot"
	// buttons across the UI. Logged with each session for adoption
	// analytics. Defaults to "manual" when empty.
	Trigger string `json:"trigger,omitempty"`
	// LastRoundUsage is the provider-reported input usage of the most
	// recent round from the previous chat request. The backend uses it to
	// seed the auto-compact decision for round 0 of this request, which
	// otherwise relies on a chars-per-token approximation that
	// underestimates JSON-dense tool results. Optional; nil when this is
	// the first request of a session.
	LastRoundUsage *copilot.Usage `json:"lastRoundUsage,omitempty"`
}

// HandleCopilotConfig returns the public copilot configuration (no API keys).
// This endpoint is reachable even when the cluster is not connected so the
// frontend can decide whether to render the chat panel.
func (h *handlers) HandleCopilotConfig(w http.ResponseWriter, r *http.Request) {
	// Expose the resolved session budget so the UI can show "context X / Y".
	budget := h.copilotConfig.SessionBudgetTokens
	if budget <= 0 {
		budget = copilot.ContextWindowFor(h.copilotConfig.Primary.Provider, h.copilotConfig.Primary.Model)
	}
	trigger := int(float64(budget) * h.copilotConfig.AutoCompactThreshold)

	resp := map[string]interface{}{
		"enabled":        h.copilotConfig.Enabled,
		"provider":       h.copilotConfig.Primary.Provider,
		"model":          h.copilotConfig.Primary.Model,
		"proxyMode":      true,
		"sessionBudget":  budget,
		"compactTrigger": trigger,
		"autoCompact":    h.copilotConfig.AutoCompact,
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

	trigger := req.Trigger
	if trigger == "" {
		trigger = "manual"
	}

	logger := slog.Default().With(
		slog.String("component", "copilot"),
		slog.String("user", auth.ContextUserID(r)),
		slog.String("cluster", clusterName),
		slog.String("provider", h.copilotConfig.Primary.Provider),
		slog.String("model", h.copilotConfig.Primary.Model),
		slog.String("trigger", trigger),
	)

	// Multi-step tool calling loop
	const maxRounds = 10
	messages := req.Messages
	usedFallback := false

	// Session-level accounting
	var sessionUsage copilot.Usage
	var sessionToolBytes int
	var sessionToolCalls int
	sessionStart := time.Now()
	roundsUsed := 0

	// Per-tool breakdown: nombre → {calls, bytes, errors, duration}
	type toolStats struct {
		Calls      int   `json:"calls"`
		Bytes      int   `json:"bytes"`
		Errors     int   `json:"errors"`
		DurationMs int64 `json:"durationMs"`
	}
	toolBreakdown := map[string]*toolStats{}

	// Auto-compact events fired during this session — we attach them to
	// the persisted SessionRecord for drill-down in the admin UI.
	var sessionCompacts []copilot.CompactEvent

	finish := func(reason string) {
		// Collapse breakdown into a plain map so slog.JSONHandler emits it cleanly.
		breakdown := make(map[string]any, len(toolBreakdown))
		for name, s := range toolBreakdown {
			breakdown[name] = s
		}
		logger.Info("copilot session",
			slog.String("reason", reason),
			slog.Int("rounds", roundsUsed),
			slog.Int("inputTokens", sessionUsage.InputTokens),
			slog.Int("outputTokens", sessionUsage.OutputTokens),
			slog.Int("cacheReadTokens", sessionUsage.CacheReadTokens),
			slog.Int("cacheCreationTokens", sessionUsage.CacheCreationTokens),
			slog.Int("totalTokens", sessionUsage.Total()),
			slog.Int("toolCalls", sessionToolCalls),
			slog.Int("toolResultBytes", sessionToolBytes),
			slog.Duration("duration", time.Since(sessionStart)),
			slog.Bool("fallback", usedFallback),
			slog.Any("toolBreakdown", breakdown),
		)

		// Persist for the admin analytics page. Skipped silently when auth
		// is disabled (no usage store wired up).
		if h.copilotUsage != nil {
			tools := make(map[string]copilot.ToolStats, len(toolBreakdown))
			for name, s := range toolBreakdown {
				tools[name] = copilot.ToolStats{
					Calls:      s.Calls,
					Bytes:      s.Bytes,
					Errors:     s.Errors,
					DurationMs: s.DurationMs,
				}
			}
			rec := &copilot.SessionRecord{
				Timestamp:  time.Now(),
				UserID:     auth.ContextUserID(r),
				Cluster:    clusterName,
				Provider:   h.copilotConfig.Primary.Provider,
				Model:      h.copilotConfig.Primary.Model,
				Trigger:    trigger,
				Reason:     reason,
				Rounds:     roundsUsed,
				Usage:      sessionUsage,
				ToolCalls:  sessionToolCalls,
				ToolBytes:  sessionToolBytes,
				DurationMs: time.Since(sessionStart).Milliseconds(),
				Fallback:   usedFallback,
				Tools:      tools,
				Compacts:   sessionCompacts,
			}
			if err := h.copilotUsage.Record(rec); err != nil {
				logger.Warn("failed to persist copilot session", slog.String("error", err.Error()))
			}
		}
	}

	// Resolve the compaction trigger: budget × threshold. The budget is the
	// user's ceiling (defaults to the model's full context window); the
	// threshold is how full the conversation gets before we compact.
	sessionBudget := h.copilotConfig.SessionBudgetTokens
	if sessionBudget <= 0 {
		sessionBudget = copilot.ContextWindowFor(h.copilotConfig.Primary.Provider, h.copilotConfig.Primary.Model)
	}
	compactTrigger := int(float64(sessionBudget) * h.copilotConfig.AutoCompactThreshold)

	// Full input size of the previous round as reported by the provider
	// (non-cached + cache-creation + cache-read). This is what the LLM
	// actually processed — including system prompt and tool definitions
	// that ApproxTokens(messages) misses. Used for the auto-compact
	// decision so the server matches what the UI displays.
	//
	// Seeded from the frontend hint on a follow-up request so round 0 of
	// the new turn has the accurate context size from the previous
	// exchange; otherwise the chars-per-token approximation would
	// underestimate JSON-dense tool results and skip the trigger.
	var lastRoundFullInput int
	if req.LastRoundUsage != nil {
		u := *req.LastRoundUsage
		lastRoundFullInput = u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
	}

	// System prompt + tool definitions are constant across this request.
	// Their token footprint is added to the message-only approximation so
	// the compact check sees the real prompt size — otherwise a round
	// whose tool results balloon the messages array would slip past a
	// stale lastRoundFullInput and the provider would be billed the
	// oversize on the next call.
	systemToolsOverhead := copilot.ApproxSystemToolsTokens(systemPrompt, tools)

	for round := 0; round < maxRounds; round++ {
		// Auto-compact when the conversation approaches the budget.
		// Uses a cheap-tier model of the same provider to summarize the
		// older turns, then replaces them with a single summary message.
		if h.copilotConfig.AutoCompact {
			// Take the max of the last provider-reported input and our
			// own approximation of "what the next call would send right
			// now". The provider number is accurate for what was already
			// processed; the approximation catches tool results anexed
			// between rounds that the provider hasn't seen yet.
			approx := copilot.ApproxTokens(messages) + systemToolsOverhead
			contextSize := lastRoundFullInput
			if approx > contextSize {
				contextSize = approx
			}
			if contextSize >= compactTrigger {
				logger.Info("copilot auto-compact triggered",
					slog.Int("contextTokens", contextSize),
					slog.Int("trigger", compactTrigger),
					slog.Int("budget", sessionBudget),
				)
				cr, cerr := copilot.Compact(r.Context(), messages, copilot.CompactOptions{
					PreserveTurns: h.copilotConfig.CompactPreserveTurns,
					Provider:      h.copilotConfig.Primary,
					CompactModel:  h.copilotConfig.CompactModel,
				})
				if cerr != nil {
					logger.Warn("copilot auto-compact failed, continuing without it",
						slog.String("error", cerr.Error()),
					)
				} else if cr != nil && (cr.TurnsFolded > 0 || cr.ToolResultsStubbed > 0) {
					messages = cr.NewMessages
					sessionUsage.Add(cr.Usage)
					sessionCompacts = append(sessionCompacts, copilot.CompactEvent{
						TurnsFolded:  cr.TurnsFolded,
						TokensBefore: cr.TokensBefore,
						TokensAfter:  cr.TokensAfter,
						Model:        cr.UsedModel,
					})
					writeSSEEvent(w, flusher, "compact", map[string]any{
						"turnsFolded":        cr.TurnsFolded,
						"toolResultsStubbed": cr.ToolResultsStubbed,
						"tokensBefore":       cr.TokensBefore,
						"tokensAfter":        cr.TokensAfter,
						"model":              cr.UsedModel,
						"summary":            cr.Summary,
					})
					logger.Info("copilot auto-compact applied",
						slog.Int("turnsFolded", cr.TurnsFolded),
						slog.Int("toolResultsStubbed", cr.ToolResultsStubbed),
						slog.Int("tokensBefore", cr.TokensBefore),
						slog.Int("tokensAfter", cr.TokensAfter),
						slog.String("compactModel", cr.UsedModel),
					)
				} else if cr != nil {
					// Nothing folded and nothing stubbed — surface so the
					// "triggered without applied" mystery in logs is gone.
					logger.Info("copilot auto-compact noop",
						slog.String("reason", "no foldable turns and no stubbable tool_results"),
						slog.Int("tokensBefore", cr.TokensBefore),
					)
				}
			}
		}

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
			logger.Warn("copilot primary failed, retrying with fallback",
				slog.String("error", err.Error()),
				slog.String("fallbackProvider", h.copilotConfig.Fallback.Provider),
				slog.String("fallbackModel", h.copilotConfig.Fallback.Model),
			)
			chatReq.Provider = *h.copilotConfig.Fallback
			resp, err = h.callProvider(r, chatReq)
			if err == nil {
				usedFallback = true
				writeSSEEvent(w, flusher, "meta", map[string]bool{"fallback": true})
			}
		}

		if err != nil {
			logger.Error("copilot chat error",
				slog.Int("round", round),
				slog.String("error", err.Error()),
			)
			writeSSEEvent(w, flusher, "error", map[string]string{"error": friendlyCopilotError(err)})
			writeSSEEvent(w, flusher, "done", nil)
			roundsUsed = round + 1
			finish("error")
			return
		}

		// Accumulate token usage for the session
		sessionUsage.Add(resp.Usage)
		roundsUsed = round + 1

		// Capture full input size (non-cached + cached) of this round for
		// the next iteration's auto-compact decision. Matches the UI's
		// contextTokens formula so both sides trigger at the same point.
		lastRoundFullInput = resp.Usage.InputTokens + resp.Usage.CacheReadTokens + resp.Usage.CacheCreationTokens

		logger.Debug("copilot round",
			slog.Int("round", round),
			slog.String("stopReason", resp.StopReason),
			slog.Int("inputTokens", resp.Usage.InputTokens),
			slog.Int("outputTokens", resp.Usage.OutputTokens),
			slog.Int("cacheReadTokens", resp.Usage.CacheReadTokens),
			slog.Int("cacheCreationTokens", resp.Usage.CacheCreationTokens),
			slog.Int("toolCalls", len(resp.ToolCalls)),
			slog.Int("textBytes", len(resp.Text)),
		)

		// Emit per-round usage so the UI can show it live
		writeSSEEvent(w, flusher, "usage", map[string]any{
			"round":   round,
			"turn":    resp.Usage,
			"session": sessionUsage,
		})

		// If the model produced text, send it to the user
		if resp.Text != "" {
			writeSSEEvent(w, flusher, "text", map[string]string{"text": resp.Text})
		}

		// If no tool calls, we're done. Emit the final messages array
		// including the assistant's text response so the frontend can
		// persist the full tool-call history across questions (this
		// matches the accumulative context-window model documented by
		// Anthropic and OpenAI — previous turns are preserved completely).
		if len(resp.ToolCalls) == 0 {
			finalMessages := messages
			if resp.Text != "" {
				finalMessages = append(finalMessages, copilot.Message{
					Role:    copilot.RoleAssistant,
					Content: resp.Text,
				})
			}

			// Reactive compact: if this final round's provider-reported
			// input crossed the trigger, compact now so the client's
			// next prompt starts from a summary instead of resending a
			// bloated history. The proactive check at the top of the
			// loop cannot fire for end_turn rounds — there is no next
			// iteration to guard. Mid-loop cases are already covered by
			// that check combined with the messages-plus-overhead
			// approximation.
			if h.copilotConfig.AutoCompact && lastRoundFullInput >= compactTrigger {
				logger.Info("copilot auto-compact triggered",
					slog.String("mode", "reactive"),
					slog.Int("contextTokens", lastRoundFullInput),
					slog.Int("trigger", compactTrigger),
					slog.Int("budget", sessionBudget),
				)
				cr, cerr := copilot.Compact(r.Context(), finalMessages, copilot.CompactOptions{
					PreserveTurns: h.copilotConfig.CompactPreserveTurns,
					Provider:      h.copilotConfig.Primary,
					CompactModel:  h.copilotConfig.CompactModel,
				})
				if cerr != nil {
					logger.Warn("copilot reactive compact failed, continuing without it",
						slog.String("error", cerr.Error()),
					)
				} else if cr != nil && (cr.TurnsFolded > 0 || cr.ToolResultsStubbed > 0) {
					finalMessages = cr.NewMessages
					sessionUsage.Add(cr.Usage)
					sessionCompacts = append(sessionCompacts, copilot.CompactEvent{
						TurnsFolded:  cr.TurnsFolded,
						TokensBefore: cr.TokensBefore,
						TokensAfter:  cr.TokensAfter,
						Model:        cr.UsedModel,
					})
					writeSSEEvent(w, flusher, "compact", map[string]any{
						"turnsFolded":        cr.TurnsFolded,
						"toolResultsStubbed": cr.ToolResultsStubbed,
						"tokensBefore":       cr.TokensBefore,
						"tokensAfter":        cr.TokensAfter,
						"model":              cr.UsedModel,
						"summary":            cr.Summary,
					})
					logger.Info("copilot auto-compact applied",
						slog.String("mode", "reactive"),
						slog.Int("turnsFolded", cr.TurnsFolded),
						slog.Int("toolResultsStubbed", cr.ToolResultsStubbed),
						slog.Int("tokensBefore", cr.TokensBefore),
						slog.Int("tokensAfter", cr.TokensAfter),
						slog.String("compactModel", cr.UsedModel),
					)
				} else if cr != nil {
					logger.Info("copilot auto-compact noop",
						slog.String("mode", "reactive"),
						slog.String("reason", "no foldable turns and no stubbable tool_results"),
						slog.Int("tokensBefore", cr.TokensBefore),
					)
				}
			}

			writeSSEEvent(w, flusher, "done", map[string]any{
				"messages": finalMessages,
			})
			finish("done")
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

			toolStart := time.Now()
			result := executor.Execute(call)
			toolDur := time.Since(toolStart)

			outBytes := len(result.Content)
			inBytes := len(call.Input)
			sessionToolCalls++
			sessionToolBytes += outBytes

			s, ok := toolBreakdown[call.Name]
			if !ok {
				s = &toolStats{}
				toolBreakdown[call.Name] = s
			}
			s.Calls++
			s.Bytes += outBytes
			s.DurationMs += toolDur.Milliseconds()
			if result.IsError {
				s.Errors++
			}

			logger.Debug("copilot tool call",
				slog.Int("round", round),
				slog.String("tool", call.Name),
				slog.Int("inputBytes", inBytes),
				slog.Int("outputBytes", outBytes),
				slog.Bool("error", result.IsError),
				slog.Duration("duration", toolDur),
			)

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
	logger.Warn("copilot hit max rounds",
		slog.Int("maxRounds", maxRounds),
	)
	writeSSEEvent(w, flusher, "error", map[string]string{
		"error": fmt.Sprintf("reached max tool call rounds (%d)", maxRounds),
	})
	writeSSEEvent(w, flusher, "done", nil)
	finish("max_rounds")
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

// CopilotCompactRequest folds an existing conversation into a summary and
// returns the compacted message array for the frontend to swap into state.
// This powers the "new session with summary" UX — the user stays in the
// same cluster context but with a much smaller conversation.
type CopilotCompactRequest struct {
	Messages []copilot.Message `json:"messages"`
	// ResetAll true → summarize everything and return a single summary
	// message. False → preserve the last CompactPreserveTurns turns intact.
	ResetAll bool `json:"resetAll,omitempty"`
}

// CopilotCompactResponse mirrors copilot.CompactResult with JSON naming.
type CopilotCompactResponse struct {
	Summary            string            `json:"summary"`
	Messages           []copilot.Message `json:"messages"`
	TokensBefore       int               `json:"tokensBefore"`
	TokensAfter        int               `json:"tokensAfter"`
	TurnsFolded        int               `json:"turnsFolded"`
	ToolResultsStubbed int               `json:"toolResultsStubbed"`
	Model              string            `json:"model"`
}

// HandleCopilotCompact runs a standalone compaction over the provided
// messages. Used by the "new session with summary" button in the UI.
func (h *handlers) HandleCopilotCompact(w http.ResponseWriter, r *http.Request) {
	if !h.copilotConfig.Enabled {
		respondError(w, http.StatusServiceUnavailable, "copilot is not configured (KUBEBOLT_AI_API_KEY not set)")
		return
	}
	var req CopilotCompactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Messages) == 0 {
		respondError(w, http.StatusBadRequest, "messages array is empty")
		return
	}

	logger := slog.Default().With(
		slog.String("component", "copilot"),
		slog.String("op", "compact"),
		slog.String("user", auth.ContextUserID(r)),
	)

	cr, err := copilot.Compact(r.Context(), req.Messages, copilot.CompactOptions{
		PreserveTurns: h.copilotConfig.CompactPreserveTurns,
		Provider:      h.copilotConfig.Primary,
		CompactModel:  h.copilotConfig.CompactModel,
		ResetAll:      req.ResetAll,
	})
	if err != nil {
		logger.Error("copilot manual compact failed", slog.String("error", err.Error()))
		respondError(w, http.StatusBadGateway, friendlyCopilotError(err))
		return
	}

	logger.Info("copilot manual compact",
		slog.Int("turnsFolded", cr.TurnsFolded),
		slog.Int("toolResultsStubbed", cr.ToolResultsStubbed),
		slog.Int("tokensBefore", cr.TokensBefore),
		slog.Int("tokensAfter", cr.TokensAfter),
		slog.Bool("resetAll", req.ResetAll),
		slog.String("compactModel", cr.UsedModel),
	)

	respondJSON(w, http.StatusOK, CopilotCompactResponse{
		Summary:            cr.Summary,
		Messages:           cr.NewMessages,
		TokensBefore:       cr.TokensBefore,
		TokensAfter:        cr.TokensAfter,
		TurnsFolded:        cr.TurnsFolded,
		ToolResultsStubbed: cr.ToolResultsStubbed,
		Model:              cr.UsedModel,
	})
}
