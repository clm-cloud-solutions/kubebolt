package copilot

import (
	"math"
	"testing"
)

func TestPricingFor_KnownModels(t *testing.T) {
	cases := []struct {
		provider string
		model    string
	}{
		{"anthropic", "claude-opus-4-7"},
		{"anthropic", "claude-sonnet-4-6"},
		{"anthropic", "claude-haiku-4-5"},
		{"openai", "gpt-5"},
		{"openai", "gpt-5-mini"},
		{"openai", "gpt-4o"},
		{"openai", "gpt-4o-mini"},
	}
	for _, c := range cases {
		if _, ok := PricingFor(c.provider, c.model); !ok {
			t.Errorf("PricingFor(%q, %q): want known, got unknown", c.provider, c.model)
		}
	}
}

func TestPricingFor_PrefixMatch(t *testing.T) {
	// Dated / versioned variants should resolve to their base via prefix match
	if _, ok := PricingFor("anthropic", "claude-sonnet-4-6-20260101"); !ok {
		t.Error("dated Sonnet variant should match base pricing")
	}
	if _, ok := PricingFor("openai", "gpt-5-mini-2025-08-07"); !ok {
		t.Error("dated gpt-5-mini variant should match base pricing")
	}
}

func TestPricingFor_Unknown(t *testing.T) {
	if _, ok := PricingFor("weirdvendor", "nothing"); ok {
		t.Error("unknown model should return ok=false")
	}
	// Empty model returns a substring match against any non-empty pricing key — guard against that.
	if _, ok := PricingFor("openai", ""); ok {
		t.Error("empty model should not match any pricing")
	}
}

func TestEstimateUSD(t *testing.T) {
	// 1M input × $3 + 500K cached × $0.30 + 100K output × $15
	// = $3.00 + $0.15 + $1.50 = $4.65
	p := ModelPricing{Input: 3, CachedInput: 0.30, CacheCreation: 3.75, Output: 15}
	u := Usage{
		InputTokens:     1_000_000,
		CacheReadTokens: 500_000,
		OutputTokens:    100_000,
	}
	got := EstimateUSD(u, p)
	want := 4.65
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("EstimateUSD = %v, want %v", got, want)
	}
}

func TestEstimateUSD_CacheCreation(t *testing.T) {
	p := ModelPricing{Input: 3, CachedInput: 0.30, CacheCreation: 3.75, Output: 15}
	u := Usage{CacheCreationTokens: 100_000}
	got := EstimateUSD(u, p)
	want := 0.375
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cache creation: got %v, want %v", got, want)
	}
}

func TestEstimateUSD_CacheCreationDefaultsToInput(t *testing.T) {
	// When CacheCreation is unset (zero), falls back to Input price.
	p := ModelPricing{Input: 2.50, CachedInput: 0.25, Output: 10}
	u := Usage{CacheCreationTokens: 1_000_000}
	got := EstimateUSD(u, p)
	want := 2.50
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cache-creation fallback: got %v, want %v", got, want)
	}
}

func TestEstimateUSD_ZeroPricing(t *testing.T) {
	got := EstimateUSD(Usage{InputTokens: 1_000_000}, ModelPricing{})
	if got != 0 {
		t.Errorf("zero pricing: got %v, want 0", got)
	}
}
