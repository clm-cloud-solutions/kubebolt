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

	// LongAbove and Long are a second rate card that applies to a WHOLE call
	// once its prompt (input + cache reads + cache writes) passes LongAbove
	// tokens. Claude Haiku 5.5 bills $0.10/$0.50 up to 100K and $0.50/$2.50
	// beyond; a Kobi conversation crosses that line long before compaction.
	// TagLongContext marks such calls where they are made; EstimateUSD bills
	// the marked part at Long. Zero / nil for single-rate models.
	LongAbove int
	Long      *ModelPricing
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
	// CacheCreation is the 5-minute TTL write (1.25x input). One-hour writes
	// cost 2x input and are billed from Usage.CacheCreation1hTokens at
	// cacheWrite1hMultiplier — Kobi writes its static prefix for an hour, so
	// most of its writes are those.
	//
	// IMPORTANT: Opus 4.5+ (4.5, 4.6, 4.7) are ~3× CHEAPER than older
	// Opus 4 / 4.1 — the May 2026 price reshuffle made the new line
	// the lower tier. Haiku 4.5 is more expensive than the now-retired
	// Haiku 3.5 ($1/$5 vs $0.80/$4). Sonnet pricing stayed flat at
	// $3/$15 across the 4.x line.
	//
	// Claude 5 family (current): Opus 5 and Opus 4.8 share the Opus $5/$25
	// tier; Sonnet 5 is $2/$10 (Anthropic made the launch rate PERMANENT —
	// it no longer reverts to $3/$15 after the 2026-08-31 intro window);
	// Fable 5 is the premium tier at $10/$50.
	// Fable 5.1 keeps Fable 5's rates except cache reads: $0.25 (0.025x), a
	// quarter of Fable 5's $1. Same prefix trap as Sonnet 5.5 and Opus 5.5.
	"claude-fable-5-1": {Input: 10, CachedInput: 0.25, CacheCreation: 12.50, Output: 50},
	"claude-fable-5":   {Input: 10, CachedInput: 1.00, CacheCreation: 12.50, Output: 50},
	// Opus 5.5 (2026-09-22) undercuts Opus 5 on every bucket, and its cache
	// read is 0.05x base input — not the 0.1x that held for every earlier
	// Claude. This entry MUST exist: without it "claude-opus-5-5" prefix-
	// matches "claude-opus-5" and bills at $5/$25. pricingKeys() sorts by
	// length descending, so the longer key wins once it is here.
	"claude-opus-5-5": {Input: 4, CachedInput: 0.20, CacheCreation: 5.00, Output: 20},
	"claude-opus-5":   {Input: 5, CachedInput: 0.50, CacheCreation: 6.25, Output: 25},
	"claude-opus-4-8": {Input: 5, CachedInput: 0.50, CacheCreation: 6.25, Output: 25},
	// Sonnet 5.5 (2026-10-07) keeps Sonnet 5's input/output and write prices
	// but reads cache at 0.05x — $0.10, half of Sonnet 5's $0.20. This entry
	// MUST exist: without it "claude-sonnet-5-5" prefix-matches
	// "claude-sonnet-5" and bills every cache read at twice its price.
	"claude-sonnet-5-5": {Input: 2, CachedInput: 0.10, CacheCreation: 2.50, Output: 10},
	"claude-sonnet-5":   {Input: 2, CachedInput: 0.20, CacheCreation: 2.50, Output: 10},
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
	// Haiku 5.5 (2026-10-07): two rate cards chosen by prompt length — 10x
	// cheaper than Haiku 4.5 up to a 100K-token prompt, and still half its
	// price beyond. Same multipliers on both cards (cache read 0.1x, 5-minute
	// write 1.25x). Without the Long card every large call would bill at a
	// fifth of what Anthropic charges.
	"claude-haiku-5-5": {
		Input: 0.10, CachedInput: 0.01, CacheCreation: 0.125, Output: 0.50,
		LongAbove: 100_000,
		Long:      &ModelPricing{Input: 0.50, CachedInput: 0.05, CacheCreation: 0.625, Output: 2.50},
	},

	// ─── OpenAI — https://openai.com/api/pricing ────────────────────
	// CacheCreation = 0 → falls back to Input for the older lines (OpenAI
	// didn't charge a cache-write premium the way Anthropic does). The
	// GPT-5.6 line DID introduce cache-write pricing (input × 1.25), so
	// those three carry an explicit CacheCreation.
	// GPT-6 (current flagship) — astra/sol/luna. Sol and Luna landed
	// 2026-09-22 at half the GPT-5.6 rates; Astra is the top tier. Note the
	// family is NOT a rename of GPT-5.6's sol/terra/luna: the tiers do not
	// line up (there is no gpt-6-terra, and gpt-6-sol is priced where
	// gpt-5.6-terra sat). Cache read 0.1x input, cache write 1.25x.
	"gpt-6-astra": {Input: 10.00, CachedInput: 1.00, CacheCreation: 12.50, Output: 50},
	"gpt-6-sol":   {Input: 2.00, CachedInput: 0.20, CacheCreation: 2.50, Output: 10},
	"gpt-6-luna":  {Input: 0.10, CachedInput: 0.01, CacheCreation: 0.125, Output: 0.50},
	// GPT-5.6 (previous flagship, Aug 2026) — sol/terra/luna. Standard
	// (short-context, ≤272K input) rates; over 272K OpenAI bills 2× input /
	// 1.5× output, which the single-rate struct can't model (estimate only).
	"gpt-5.6-sol":   {Input: 5.00, CachedInput: 0.50, CacheCreation: 6.25, Output: 30},
	"gpt-5.6-terra": {Input: 2.00, CachedInput: 0.20, CacheCreation: 2.50, Output: 12},
	"gpt-5.6-luna":  {Input: 0.20, CachedInput: 0.02, CacheCreation: 0.25, Output: 1.20},
	// GPT-5.5 (previous flagship, May 2026).
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

// cacheWrite1hMultiplier prices a one-hour cache write: 2x base input on every
// Anthropic model (a 5-minute write is 1.25x — ModelPricing.CacheCreation).
// Only the Anthropic adapter reports 1h writes, so other providers never reach it.
const cacheWrite1hMultiplier = 2.0

// EstimateUSD computes an estimated USD cost from a Usage and pricing.
// Returns 0 when pricing is unknown. Cache-creation defaults to
// input price if not set on the pricing struct. The part of the usage that
// came from long-context calls (Usage.Long*) is billed at p.Long.
func EstimateUSD(u Usage, p ModelPricing) float64 {
	if p.Long == nil {
		return estimateSingleRate(u, p)
	}
	long := Usage{
		InputTokens:           u.LongInputTokens,
		OutputTokens:          u.LongOutputTokens,
		CacheCreationTokens:   u.LongCacheCreationTokens,
		CacheReadTokens:       u.LongCacheReadTokens,
		CacheCreation1hTokens: u.LongCacheCreation1hTokens,
	}
	short := Usage{
		InputTokens:           u.InputTokens - long.InputTokens,
		OutputTokens:          u.OutputTokens - long.OutputTokens,
		CacheCreationTokens:   u.CacheCreationTokens - long.CacheCreationTokens,
		CacheReadTokens:       u.CacheReadTokens - long.CacheReadTokens,
		CacheCreation1hTokens: u.CacheCreation1hTokens - long.CacheCreation1hTokens,
	}
	return estimateSingleRate(short, p) + estimateSingleRate(long, *p.Long)
}

// TagLongContext marks ONE call's usage as billed at the model's long rate
// card when that call's prompt passed the threshold. It must run per call,
// where the prompt size is known: once calls are summed, nothing says which
// of them were large. No-op for models with a single rate card.
func TagLongContext(u Usage, provider, model string) Usage {
	p, ok := PricingFor(provider, model)
	if !ok || p.Long == nil || p.LongAbove <= 0 {
		return u
	}
	if u.InputTokens+u.CacheReadTokens+u.CacheCreationTokens <= p.LongAbove {
		return u
	}
	u.LongInputTokens = u.InputTokens
	u.LongOutputTokens = u.OutputTokens
	u.LongCacheCreationTokens = u.CacheCreationTokens
	u.LongCacheReadTokens = u.CacheReadTokens
	u.LongCacheCreation1hTokens = u.CacheCreation1hTokens
	return u
}

func estimateSingleRate(u Usage, p ModelPricing) float64 {
	cacheCreationPrice := p.CacheCreation
	if cacheCreationPrice == 0 {
		cacheCreationPrice = p.Input
	}
	// The one-hour part of the writes bills at 2x input, the rest at the
	// 5-minute rate. Clamped: the 1h count is part of the total, never more.
	writes1h := min(u.CacheCreation1hTokens, u.CacheCreationTokens)
	writes5m := u.CacheCreationTokens - writes1h
	return (float64(u.InputTokens)*p.Input +
		float64(u.CacheReadTokens)*p.CachedInput +
		float64(writes5m)*cacheCreationPrice +
		float64(writes1h)*p.Input*cacheWrite1hMultiplier +
		float64(u.OutputTokens)*p.Output) / 1_000_000.0
}
