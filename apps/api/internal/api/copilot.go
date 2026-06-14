package api

import (
	"context"
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

// withSessionContextPrefix returns a fresh slice with `sessionCtx` prepended
// to the content of the FIRST user message. The original messages slice is
// not modified and the messages it shares with the caller's slice remain
// untouched — only the prefixed first user message is a new struct value.
//
// This is the LLM-facing transformation: the model sees cluster + view + Now
// at the top of the operator's question, while the canonical messages slice
// (which the `done` event echoes back to the chat UI, and which the next
// turn re-sends) keeps the operator's literal text. Without the separation,
// the prefix surfaces in the chat bubble AND multi-turn re-sends stack
// nested Session Context blocks one per turn.
//
// When sessionCtx is empty (or there is no user message), the input slice
// is returned as-is.
func withSessionContextPrefix(messages []copilotMessageView, sessionCtx string) []copilotMessageView {
	if sessionCtx == "" || len(messages) == 0 {
		return messages
	}
	out := make([]copilotMessageView, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].Role == copilot.RoleUser && out[i].Content != "" {
			out[i].Content = sessionCtx + "\n\n" + out[i].Content
			break
		}
	}
	return out
}

// copilotMessageView is an alias for the canonical Message type. Keeping it
// here makes the helper's intent obvious — it operates on the LLM's view of
// messages, never on the caller's storage.
type copilotMessageView = copilot.Message

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
	// ClientTimezone is the IANA timezone name of the operator's browser
	// (Intl.DateTimeFormat().resolvedOptions().timeZone). Used to anchor
	// "today"/"yesterday" in the Now block of the session context so Kobi
	// resolves relative time without asking. Optional; falls back to UTC
	// when missing or unparseable.
	ClientTimezone string `json:"clientTimezone,omitempty"`
	// ClientNow is the operator's wall-clock at request time, as RFC3339.
	// Currently advisory — the server's own clock is authoritative for the
	// Now block. Reserved for future drift detection / multi-region
	// debugging without a schema change. Optional.
	ClientNow string `json:"clientNow,omitempty"`
	// ConversationID ties this turn to a persisted conversation so it can be
	// resumed after a browser refresh / re-login. Empty on the first turn of
	// a new conversation — the server generates one and returns it in the
	// `meta` event. Persistence is skipped silently when no store is wired
	// (auth/BoltDB disabled).
	ConversationID string `json:"conversationId,omitempty"`
	// OriginatingInsightID links a conversation that began from an insight
	// (the insight's stable fingerprint) so the insight detail can deep-link
	// "Kobi analyzed this" back to resume. Sent only on the first turn of an
	// insight-triggered conversation; preserved across later turns.
	OriginatingInsightID string `json:"originatingInsightId,omitempty"`
}

// HandleCopilotConfig returns the public copilot configuration (no API keys).
// This endpoint is reachable even when the cluster is not connected so the
// frontend can decide whether to render the chat panel.
func (h *handlers) HandleCopilotConfig(w http.ResponseWriter, r *http.Request) {
	// Resolve live (BoltDB-override-aware) config so the chat panel's
	// "Configured / Not configured" pill updates immediately after the
	// admin saves a new API key in Admin → Settings, without needing a
	// process restart or a 30s-cached metadata round trip.
	cfg := h.liveCopilotConfig()

	// Expose the resolved session budget so the UI can show "context X / Y".
	budget := cfg.SessionBudgetTokens
	if budget <= 0 {
		budget = copilot.ContextWindowFor(cfg.Primary.Provider, cfg.Primary.Model)
	}
	trigger := int(float64(budget) * cfg.AutoCompactThreshold)

	resp := map[string]interface{}{
		"enabled":        cfg.Enabled,
		"provider":       cfg.Primary.Provider,
		"model":          cfg.Primary.Model,
		"proxyMode":      true,
		"sessionBudget":  budget,
		"compactTrigger": trigger,
		"autoCompact":    cfg.AutoCompact,
		"showToolCalls":  cfg.ShowToolCalls,
		// How long the UI polls an executed action for convergence before
		// declaring it stalled and auto-investigating. Milliseconds so the
		// frontend can feed it straight into setInterval/Date math.
		"actionProgressTimeoutMs": cfg.ActionProgressTimeout.Milliseconds(),
	}
	if cfg.Fallback != nil {
		resp["fallback"] = map[string]string{
			"provider": cfg.Fallback.Provider,
			"model":    cfg.Fallback.Model,
		}
	}
	respondJSON(w, http.StatusOK, resp)
}

// HandleCopilotChat runs a chat turn with the configured LLM provider.
// It manages the multi-step tool calling loop: the LLM may request tools,
// the handler executes them, appends results, and re-invokes the model.
//
// Response is streamed via Server-Sent Events with these event types:
//
//	meta       — fallback used, model info
//	tool_call  — emitted when the LLM invokes a tool (for UI indicator)
//	text       — final assistant text
//	error      — provider or tool error
//	done       — stream complete
func (h *handlers) HandleCopilotChat(w http.ResponseWriter, r *http.Request) {
	// Snapshot the live config ONCE per request. Subsequent reads inside
	// this handler use `cfg.X` (never re-read) so a concurrent admin
	// PUT doesn't split the request between two configurations.
	cfg := h.liveCopilotConfig()
	if !cfg.Enabled {
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
	conn := h.manager.Connector(r.Context())
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

	// Build the system prompt — parameter-free as of Phase 6 so the cached
	// prefix is byte-identical across clusters, views, and operators.
	clusterName := h.manager.ActiveContext()
	systemPrompt := copilot.BuildSystemPrompt()

	executor := copilot.NewExecutor(h.manager)
	// Action governance (Sprint 1): withhold propose_* tools when actions
	// are disabled, and the destructive verbs when the sub-switch is off —
	// the LLM can't propose what it can't see. Defaults are ON (the action
	// surface already shipped). Resolved config (env baseline + live BoltDB
	// override from the admin toggle) wins over the raw env baseline.
	govCfg := h.resolvedCopilotConfig()
	tools := copilot.GovernedToolDefinitions(
		govCfg.ActionsEnabled,
		govCfg.DestructiveActionsEnabled,
	)

	trigger := req.Trigger
	if trigger == "" {
		trigger = "manual"
	}

	// Resolve the conversation identity for persistence + resume. A brand-new
	// conversation has no id yet — we mint one and hand it back in the `meta`
	// event up-front, so a mid-stream refresh can still resume before `done`
	// lands. Owner is the user (conversations are personal); fall back to a
	// stable "local" id when auth is disabled so single-user installs persist.
	convUserID := auth.ContextUserID(r)
	if convUserID == "" {
		convUserID = copilot.FallbackConversationUser
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	newConversation := conversationID == ""
	if newConversation {
		conversationID = copilot.NewConversationID()
	}
	writeSSEEvent(w, flusher, "meta", map[string]any{"conversationId": conversationID})

	logger := slog.Default().With(
		slog.String("component", "copilot"),
		slog.String("user", auth.ContextUserID(r)),
		slog.String("cluster", clusterName),
		slog.String("provider", cfg.Primary.Provider),
		slog.String("model", cfg.Primary.Model),
		slog.String("trigger", trigger),
	)

	// Multi-step tool calling loop. The round budget is configurable
	// (KUBEBOLT_AI_MAX_ROUNDS / Settings → Copilot) because a small,
	// sequential model (e.g. Haiku, which calls tools one at a time) needs
	// more headroom to converge on a deep RCA than a model that batches calls.
	maxRounds := cfg.MaxRounds
	if maxRounds < config.MinMaxRounds {
		maxRounds = config.DefaultMaxRounds
	}
	// Detach from req.Messages so subsequent appends in the round loop never
	// reach back into the caller's slice via shared backing array.
	messages := append([]copilot.Message(nil), req.Messages...)

	// Phase 6: inject per-session context (cluster + current_view + Now block)
	// as a prefix on the first user message. We compute the prefix string
	// once per request and apply it ONLY to the LLM-facing slice via
	// `withSessionContextPrefix` at the chat-request site below — never
	// mutate `messages` in place. Reasons:
	//   - The frontend replaces its transcript with `finalMessages` on the
	//     `done` event; if we mutated, the session-context block would
	//     appear inside the user bubble in the chat UI.
	//   - On the next turn, the frontend re-sends that already-prefixed
	//     content; mutating again would stack a second prefix, then a
	//     third, etc. (observed in production: 4 nested Session Context
	//     blocks after 4 turns).
	sessionCtx := ""
	if len(messages) > 0 {
		sessionCtx = copilot.BuildSessionContext(clusterName, req.CurrentPath, time.Now(), req.ClientTimezone)
		// Tell Kobi the live governance-toggle state so it explains a blocked
		// action as policy (not RBAC). Appended to the per-session prefix —
		// keeps the system prompt's cache prefix byte-identical. Empty when
		// both toggles are ON (the default), so the common case adds nothing.
		if gov := copilot.GovernanceContextBlock(govCfg.ActionsEnabled, govCfg.DestructiveActionsEnabled); gov != "" {
			sessionCtx += "\n\n" + gov
		}
	}

	usedFallback := false

	// Session-level accounting
	var sessionUsage copilot.Usage
	var sessionToolBytes int
	var sessionToolCalls int
	sessionStart := time.Now()
	roundsUsed := 0
	// Provider-reported usage of the most recent round. Stored on the
	// conversation as LastRoundUsage so a resumed chat seeds its first
	// auto-compact decision accurately (same hint the frontend carries).
	var lastTurnUsage copilot.Usage

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
				Timestamp:      time.Now(),
				UserID:         auth.ContextUserID(r),
				Cluster:        clusterName,
				ConversationID: conversationID,
				Provider:       cfg.Primary.Provider,
				// Model is resolved through copilot.ResolvedModel so the
				// stored value reflects the model the provider ACTUALLY
				// uses. When KUBEBOLT_AI_MODEL is unset the raw
				// cfg.Primary.Model is empty, but the provider
				// applies its own default (claude-sonnet-4-6 / gpt-4o).
				// Persisting the empty string here makes the admin Copilot
				// Usage page lose pricing — PricingFor("anthropic", "")
				// returns no-match → estimatedUsd=0 → "no known pricing"
				// even though real cost was incurred. ResolvedModel
				// centralises the same fallback the providers do.
				Model:      copilot.ResolvedModel(cfg.Primary.Provider, cfg.Primary.Model),
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

	// persistConversation write-throughs the full transcript so the operator
	// can resume after a refresh / re-login. Called once per request on every
	// terminal path (done / error / max_rounds) — even an errored turn saves
	// what happened so the user can pick it back up. No-op when no store is
	// wired (auth/BoltDB disabled). Stays out of the stateless chat loop:
	// resume just pre-populates `messages` the loop already consumes.
	persistConversation := func(msgs []copilot.Message) {
		if h.copilotConversations == nil || len(msgs) == 0 {
			return
		}
		tenant := copilot.DefaultConversationTenant
		var lru *copilot.Usage
		if lastTurnUsage.Total() > 0 {
			u := lastTurnUsage
			lru = &u
		}
		// Load any prior copy so MergeConversationRecord can preserve the
		// identity-stable fields (CreatedAt, refined Title, Archived, origin
		// Trigger, insight provenance, last-round usage seed) across resumes.
		existing, _, _ := h.copilotConversations.Get(tenant, convUserID, conversationID)
		rec := copilot.MergeConversationRecord(copilot.ConversationUpsertInput{
			ID:                   conversationID,
			TenantID:             tenant,
			UserID:               convUserID,
			ClusterID:            clusterName,
			Provider:             cfg.Primary.Provider,
			Model:                copilot.ResolvedModel(cfg.Primary.Provider, cfg.Primary.Model),
			Messages:             msgs,
			Trigger:              trigger,
			OriginatingInsightID: strings.TrimSpace(req.OriginatingInsightID),
			LastRoundUsage:       lru,
			Now:                  time.Now(),
		}, existing)
		if err := h.copilotConversations.Upsert(rec); err != nil {
			logger.Warn("failed to persist conversation",
				slog.String("error", err.Error()),
				slog.String("conversationId", conversationID),
			)
			return
		}
		// Refine the heuristic title with the cheap model for a brand-new
		// conversation — in the background so it never delays the response.
		if newConversation && rec.FirstUserMessage() != "" {
			go h.refineConversationTitle(convUserID, clusterName, conversationID, cfg.Primary, rec.FirstUserMessage(), rec.LastAssistantMessage())
		}
	}

	// Resolve the compaction trigger: budget × threshold. The budget is the
	// user's ceiling (defaults to the model's full context window); the
	// threshold is how full the conversation gets before we compact.
	sessionBudget := cfg.SessionBudgetTokens
	if sessionBudget <= 0 {
		sessionBudget = copilot.ContextWindowFor(cfg.Primary.Provider, cfg.Primary.Model)
	}
	compactTrigger := int(float64(sessionBudget) * cfg.AutoCompactThreshold)

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
		// Active provider for this round: the primary, unless an earlier round
		// already fell over to the fallback — then we STICK with the fallback
		// for the rest of the session instead of re-trying a degraded primary
		// every round. Re-trying surfaced a confusing upstream error (e.g. a
		// 502) AFTER the fallback had already answered the conversation.
		activeProvider := cfg.Primary
		if usedFallback && cfg.Fallback != nil {
			activeProvider = *cfg.Fallback
		}

		// Auto-compact when the conversation approaches the budget.
		// Uses a cheap-tier model of the same provider to summarize the
		// older turns, then replaces them with a single summary message.
		if cfg.AutoCompact {
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
					PreserveTurns: cfg.CompactPreserveTurns,
					Provider:      activeProvider,
					CompactModel:  cfg.CompactModel,
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

		// Build the chat request for this round. Apply the session-context
		// prefix to a fresh slice so the canonical `messages` (echoed back
		// to the UI) stays exactly what the operator typed.
		chatReq := copilot.ChatRequest{
			System:    systemPrompt,
			Messages:  withSessionContextPrefix(messages, sessionCtx),
			Tools:     tools,
			Provider:  activeProvider,
			MaxTokens: cfg.MaxTokens,
		}

		resp, err := h.callProvider(r, chatReq)

		// On recoverable error, try fallback if configured
		if err != nil && copilot.IsRecoverable(err) && cfg.Fallback != nil && !usedFallback {
			logger.Warn("copilot primary failed, retrying with fallback",
				slog.String("error", err.Error()),
				slog.String("fallbackProvider", cfg.Fallback.Provider),
				slog.String("fallbackModel", cfg.Fallback.Model),
			)
			chatReq.Provider = *cfg.Fallback
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
			persistConversation(messages)
			finish("error")
			return
		}

		// Accumulate token usage for the session
		sessionUsage.Add(resp.Usage)
		lastTurnUsage = resp.Usage
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
					Role:      copilot.RoleAssistant,
					Content:   resp.Text,
					Timestamp: time.Now(),
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
			if cfg.AutoCompact && lastRoundFullInput >= compactTrigger {
				logger.Info("copilot auto-compact triggered",
					slog.String("mode", "reactive"),
					slog.Int("contextTokens", lastRoundFullInput),
					slog.Int("trigger", compactTrigger),
					slog.Int("budget", sessionBudget),
				)
				cr, cerr := copilot.Compact(r.Context(), finalMessages, copilot.CompactOptions{
					PreserveTurns: cfg.CompactPreserveTurns,
					Provider:      activeProvider,
					CompactModel:  cfg.CompactModel,
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
				"messages":       finalMessages,
				"conversationId": conversationID,
			})
			persistConversation(finalMessages)
			finish("done")
			return
		}

		// Append the assistant message with tool calls
		messages = append(messages, copilot.Message{
			Role:      copilot.RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
			Timestamp: time.Now(),
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
			Timestamp:   time.Now(),
		})

		// Continue the loop — model will see tool results and produce its next response
	}

	// Hit the max rounds limit. Instead of a dead error that throws away
	// everything Kobi gathered, run ONE final tools-free turn that summarizes
	// what was found and tells the operator they can ask to continue. Small,
	// sequential models exhaust the round budget mid-RCA precisely when they're
	// being most useful — a bare "reached max tool call rounds" is the worst
	// possible payload at that moment.
	logger.Warn("copilot hit max rounds",
		slog.Int("maxRounds", maxRounds),
	)
	// Forward-compatible signal so the UI can later offer a "Continue
	// investigation" affordance (1.16). Unknown meta keys are ignored today.
	writeSSEEvent(w, flusher, "meta", map[string]any{"maxRoundsReached": maxRounds})

	// Same provider-selection rule the loop uses: stick with the fallback if an
	// earlier round already fell over to it.
	closeProvider := cfg.Primary
	if usedFallback && cfg.Fallback != nil {
		closeProvider = *cfg.Fallback
	}
	// Deliver the close directive as a final USER turn, not a system-prompt
	// tail. In-vivo (maxRounds=5 on the 3-tier RCA) the system-tail directive
	// was ignored — the model kept narrating "let me check…" instead of
	// summarizing. A trailing instruction message is far more salient. It steers
	// the close turn ONLY and is never persisted (closeMessages is a copy;
	// finalMessages derives from `messages`).
	closeMessages := append(append([]copilot.Message(nil), messages...), copilot.Message{
		Role:      copilot.RoleUser,
		Content:   copilot.MaxRoundsCloseDirective(maxRounds),
		Timestamp: time.Now(),
	})
	closeReq := copilot.ChatRequest{
		System:    systemPrompt,
		Messages:  withSessionContextPrefix(closeMessages, sessionCtx),
		Tools:     nil, // force a text-only answer — no more tool calls
		Provider:  closeProvider,
		MaxTokens: cfg.MaxTokens,
	}
	resp, err := h.callProvider(r, closeReq)
	if err != nil && copilot.IsRecoverable(err) && cfg.Fallback != nil && !usedFallback {
		closeReq.Provider = *cfg.Fallback
		resp, err = h.callProvider(r, closeReq)
		if err == nil {
			usedFallback = true
			writeSSEEvent(w, flusher, "meta", map[string]bool{"fallback": true})
		}
	}

	finalMessages := messages
	if err != nil {
		// Even the close turn failed — fall back to the honest error, but keep
		// the transcript we have so the next resume isn't empty.
		logger.Error("copilot max-rounds close turn failed",
			slog.String("error", err.Error()),
		)
		writeSSEEvent(w, flusher, "error", map[string]string{
			"error": fmt.Sprintf("reached max tool call rounds (%d)", maxRounds),
		})
		writeSSEEvent(w, flusher, "done", map[string]any{
			"messages":       finalMessages,
			"conversationId": conversationID,
		})
		persistConversation(finalMessages)
		finish("max_rounds")
		return
	}

	sessionUsage.Add(resp.Usage)
	lastTurnUsage = resp.Usage
	writeSSEEvent(w, flusher, "usage", map[string]any{
		"round":   maxRounds,
		"turn":    resp.Usage,
		"session": sessionUsage,
	})
	if resp.Text != "" {
		writeSSEEvent(w, flusher, "text", map[string]string{"text": resp.Text})
		finalMessages = append(finalMessages, copilot.Message{
			Role:      copilot.RoleAssistant,
			Content:   resp.Text,
			Timestamp: time.Now(),
		})
	}
	writeSSEEvent(w, flusher, "done", map[string]any{
		"messages":       finalMessages,
		"conversationId": conversationID,
	})
	persistConversation(finalMessages)
	finish("max_rounds")
}

// recordAuxUsage persists a SessionRecord for an LLM call made OUTSIDE the
// main chat loop (auto-title, standalone manual compaction). The rule is "no
// LLM consumption without a usage record" — these calls spend real tokens, so
// they must show up in the admin cost analytics, attributed to the user (and
// the conversation, when there is one). Best-effort; nil store = no-op.
func (h *handlers) recordAuxUsage(userID, cluster, conversationID, trigger, provider, model string, usage copilot.Usage, dur time.Duration) {
	if h.copilotUsage == nil {
		return
	}
	if err := h.copilotUsage.Record(&copilot.SessionRecord{
		Timestamp:      time.Now(),
		UserID:         userID,
		Cluster:        cluster,
		ConversationID: conversationID,
		Provider:       provider,
		Model:          model,
		Trigger:        trigger,
		Reason:         "done",
		Rounds:         1,
		Usage:          usage,
		DurationMs:     dur.Milliseconds(),
	}); err != nil {
		slog.Default().Warn("failed to record auxiliary copilot usage",
			slog.String("trigger", trigger),
			slog.String("error", err.Error()),
		)
	}
}

// refineConversationTitle asks the cheap model for a better title than the
// first-prompt heuristic and persists it. Best-effort + detached: runs in its
// own goroutine with a fresh context (the request's context is already
// cancelled once the SSE stream closes), and silently keeps the heuristic on
// any failure. SetTitle is a field-level load-modify-save so it never clobbers
// a transcript append from a concurrent turn.
//
// The title call's token usage is always recorded (even when the reply is
// unusable) so no LLM spend goes unaccounted.
func (h *handlers) refineConversationTitle(userID, cluster, conversationID string, provider config.ProviderConfig, firstUser, assistantReply string) {
	if h.copilotConversations == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	res, err := copilot.GenerateTitle(ctx, provider, firstUser, assistantReply)
	if res != nil && res.Usage.Total() > 0 {
		h.recordAuxUsage(userID, cluster, conversationID, "auto_title", provider.Provider, res.Model, res.Usage, time.Since(start))
	}
	if err != nil || res == nil || res.Title == "" {
		return
	}
	if err := h.copilotConversations.SetTitle(copilot.DefaultConversationTenant, userID, conversationID, res.Title); err != nil {
		slog.Default().Warn("failed to persist refined conversation title",
			slog.String("error", err.Error()),
			slog.String("conversationId", conversationID),
		)
	}
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
	// ConversationID lets the recorded compaction usage cross-reference the
	// conversation it summarized. Optional.
	ConversationID string `json:"conversationId,omitempty"`
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
	// Same snapshot-per-request pattern as HandleCopilotChat — read
	// once via the runtime resolver, use the local `cfg` thereafter.
	cfg := h.liveCopilotConfig()
	if !cfg.Enabled {
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

	compactStart := time.Now()
	cr, err := copilot.Compact(r.Context(), req.Messages, copilot.CompactOptions{
		PreserveTurns: cfg.CompactPreserveTurns,
		Provider:      cfg.Primary,
		CompactModel:  cfg.CompactModel,
		ResetAll:      req.ResetAll,
	})
	if err != nil {
		logger.Error("copilot manual compact failed", slog.String("error", err.Error()))
		respondError(w, http.StatusBadGateway, friendlyCopilotError(err))
		return
	}

	// A manual compaction is a real LLM call — record its token usage so it
	// isn't invisible spend (the "no LLM consumption without a usage record"
	// rule). Resolve the model the summarizer actually used for pricing.
	compactModel := copilot.ResolvedModel(cfg.Primary.Provider, cr.UsedModel)
	h.recordAuxUsage(auth.ContextUserID(r), h.manager.ActiveContext(), req.ConversationID,
		"manual_compact", cfg.Primary.Provider, compactModel, cr.Usage, time.Since(compactStart))

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
