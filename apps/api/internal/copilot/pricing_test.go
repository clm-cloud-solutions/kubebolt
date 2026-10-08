package copilot

import (
	"encoding/json"
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
		{"openai", "grok-4.20-0309-reasoning"},
		{"openai", "grok-4.20-0309-non-reasoning"},
		// grok-4-1-fast / multi-agent removed from xAI's catalog in
		// May 2026 — the prefix-resolvable IDs above are the current
		// chat-compatible variants. The dedicated coverage test
		// (TestPricingFor_AdminCatalogCoverage) now pins the full list.
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
		{"gpt-5", 1.25, 10, "base gpt-5 keeps its own price (1.25 input post-May-2026 reshuffle)"},
		// Opus 5.5 is a SUBSTRING collision with Opus 5, not just a prefix one:
		// without its own entry it resolves to $5/$25 and over-bills by 25%.
		{"claude-opus-5-5", 4, 20, "opus 5.5 must not collapse to the opus 5 price"},
		{"claude-opus-5", 5, 25, "base opus 5 keeps its own price"},
		{"gpt-6-sol", 2.00, 10, "gpt-6-sol is its own tier, not gpt-5.6-sol"},
		{"gpt-6-luna", 0.10, 0.50, "gpt-6-luna is its own tier, not gpt-5.6-luna"},
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

// TestPricingFor_AdminCatalogCoverage pins that every model ID surfaced
// by the Administration → AI (Kobi) → Configuration catalog has a pricing entry that
// PricingFor can resolve. Without this, an operator who selects a model
// from the dropdown sees "$0.0000 / no known pricing" in the Kobi Usage
// dashboard for that model — confusing and undermines the cost-tracking
// surface entirely. The list mirrors
// apps/web/src/pages/admin/settings/modelCatalog.ts as of May 2026;
// when the catalog gains a new model, add its ID here AND the
// corresponding entry in modelPricing.
func TestPricingFor_AdminCatalogCoverage(t *testing.T) {
	cases := []struct {
		provider string
		models   []string
	}{
		{"anthropic", []string{
			"claude-opus-5-5",
			"claude-opus-5",
			"claude-sonnet-5",
			"claude-fable-5",
			"claude-opus-4-8",
			"claude-opus-4-7",
			"claude-sonnet-4-6",
			"claude-haiku-4-5",
			"claude-opus-4-6",
			"claude-sonnet-4-5",
			"claude-opus-4-5-20251101",
			"claude-opus-4-1-20250805",
			"claude-opus-4-20250514",
			"claude-sonnet-4-20250514",
		}},
		{"openai", []string{
			// GPT-6 (current). astra is intentionally absent from the
			// dropdown (see toolsForceReasoningOff) but stays priced.
			"gpt-6-sol", "gpt-6-luna", "gpt-6-astra",
			// GPT-5.6 + chat-latest aliases
			"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
			"gpt-5.5",
			"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano",
			"gpt-5.2", "gpt-5.2-chat-latest",
			"gpt-5.1", "gpt-5.1-chat-latest",
			"gpt-5", "gpt-5-chat-latest",
			// GPT-4.1 family
			"gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano",
			// GPT-4o
			"gpt-4o", "gpt-4o-mini",
			// o-series (reasoning) — o1 removed from catalog by user
			"o4-mini", "o3", "o3-mini",
		}},
		{"openai", []string{
			// xAI Grok (via OpenAI-compatible adapter)
			"grok-4.3",
			"grok-4.20-0309-reasoning",
			"grok-4.20-0309-non-reasoning",
			// DeepSeek
			"deepseek-chat",
			"deepseek-reasoner",
			// Qwen
			"qwen-max", "qwen-plus", "qwen-turbo",
			// Llama via Groq
			"llama-3.3-70b-versatile",
			"llama-3.1-70b-versatile",
			"llama-3.1-8b-instant",
			// Mistral
			"mistral-large-latest",
			"mistral-medium-latest",
			"mistral-small-latest",
			"open-mistral-nemo",
		}},
	}
	for _, group := range cases {
		for _, m := range group.models {
			t.Run(m, func(t *testing.T) {
				p, ok := PricingFor(group.provider, m)
				if !ok {
					t.Errorf("%s/%s: pricing not resolvable — dashboard will show '$0.0000 / no known pricing'", group.provider, m)
					return
				}
				// Sanity: at minimum Input + Output must be set.
				if p.Input <= 0 || p.Output <= 0 {
					t.Errorf("%s/%s: pricing resolved but Input=%v Output=%v — fix the entry", group.provider, m, p.Input, p.Output)
				}
			})
		}
	}
}

// Claude Haiku 5.5 has two rate cards chosen by prompt length: $0.10/$0.50
// up to a 100K-token prompt, $0.50/$2.50 beyond, applied to the WHOLE call.
// The tier is a property of each call, so a summed usage prices right only
// when every call was tagged where it was made (TagLongContext).
func TestHaikuFiveFive_TwoRateCards(t *testing.T) {
	const model = "claude-haiku-5-5"
	p, ok := PricingFor("anthropic", model)
	if !ok || p.Long == nil || p.LongAbove != 100_000 {
		t.Fatalf("haiku 5.5 must carry its long-context card above 100K, got %+v", p)
	}

	// A Kobi round with the ~27K static prefix cached: short card.
	small := TagLongContext(Usage{InputTokens: 2_000, CacheReadTokens: 27_000, OutputTokens: 800}, "anthropic", model)
	if small.LongInputTokens+small.LongOutputTokens+small.LongCacheReadTokens+small.LongCacheCreationTokens != 0 {
		t.Fatalf("a 29K-token prompt must stay on the short card, got %+v", small)
	}
	// "100K tokens or fewer" is the short card: the edge itself stays short.
	if edge := TagLongContext(Usage{InputTokens: 100_000}, "anthropic", model); edge.LongInputTokens != 0 {
		t.Fatalf("a prompt of exactly 100K must stay on the short card, got %+v", edge)
	}
	// A late round of a long conversation: the whole call goes long, output included.
	big := TagLongContext(Usage{InputTokens: 4_000, CacheReadTokens: 120_000, OutputTokens: 1_000}, "anthropic", model)
	if big.LongInputTokens != 4_000 || big.LongCacheReadTokens != 120_000 || big.LongOutputTokens != 1_000 {
		t.Fatalf("a 124K-token prompt must bill whole at the long card, got %+v", big)
	}

	var session Usage
	session.Add(small)
	session.Add(big)
	want := (2_000*0.10 + 27_000*0.01 + 800*0.50 + // short call
		4_000*0.50 + 120_000*0.05 + 1_000*2.50) / 1_000_000 // long call
	if got := EstimateUSD(session, p); math.Abs(got-want) > 1e-12 {
		t.Errorf("EstimateUSD(session) = %.9f, want %.9f (each call at its own card)", got, want)
	}

	// Long fields survive the JSON round trip the session store does.
	b, _ := json.Marshal(session)
	var back Usage
	if err := json.Unmarshal(b, &back); err != nil || back != session {
		t.Fatalf("usage must round-trip through JSON with its long part, got %+v (%v)", back, err)
	}
}

// Single-rate models ignore the tagging, and usage recorded before the long
// fields existed prices exactly as it always did.
func TestTagLongContext_SingleRateModelsUnchanged(t *testing.T) {
	u := Usage{InputTokens: 300_000, OutputTokens: 2_000, CacheReadTokens: 400_000}
	if got := TagLongContext(u, "anthropic", "claude-sonnet-5"); got != u {
		t.Fatalf("a single-rate model must not be tagged, got %+v", got)
	}
	p, _ := PricingFor("anthropic", "claude-sonnet-5")
	want := (300_000*p.Input + 2_000*p.Output + 400_000*p.CachedInput) / 1_000_000
	if got := EstimateUSD(u, p); math.Abs(got-want) > 1e-12 {
		t.Errorf("EstimateUSD = %.9f, want %.9f", got, want)
	}
}

// Kobi writes its static prefix with a one-hour TTL, which Anthropic bills at
// 2x base input; a 5-minute write is 1.25x. The adapter reports the 1h part
// (CacheCreation1hTokens) and the estimate bills each part at its own rate —
// including on Haiku 5.5's long-context card.
func TestEstimateUSD_OneHourCacheWrites(t *testing.T) {
	sonnet, _ := PricingFor("anthropic", "claude-sonnet-5")
	u := Usage{InputTokens: 1_000, OutputTokens: 500, CacheCreationTokens: 30_000, CacheCreation1hTokens: 27_000}
	want := (1_000*2 + 500*10 + 3_000*2.50 + 27_000*4.00) / 1_000_000.0
	if got := EstimateUSD(u, sonnet); math.Abs(got-want) > 1e-12 {
		t.Errorf("sonnet-5: EstimateUSD = %.6f, want %.6f (1h writes at 2x input)", got, want)
	}

	// A record without the split (written before it existed) prices as before.
	old := Usage{InputTokens: 1_000, OutputTokens: 500, CacheCreationTokens: 30_000}
	if got, want := EstimateUSD(old, sonnet), (1_000*2+500*10+30_000*2.50)/1_000_000.0; math.Abs(got-want) > 1e-12 {
		t.Errorf("record without the split: EstimateUSD = %.6f, want %.6f", got, want)
	}

	// Haiku 5.5, a call over 100K tokens: its 1h writes bill at 2x the LONG input.
	haiku, _ := PricingFor("anthropic", "claude-haiku-5-5")
	big := TagLongContext(Usage{InputTokens: 2_000, CacheReadTokens: 90_000, CacheCreationTokens: 30_000, CacheCreation1hTokens: 27_000, OutputTokens: 1_000}, "anthropic", "claude-haiku-5-5")
	wantBig := (2_000*0.50 + 90_000*0.05 + 3_000*0.625 + 27_000*1.00 + 1_000*2.50) / 1_000_000.0
	if got := EstimateUSD(big, haiku); math.Abs(got-wantBig) > 1e-12 {
		t.Errorf("haiku-5-5 long call: EstimateUSD = %.6f, want %.6f", got, wantBig)
	}
}

// The 5.5 / 5.1 revisions read cache cheaper than their parent (Sonnet 5.5
// and Fable 5.1 keep the parent's input and output; Opus 5.5 lowers those
// too) — and each id prefix-matches its parent ("claude-sonnet-5-5" starts
// with "claude-sonnet-5"). Without its own entry a model silently bills its
// largest bucket in an agentic loop at the parent's rate.
// Prices from the official pricing page, 2026-10-08.
func TestPricingFor_CacheReadsOfRevisionsDoNotCollapse(t *testing.T) {
	cases := []struct {
		model    string
		cacheRd  float64
		inherits string
	}{
		{"claude-sonnet-5-5", 0.10, "claude-sonnet-5"},
		{"claude-sonnet-5", 0.20, ""},
		{"claude-opus-5-5", 0.20, ""},
		{"claude-opus-5", 0.50, ""},
		{"claude-fable-5-1", 0.25, "claude-fable-5"},
		{"claude-fable-5", 1.00, ""},
	}
	for _, c := range cases {
		p, ok := PricingFor("anthropic", c.model)
		if !ok {
			t.Fatalf("%s must be priced", c.model)
		}
		if p.CachedInput != c.cacheRd {
			t.Errorf("%s cache read = $%.2f, want $%.2f", c.model, p.CachedInput, c.cacheRd)
		}
		if c.inherits != "" {
			parent, _ := PricingFor("anthropic", c.inherits)
			if p.Input != parent.Input || p.Output != parent.Output {
				t.Errorf("%s input/output should match %s", c.model, c.inherits)
			}
		}
	}
}
