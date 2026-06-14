package config

import (
	"os"
	"strconv"
	"time"
)

// Action-progress timeout bounds. After an action executes, the frontend
// polls the workload for convergence (observedGeneration + ready==desired)
// and gives up after this long, marking the card "did not converge" and
// asking Kobi to investigate why. Configurable via
// KUBEBOLT_AI_ACTION_PROGRESS_TIMEOUT (a Go duration, e.g. "120s", "2m").
const (
	DefaultActionProgressTimeout = 90 * time.Second
	// Floor so a fat-fingered tiny value (e.g. "1s") doesn't declare every
	// real rollout stalled before the first poll even lands.
	MinActionProgressTimeout = 10 * time.Second
)

// Tool-call round bounds. A "round" is one model turn that calls ≥1 tool
// (several parallel tool calls count as ONE). The multi-step loop runs until
// the model answers without tools or it hits MaxRounds. Configurable via
// KUBEBOLT_AI_MAX_ROUNDS so a small, sequential model (e.g. Haiku, which
// calls tools one at a time) can be given more headroom to converge on a
// deep RCA than a model that batches calls in parallel.
const (
	DefaultMaxRounds = 20
	// MinMaxRounds keeps at least a couple of tool turns available — a value
	// of 0/1 would make Kobi useless for anything needing data.
	MinMaxRounds = 2
	// MaxMaxRounds caps cost/latency: each round is a full provider call plus
	// tool execution, so an unbounded ceiling lets one runaway session burn
	// tokens. 40 is generous headroom over the 20 default.
	MaxMaxRounds = 40
)

// ProviderConfig holds settings for a single LLM provider (primary or fallback).
type ProviderConfig struct {
	Provider string // "anthropic" | "openai" | "custom"
	APIKey   string // never exposed to the frontend
	Model    string // optional, provider default if empty
	BaseURL  string // optional, for custom/self-hosted endpoints
}

// CopilotConfig holds the AI Copilot configuration loaded from env vars.
type CopilotConfig struct {
	Enabled   bool
	Primary   ProviderConfig
	Fallback  *ProviderConfig // nil if not configured
	MaxTokens int             // default 4096

	// Conversation memory management. See KUBEBOLT_AI_* docs in .env.example.
	AutoCompact          bool    // default true
	SessionBudgetTokens  int     // 0 = auto from model context window
	AutoCompactThreshold float64 // default 0.80
	CompactModel         string  // empty = auto-pick cheap model for provider
	CompactPreserveTurns int     // default 3

	// UI: render persistent collapsible cards for each tool call (with the
	// tool name, status, and the result content) inline in the chat panel.
	// Default true — operators see what Kobi did and can verify the data.
	// Set KUBEBOLT_AI_SHOW_TOOL_CALLS=false to keep only the final assistant
	// text and a transient loading indicator (the pre-2026-05-15 behavior).
	ShowToolCalls bool

	// Action governance (Sprint 1). ActionsEnabled is the master switch:
	// when false, the propose_* tools are withheld from the LLM and Kobi
	// reverts to read-only advisory (pre-action-calling 1.12 behavior).
	// DestructiveActionsEnabled gates the destructive verbs (delete,
	// scale-to-0); when false they're withheld + rejected server-side.
	// Both default TRUE — the action surface already shipped, so the
	// toggles are an opt-out for compliance-conscious operators, not a
	// behavior change. KUBEBOLT_AI_ACTIONS_ENABLED /
	// KUBEBOLT_AI_DESTRUCTIVE_ACTIONS_ENABLED.
	ActionsEnabled            bool
	DestructiveActionsEnabled bool

	// ActionProgressTimeout bounds how long the UI polls an executed action
	// for convergence before declaring it stalled and auto-investigating.
	// Default DefaultActionProgressTimeout; KUBEBOLT_AI_ACTION_PROGRESS_TIMEOUT.
	ActionProgressTimeout time.Duration

	// MaxRounds bounds the multi-step tool-calling loop (one model turn that
	// calls ≥1 tool = one round). On exhaustion the handler runs one final
	// tools-free turn that summarizes what was found instead of erroring out.
	// Default DefaultMaxRounds; KUBEBOLT_AI_MAX_ROUNDS, clamped to
	// [MinMaxRounds, MaxMaxRounds].
	MaxRounds int
}

// LoadCopilotConfig reads copilot configuration from KUBEBOLT_AI_* env vars.
func LoadCopilotConfig() CopilotConfig {
	cfg := CopilotConfig{
		Primary: ProviderConfig{
			Provider: getEnvOr("KUBEBOLT_AI_PROVIDER", "anthropic"),
			APIKey:   os.Getenv("KUBEBOLT_AI_API_KEY"),
			Model:    os.Getenv("KUBEBOLT_AI_MODEL"),
			BaseURL:  os.Getenv("KUBEBOLT_AI_BASE_URL"),
		},
		MaxTokens:            4096,
		AutoCompact:          true,
		AutoCompactThreshold:      0.80,
		CompactPreserveTurns:      3,
		ShowToolCalls:             true,
		ActionsEnabled:            true,
		DestructiveActionsEnabled: true,
		ActionProgressTimeout:     DefaultActionProgressTimeout,
		MaxRounds:                 DefaultMaxRounds,
	}
	if v := os.Getenv("KUBEBOLT_AI_ACTIONS_ENABLED"); v == "false" || v == "0" {
		cfg.ActionsEnabled = false
	}
	if v := os.Getenv("KUBEBOLT_AI_DESTRUCTIVE_ACTIONS_ENABLED"); v == "false" || v == "0" {
		cfg.DestructiveActionsEnabled = false
	}
	if v := os.Getenv("KUBEBOLT_AI_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxTokens = n
		}
	}
	if v := os.Getenv("KUBEBOLT_AI_AUTO_COMPACT"); v == "false" || v == "0" {
		cfg.AutoCompact = false
	}
	if v := os.Getenv("KUBEBOLT_AI_SESSION_BUDGET_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SessionBudgetTokens = n
		}
	}
	if v := os.Getenv("KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f < 1 {
			cfg.AutoCompactThreshold = f
		}
	}
	if v := os.Getenv("KUBEBOLT_AI_COMPACT_MODEL"); v != "" {
		cfg.CompactModel = v
	}
	if v := os.Getenv("KUBEBOLT_AI_COMPACT_PRESERVE_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.CompactPreserveTurns = n
		}
	}
	if v := os.Getenv("KUBEBOLT_AI_SHOW_TOOL_CALLS"); v == "false" || v == "0" {
		cfg.ShowToolCalls = false
	}
	if v := os.Getenv("KUBEBOLT_AI_ACTION_PROGRESS_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= MinActionProgressTimeout {
			cfg.ActionProgressTimeout = d
		}
	}
	if v := os.Getenv("KUBEBOLT_AI_MAX_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxRounds = ClampMaxRounds(n)
		}
	}

	// Optional fallback — only enabled when its API key is present
	if fbKey := os.Getenv("KUBEBOLT_AI_FALLBACK_API_KEY"); fbKey != "" {
		cfg.Fallback = &ProviderConfig{
			Provider: getEnvOr("KUBEBOLT_AI_FALLBACK_PROVIDER", cfg.Primary.Provider),
			APIKey:   fbKey,
			Model:    os.Getenv("KUBEBOLT_AI_FALLBACK_MODEL"),
			BaseURL:  os.Getenv("KUBEBOLT_AI_FALLBACK_BASE_URL"),
		}
	}

	cfg.Enabled = cfg.Primary.APIKey != ""
	return cfg
}

func getEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ClampMaxRounds bounds a configured round limit to [MinMaxRounds, MaxMaxRounds]
// so neither a too-small value (Kobi can't fetch data) nor a runaway ceiling
// (cost/latency blowout) slips through. Shared by env parsing and the
// settings-override merge so both honor the same bounds.
func ClampMaxRounds(n int) int {
	if n < MinMaxRounds {
		return MinMaxRounds
	}
	if n > MaxMaxRounds {
		return MaxMaxRounds
	}
	return n
}
