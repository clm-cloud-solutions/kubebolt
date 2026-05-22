package copilot

import (
	"sort"
	"strings"
)

// ModelPricing holds USD-per-1M-tokens for a model. Fields track what the
// provider actually bills: fresh input, cached-read input (discounted),
// cache-creation input (premium), and output. Zero values are treated as
// "same as input" so new models work without entries.
type ModelPricing struct {
	Input         float64 // $/1M tokens, fresh
	CachedInput   float64 // $/1M tokens, cache-read
	CacheCreation float64 // $/1M tokens, cache-write
	Output        float64 // $/1M tokens
}

// modelPricing is a best-effort snapshot of public list prices. It exists
// only to render *estimated* costs in the admin UI — the user's real bill
// is whatever their provider charges on their BYOK account. Easy to patch
// as prices move.
//
// Coverage policy: every model in the V1 admin Settings catalog
// (apps/web/src/pages/admin/settings/modelCatalog.ts) should have an
// entry that resolves via PricingFor — either by exact key or by prefix
// match against a shorter parent key. The pricingKeys() sort guarantees
// longer (more specific) entries win, so adding a finer-grained variant
// later doesn't break the broader fallback.
//
// All values are USD per 1,000,000 tokens.
var modelPricing = map[string]ModelPricing{
	// ─── Anthropic — https://www.anthropic.com/pricing#api ──────────
	// CacheCreation uses the 5-minute TTL (default). Anthropic also has
	// a 1-hour TTL that's 1.6× more expensive (e.g. Opus 4.7 5m=$6.25,
	// 1h=$10); the struct only has a single field, so we go with the
	// default cadence that operators actually see most often.
	//
	// IMPORTANT: Opus 4.5+ (4.5, 4.6, 4.7) are ~3× CHEAPER than older
	// Opus 4 / 4.1 — the May 2026 price reshuffle made the new line
	// the lower tier. Haiku 4.5 is more expensive than the now-retired
	// Haiku 3.5 ($1/$5 vs $0.80/$4). Sonnet pricing stayed flat at
	// $3/$15 across the 4.x line.
	"claude-opus-4-7": {Input: 5, CachedInput: 0.50, CacheCreation: 6.25, Output: 25},
	"claude-opus-4-6": {Input: 5, CachedInput: 0.50, CacheCreation: 6.25, Output: 25},
	"claude-opus-4-5": {Input: 5, CachedInput: 0.50, CacheCreation: 6.25, Output: 25},
	// Older Opus 4 / 4.1 keep the legacy higher price (deprecated tier).
	"claude-opus-4-1":   {Input: 15, CachedInput: 1.50, CacheCreation: 18.75, Output: 75},
	"claude-opus-4":     {Input: 15, CachedInput: 1.50, CacheCreation: 18.75, Output: 75},
	"claude-sonnet-4-6": {Input: 3, CachedInput: 0.30, CacheCreation: 3.75, Output: 15},
	"claude-sonnet-4-5": {Input: 3, CachedInput: 0.30, CacheCreation: 3.75, Output: 15},
	"claude-sonnet-4":   {Input: 3, CachedInput: 0.30, CacheCreation: 3.75, Output: 15},
	"claude-haiku-4-5":  {Input: 1, CachedInput: 0.10, CacheCreation: 1.25, Output: 5},

	// ─── OpenAI — https://openai.com/api/pricing ────────────────────
	// CacheCreation = 0 → falls back to Input (OpenAI doesn't charge a
	// premium for cache writes the way Anthropic does).
	// GPT-5.5 (current flagship, May 2026).
	"gpt-5.5": {Input: 5.00, CachedInput: 0.50, Output: 30},
	// GPT-5.4 family. Listed nano/mini ahead of base in source order
	// but order doesn't matter for matching — pricingKeys() sorts by
	// length descending so "gpt-5.4-nano" still wins over "gpt-5.4".
	"gpt-5.4-nano": {Input: 0.20, CachedInput: 0.02, Output: 1.25},
	"gpt-5.4-mini": {Input: 0.75, CachedInput: 0.075, Output: 4.50},
	"gpt-5.4":      {Input: 2.50, CachedInput: 0.25, Output: 15},
	// GPT-5.2 / 5.1 — chat-latest aliases prefix-match these.
	"gpt-5.2": {Input: 1.75, CachedInput: 0.175, Output: 14},
	"gpt-5.1": {Input: 1.25, CachedInput: 0.125, Output: 10},
	// GPT-5 (original 5.0) — keep mini and base; chat-latest matches base.
	"gpt-5-mini": {Input: 0.25, CachedInput: 0.025, Output: 2.00},
	"gpt-5-nano": {Input: 0.05, CachedInput: 0.005, Output: 0.40},
	"gpt-5":      {Input: 1.25, CachedInput: 0.125, Output: 10},
	// GPT-4.1 family.
	"gpt-4.1-nano": {Input: 0.10, CachedInput: 0.025, Output: 0.40},
	"gpt-4.1-mini": {Input: 0.40, CachedInput: 0.10, Output: 1.60},
	"gpt-4.1":      {Input: 2.00, CachedInput: 0.50, Output: 8.00},
	// GPT-4o family.
	"gpt-4o-mini": {Input: 0.15, CachedInput: 0.075, Output: 0.60},
	"gpt-4o":      {Input: 2.50, CachedInput: 1.25, Output: 10},
	// o-series (reasoning).
	"o4-mini": {Input: 1.10, CachedInput: 0.275, Output: 4.40},
	"o3-mini": {Input: 1.10, CachedInput: 0.55, Output: 4.40},
	"o3":      {Input: 2.00, CachedInput: 0.50, Output: 8.00},

	// ─── xAI Grok — https://x.ai/api (text models) ──────────────────
	// 4.20 dated variants (-0309-reasoning, -0309-non-reasoning)
	// prefix-match this entry.
	"grok-4.3":  {Input: 1.25, Output: 2.50},
	"grok-4.20": {Input: 1.25, Output: 2.50},

	// ─── DeepSeek — https://api-docs.deepseek.com/quick_start/pricing ─
	"deepseek-reasoner": {Input: 0.55, CachedInput: 0.14, Output: 2.19},
	"deepseek-chat":     {Input: 0.27, CachedInput: 0.027, Output: 1.10},

	// ─── Alibaba Qwen — DashScope OpenAI-compatible pricing ─────────
	"qwen-max":   {Input: 2.80, Output: 8.40},
	"qwen-plus":  {Input: 0.80, Output: 2.00},
	"qwen-turbo": {Input: 0.30, Output: 0.60},

	// ─── Meta Llama via Groq — https://groq.com/pricing ─────────────
	"llama-3.3-70b": {Input: 0.59, Output: 0.79},
	"llama-3.1-70b": {Input: 0.59, Output: 0.79},
	"llama-3.1-8b":  {Input: 0.05, Output: 0.08},

	// ─── Mistral — https://mistral.ai/pricing (La Plateforme) ───────
	"mistral-large":     {Input: 2.00, Output: 6.00},
	"mistral-medium":    {Input: 0.40, Output: 2.00},
	"mistral-small":     {Input: 0.20, Output: 0.60},
	"open-mistral-nemo": {Input: 0.15, Output: 0.15},

	// ─── MiniMax (kept for legacy installs; not in V1 catalog) ──────
	"minimax-m2.7-highspeed": {Input: 0.60, CachedInput: 0.06, CacheCreation: 0.375, Output: 2.40},
	"minimax-m2.7":           {Input: 0.30, CachedInput: 0.06, CacheCreation: 0.375, Output: 1.20},
	"minimax-m2.5-highspeed": {Input: 0.60, CachedInput: 0.03, CacheCreation: 0.375, Output: 2.40},
	"minimax-m2.5":           {Input: 0.30, CachedInput: 0.03, CacheCreation: 0.375, Output: 1.20},
}

// pricingKeys returns map keys sorted by length descending, so longer
// (more specific) keys are checked first. Without this, HasPrefix
// matching against keys like "minimax-m2.7" and "minimax-m2.7-highspeed"
// — or "gpt-5" and "gpt-5-mini" — returns whichever Go's randomized map
// iteration hits first, leading to nondeterministic mispricing.
func pricingKeys() []string {
	keys := make([]string, 0, len(modelPricing))
	for k := range modelPricing {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}

// PricingFor returns the ModelPricing for a provider/model pair. Matches
// by prefix so versioned/dated variants (e.g. claude-sonnet-4-6-20260101,
// gpt-5-mini-2025-08-07) resolve to their base model. Returns the zero
// value if unknown — callers can check the result.
func PricingFor(provider, model string) (ModelPricing, bool) {
	m := strings.ToLower(model)
	if m == "" {
		return ModelPricing{}, false
	}
	for _, key := range pricingKeys() {
		if strings.HasPrefix(m, key) || strings.Contains(m, key) {
			return modelPricing[key], true
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
