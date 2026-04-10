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
		MaxTokens: 4096,
	}
	if v := os.Getenv("KUBEBOLT_AI_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxTokens = n
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
