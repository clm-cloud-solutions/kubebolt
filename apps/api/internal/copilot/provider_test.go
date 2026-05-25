package copilot

import "testing"

// Tests for ResolvedModel — the helper that mirrors the
// `if model == ""` fallback inside each provider's Chat impl so callers
// persisting the "model actually used" can resolve the same value
// without making an API call first.
//
// The bug this helper closes (admin Copilot Usage shows "$0.0000 / no
// known pricing" even though real API calls happen): an OSS install
// that omits KUBEBOLT_AI_MODEL leaves Primary.Model = "". The session
// record stamps Model = "" and downstream pricing lookup misses.
// ResolvedModel makes the resolution explicit at the persistence layer.

func TestResolvedModel(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		configured string
		want       string
	}{
		// Operator set KUBEBOLT_AI_MODEL — round-trip unchanged.
		{
			name:       "configured wins over default — anthropic",
			provider:   "anthropic",
			configured: "claude-opus-4-7",
			want:       "claude-opus-4-7",
		},
		{
			name:       "configured wins over default — openai",
			provider:   "openai",
			configured: "gpt-4o-mini",
			want:       "gpt-4o-mini",
		},
		// The bug case — empty config + known provider falls back to
		// the same default the provider impl uses internally.
		{
			name:       "anthropic empty → default",
			provider:   "anthropic",
			configured: "",
			want:       anthropicDefaultModel,
		},
		{
			name:       "openai empty → default",
			provider:   "openai",
			configured: "",
			want:       openaiDefaultModel,
		},
		// Case-insensitive provider name (mirrors the convention used in
		// pricing.go's PricingFor — operators sometimes capitalize).
		{
			name:       "case-insensitive provider lookup",
			provider:   "Anthropic",
			configured: "",
			want:       anthropicDefaultModel,
		},
		// Unknown provider with empty config returns empty — same posture
		// as PricingFor for unknown models. Caller decides what to do.
		{
			name:       "unknown provider + empty → empty (no guessing)",
			provider:   "minimax",
			configured: "",
			want:       "",
		},
		// Unknown provider with configured model returns the configured
		// value — we don't second-guess the operator's explicit choice.
		{
			name:       "unknown provider + configured passes through",
			provider:   "minimax",
			configured: "minimax-m2.7",
			want:       "minimax-m2.7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolvedModel(tc.provider, tc.configured)
			if got != tc.want {
				t.Errorf("ResolvedModel(%q, %q) = %q, want %q",
					tc.provider, tc.configured, got, tc.want)
			}
		})
	}
}

// TestResolvedModelMatchesProviderInternalFallback is a regression
// guard: if someone updates anthropicDefaultModel or openaiDefaultModel
// in the provider impl files, ResolvedModel must return the new value
// so the SessionRecord stays consistent with what the API call actually
// uses. Catching drift here is cheaper than tracing a "$0.0000" report
// back to a constant divergence.
func TestResolvedModelMatchesProviderInternalFallback(t *testing.T) {
	// Anthropic — must equal the constant used in anthropic.go's Chat
	// when model arg is "". If anthropic.go's `if model == "" { model =
	// anthropicDefaultModel }` ever changes shape, this test should
	// fail and the helper updated alongside.
	if got := ResolvedModel("anthropic", ""); got != anthropicDefaultModel {
		t.Errorf("anthropic default drift: ResolvedModel returned %q, anthropicDefaultModel = %q",
			got, anthropicDefaultModel)
	}
	if got := ResolvedModel("openai", ""); got != openaiDefaultModel {
		t.Errorf("openai default drift: ResolvedModel returned %q, openaiDefaultModel = %q",
			got, openaiDefaultModel)
	}
}

// TestResolvedModelPricesFlowEndToEnd verifies the actual outcome the
// helper is meant to fix: the resolved model name plugs into PricingFor
// and yields a non-zero estimate. Locks in the chain
// (configured-empty) → ResolvedModel → PricingFor → cost > 0.
func TestResolvedModelPricesFlowEndToEnd(t *testing.T) {
	resolved := ResolvedModel("anthropic", "")
	pricing, ok := PricingFor("anthropic", resolved)
	if !ok {
		t.Fatalf("expected pricing entry for resolved default %q", resolved)
	}
	usage := Usage{InputTokens: 1_000_000, OutputTokens: 100_000}
	cost := EstimateUSD(usage, pricing)
	if cost <= 0 {
		t.Errorf("expected non-zero estimate for default model usage, got %.4f", cost)
	}
}
