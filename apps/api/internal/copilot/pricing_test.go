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
		{"openai", "grok-4.3"},
		{"openai", "grok-4.20-multi-agent-0309"},
		{"openai", "grok-4.20-0309-reasoning"},
		{"openai", "grok-4.20-0309-non-reasoning"},
		{"openai", "grok-4-1-fast-reasoning"},
		{"openai", "grok-4-1-fast-non-reasoning"},
		{"openai", "MiniMax-M2.7"},
		{"openai", "MiniMax-M2.7-highspeed"},
		{"openai", "MiniMax-M2.5"},
		{"openai", "MiniMax-M2.5-highspeed"},
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

// TestPricingFor_LongestPrefixWins guards against the latent
// nondeterminism where two map keys both match (e.g. "minimax-m2.7"
// is a prefix of "minimax-m2.7-highspeed", or "gpt-5" of "gpt-5-mini")
// and Go's randomized map iteration could return either price. The
// fix is in PricingFor: iterate keys sorted by length descending.
// Run with -count=20 to expose iteration-order flakes if regressed.
func TestPricingFor_LongestPrefixWins(t *testing.T) {
	cases := []struct {
		model        string
		wantInput    float64
		wantOutput   float64
		description  string
	}{
		{"MiniMax-M2.7-highspeed", 0.60, 2.40, "highspeed must not collapse to base M2.7 price"},
		{"MiniMax-M2.7", 0.30, 1.20, "base M2.7 keeps its own price"},
		{"MiniMax-M2.5-highspeed", 0.60, 2.40, "highspeed must not collapse to base M2.5 price"},
		{"MiniMax-M2.5", 0.30, 1.20, "base M2.5 keeps its own price"},
		{"gpt-5-mini-2025-08-07", 0.25, 2.00, "gpt-5-mini dated variant must not collapse to gpt-5 price"},
		{"gpt-5", 2.50, 10, "base gpt-5 keeps its own price"},
	}
	for _, c := range cases {
		p, ok := PricingFor("openai", c.model)
		if !ok {
			t.Errorf("%s: PricingFor(%q) returned ok=false", c.description, c.model)
			continue
		}
		if p.Input != c.wantInput || p.Output != c.wantOutput {
			t.Errorf("%s: PricingFor(%q) = {Input:%v Output:%v}, want {Input:%v Output:%v}",
				c.description, c.model, p.Input, p.Output, c.wantInput, c.wantOutput)
		}
	}
}
