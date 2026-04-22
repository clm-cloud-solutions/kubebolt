package config

import (
	"os"
	"testing"
)

// clearCopilotEnv wipes every KUBEBOLT_AI_* env var so tests don't pick up
// leakage from prior tests or the developer's shell.
func clearCopilotEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"KUBEBOLT_AI_PROVIDER", "KUBEBOLT_AI_API_KEY", "KUBEBOLT_AI_MODEL", "KUBEBOLT_AI_BASE_URL",
		"KUBEBOLT_AI_MAX_TOKENS",
		"KUBEBOLT_AI_AUTO_COMPACT", "KUBEBOLT_AI_SESSION_BUDGET_TOKENS",
		"KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD", "KUBEBOLT_AI_COMPACT_MODEL",
		"KUBEBOLT_AI_COMPACT_PRESERVE_TURNS",
		"KUBEBOLT_AI_FALLBACK_PROVIDER", "KUBEBOLT_AI_FALLBACK_API_KEY",
		"KUBEBOLT_AI_FALLBACK_MODEL", "KUBEBOLT_AI_FALLBACK_BASE_URL",
	}
	for _, v := range vars {
		t.Setenv(v, "") // t.Setenv automatically restores on test end
		os.Unsetenv(v)
	}
}

func TestLoadCopilotConfig_Defaults(t *testing.T) {
	clearCopilotEnv(t)
	cfg := LoadCopilotConfig()

	if cfg.Enabled {
		t.Error("disabled when no API key")
	}
	if cfg.Primary.Provider != "anthropic" {
		t.Errorf("default provider = %q, want anthropic", cfg.Primary.Provider)
	}
	if cfg.MaxTokens != 4096 {
		t.Errorf("default MaxTokens = %d, want 4096", cfg.MaxTokens)
	}
	if !cfg.AutoCompact {
		t.Error("AutoCompact should default to true")
	}
	if cfg.AutoCompactThreshold != 0.80 {
		t.Errorf("default threshold = %v, want 0.80", cfg.AutoCompactThreshold)
	}
	if cfg.CompactPreserveTurns != 3 {
		t.Errorf("default preserve turns = %d, want 3", cfg.CompactPreserveTurns)
	}
	if cfg.Fallback != nil {
		t.Error("no fallback without API key")
	}
}

func TestLoadCopilotConfig_EnabledWithKey(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("KUBEBOLT_AI_API_KEY", "sk-test")
	t.Setenv("KUBEBOLT_AI_MODEL", "claude-sonnet-4-6")
	t.Setenv("KUBEBOLT_AI_BASE_URL", "https://api.example.com")

	cfg := LoadCopilotConfig()
	if !cfg.Enabled {
		t.Error("should be enabled with API key")
	}
	if cfg.Primary.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", cfg.Primary.Model)
	}
	if cfg.Primary.BaseURL != "https://api.example.com" {
		t.Errorf("base URL = %q", cfg.Primary.BaseURL)
	}
}

func TestLoadCopilotConfig_MemoryVars(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("KUBEBOLT_AI_API_KEY", "sk")
	t.Setenv("KUBEBOLT_AI_AUTO_COMPACT", "false")
	t.Setenv("KUBEBOLT_AI_SESSION_BUDGET_TOKENS", "50000")
	t.Setenv("KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD", "0.9")
	t.Setenv("KUBEBOLT_AI_COMPACT_MODEL", "haiku-custom")
	t.Setenv("KUBEBOLT_AI_COMPACT_PRESERVE_TURNS", "5")

	cfg := LoadCopilotConfig()
	if cfg.AutoCompact {
		t.Error("AutoCompact should be false when env=false")
	}
	if cfg.SessionBudgetTokens != 50000 {
		t.Errorf("budget = %d", cfg.SessionBudgetTokens)
	}
	if cfg.AutoCompactThreshold != 0.9 {
		t.Errorf("threshold = %v", cfg.AutoCompactThreshold)
	}
	if cfg.CompactModel != "haiku-custom" {
		t.Errorf("compact model = %q", cfg.CompactModel)
	}
	if cfg.CompactPreserveTurns != 5 {
		t.Errorf("preserve turns = %d", cfg.CompactPreserveTurns)
	}
}

func TestLoadCopilotConfig_IgnoresInvalidValues(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("KUBEBOLT_AI_API_KEY", "sk")
	t.Setenv("KUBEBOLT_AI_MAX_TOKENS", "not-a-number")
	t.Setenv("KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD", "1.5") // must be < 1
	t.Setenv("KUBEBOLT_AI_SESSION_BUDGET_TOKENS", "-100")
	t.Setenv("KUBEBOLT_AI_COMPACT_PRESERVE_TURNS", "nonsense")

	cfg := LoadCopilotConfig()
	if cfg.MaxTokens != 4096 {
		t.Errorf("bad int should fall back to default; got %d", cfg.MaxTokens)
	}
	if cfg.AutoCompactThreshold != 0.80 {
		t.Errorf("threshold > 1 should fall back to default; got %v", cfg.AutoCompactThreshold)
	}
	if cfg.SessionBudgetTokens != 0 {
		t.Errorf("negative budget should not be applied; got %d", cfg.SessionBudgetTokens)
	}
	if cfg.CompactPreserveTurns != 3 {
		t.Errorf("bad preserve should fall back to default; got %d", cfg.CompactPreserveTurns)
	}
}

func TestLoadCopilotConfig_Fallback(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("KUBEBOLT_AI_API_KEY", "primary-key")
	t.Setenv("KUBEBOLT_AI_FALLBACK_API_KEY", "fallback-key")
	t.Setenv("KUBEBOLT_AI_FALLBACK_MODEL", "gpt-4o-mini")

	cfg := LoadCopilotConfig()
	if cfg.Fallback == nil {
		t.Fatal("Fallback should be set when fallback API key present")
	}
	if cfg.Fallback.APIKey != "fallback-key" {
		t.Errorf("fallback key = %q", cfg.Fallback.APIKey)
	}
	if cfg.Fallback.Model != "gpt-4o-mini" {
		t.Errorf("fallback model = %q", cfg.Fallback.Model)
	}
	if cfg.Fallback.Provider != "anthropic" {
		t.Errorf("fallback provider should inherit primary; got %q", cfg.Fallback.Provider)
	}
}

func TestGetEnvOr(t *testing.T) {
	t.Setenv("KUBEBOLT_TEST_VAR", "")
	os.Unsetenv("KUBEBOLT_TEST_VAR")
	if got := getEnvOr("KUBEBOLT_TEST_VAR", "default-val"); got != "default-val" {
		t.Errorf("unset var should return default; got %q", got)
	}
	t.Setenv("KUBEBOLT_TEST_VAR", "custom")
	if got := getEnvOr("KUBEBOLT_TEST_VAR", "default-val"); got != "custom" {
		t.Errorf("set var should override default; got %q", got)
	}
}
