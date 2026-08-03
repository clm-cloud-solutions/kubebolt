package settings

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Regression: a stored fallback override that carries an API key + model but no
// explicit provider name must inherit the PRIMARY's provider, exactly as the env
// path does (LoadCopilotConfig's getEnvOr("KUBEBOLT_AI_FALLBACK_PROVIDER",
// cfg.Primary.Provider)).
//
// Before the fix the stored path seeded a bare ProviderConfig{}, so the resolved
// fallback had Provider:"" while holding a valid key. It survived the
// drop-check, and the chat handler's recoverable-error retry then failed with a
// bare `unknown provider: ` — masking the real upstream error that triggered the
// retry in the first place.
func TestApplyStoredFallback_InheritsPrimaryProvider(t *testing.T) {
	crypto, err := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}
	enc, err := crypto.encrypt("sk-fallback-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	model := "gpt-4o-mini"

	cases := []struct {
		name  string
		apply func(cfg *config.CopilotConfig, stored *StoredCopilotSettings)
	}{
		{"org path", func(cfg *config.CopilotConfig, stored *StoredCopilotSettings) {
			applyStoredCopilot(cfg, stored, crypto)
		}},
		{"platform path", func(cfg *config.CopilotConfig, stored *StoredCopilotSettings) {
			applyCopilotPlatformFields(cfg, stored, crypto)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.CopilotConfig{
				Primary: config.ProviderConfig{Provider: "anthropic", APIKey: "sk-primary", Model: "claude-haiku-4-5"},
			}
			stored := &StoredCopilotSettings{
				Fallback: &StoredProviderSettings{
					// Provider omitted — what the settings UI sends when its
					// provider select still shows its defaulted value.
					APIKeyEncoded: &enc,
					Model:         &model,
				},
			}
			tc.apply(&cfg, stored)

			if cfg.Fallback == nil {
				t.Fatal("fallback dropped despite a stored API key")
			}
			if cfg.Fallback.Provider != "anthropic" {
				t.Errorf("fallback provider = %q, want it inherited from the primary (\"anthropic\")", cfg.Fallback.Provider)
			}
			if cfg.Fallback.Model != model {
				t.Errorf("fallback model = %q, want %q", cfg.Fallback.Model, model)
			}
		})
	}
}

// An explicit provider in the stored record always wins over the inherited one.
func TestApplyStoredFallback_ExplicitProviderWins(t *testing.T) {
	crypto, err := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}
	enc, err := crypto.encrypt("sk-fallback-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	provider := "openai"
	cfg := config.CopilotConfig{
		Primary: config.ProviderConfig{Provider: "anthropic", APIKey: "sk-primary"},
	}
	applyStoredFallback(&cfg, &StoredProviderSettings{
		Provider:      &provider,
		APIKeyEncoded: &enc,
	}, crypto)

	if cfg.Fallback == nil || cfg.Fallback.Provider != "openai" {
		t.Fatalf("fallback = %+v, want the explicitly stored provider (openai)", cfg.Fallback)
	}
}

// The "no fallback configured" state must still resolve to nil — inheriting a
// provider name must not resurrect a fallback that has no key behind it.
func TestApplyStoredFallback_NoKeyStillDrops(t *testing.T) {
	crypto, err := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}
	model := "gpt-4o-mini"
	cfg := config.CopilotConfig{
		Primary: config.ProviderConfig{Provider: "anthropic", APIKey: "sk-primary"},
	}
	applyStoredFallback(&cfg, &StoredProviderSettings{Model: &model}, crypto)

	if cfg.Fallback != nil {
		t.Errorf("fallback = %+v, want nil when no API key was ever stored", cfg.Fallback)
	}
}

// An env-configured fallback keeps its own provider; the stored override only
// merges the fields it actually carries.
func TestApplyStoredFallback_EnvFallbackProviderPreserved(t *testing.T) {
	crypto, err := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}
	model := "gpt-4o-mini"
	cfg := config.CopilotConfig{
		Primary:  config.ProviderConfig{Provider: "anthropic", APIKey: "sk-primary"},
		Fallback: &config.ProviderConfig{Provider: "openai", APIKey: "sk-env-fallback"},
	}
	applyStoredFallback(&cfg, &StoredProviderSettings{Model: &model}, crypto)

	if cfg.Fallback == nil {
		t.Fatal("env fallback dropped")
	}
	if cfg.Fallback.Provider != "openai" {
		t.Errorf("fallback provider = %q, want the env value \"openai\" preserved", cfg.Fallback.Provider)
	}
	if cfg.Fallback.Model != model {
		t.Errorf("fallback model = %q, want the override applied (%q)", cfg.Fallback.Model, model)
	}
}
