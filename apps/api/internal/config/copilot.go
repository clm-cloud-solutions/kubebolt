package config

import (
	"os"
	"strconv"
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
		AutoCompactThreshold: 0.80,
		CompactPreserveTurns: 3,
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
