package copilot

import "strings"

// ModelPricing holds USD-per-1M-tokens for a model. Fields track what the
// provider actually bills: fresh input, cached-read input (discounted),
// cache-creation input (premium), and output. Zero values are treated as
// "same as input" so new models work without entries.
type ModelPricing struct {
	Input           float64 // $/1M tokens, fresh
	CachedInput     float64 // $/1M tokens, cache-read
	CacheCreation   float64 // $/1M tokens, cache-write
	Output          float64 // $/1M tokens
}

// modelPricing is a best-effort snapshot of public list prices. It exists
// only to render *estimated* costs in the admin UI — the user's real bill
// is whatever their provider charges on their BYOK account. Easy to patch
// as prices move.
var modelPricing = map[string]ModelPricing{
	// Anthropic — https://www.anthropic.com/pricing#api
	"claude-opus-4-7":    {Input: 15, CachedInput: 1.50, CacheCreation: 18.75, Output: 75},
	"claude-opus-4-6":    {Input: 15, CachedInput: 1.50, CacheCreation: 18.75, Output: 75},
	"claude-sonnet-4-6":  {Input: 3, CachedInput: 0.30, CacheCreation: 3.75, Output: 15},
	"claude-sonnet-4-5":  {Input: 3, CachedInput: 0.30, CacheCreation: 3.75, Output: 15},
	"claude-haiku-4-5":   {Input: 0.80, CachedInput: 0.08, CacheCreation: 1.00, Output: 4},

	// OpenAI — https://openai.com/api/pricing
	"gpt-5":              {Input: 2.50, CachedInput: 0.25, Output: 10},
	"gpt-5-mini":         {Input: 0.25, CachedInput: 0.025, Output: 2.00},
	"gpt-4o":             {Input: 2.50, CachedInput: 1.25, Output: 10},
	"gpt-4o-mini":        {Input: 0.15, CachedInput: 0.075, Output: 0.60},
}

// PricingFor returns the ModelPricing for a provider/model pair. Matches
// by prefix so versioned/dated variants (e.g. claude-sonnet-4-6-20260101,
// gpt-5-mini-2025-08-07) resolve to their base model. Returns the zero
// value if unknown — callers can check the result.
func PricingFor(provider, model string) (ModelPricing, bool) {
	m := strings.ToLower(model)
	for key, price := range modelPricing {
		if strings.HasPrefix(m, key) || strings.Contains(m, key) {
			return price, true
		}
	}
	return ModelPricing{}, false
}

// EstimateUSD computes an estimated USD cost from a Usage and pricing.
// Returns 0 when pricing is unknown. Cache-creation defaults to
// input price if not set on the pricing struct.
func EstimateUSD(u Usage, p ModelPricing) float64 {
	cacheCreationPrice := p.CacheCreation
	if cacheCreationPrice == 0 {
		cacheCreationPrice = p.Input
	}
	return (float64(u.InputTokens)*p.Input +
		float64(u.CacheReadTokens)*p.CachedInput +
		float64(u.CacheCreationTokens)*cacheCreationPrice +
		float64(u.OutputTokens)*p.Output) / 1_000_000.0
}
