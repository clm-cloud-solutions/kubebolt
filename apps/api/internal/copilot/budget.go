package copilot

import "strings"

// ContextWindowFor returns the input context window size in tokens for a
// given provider+model, falling back to a conservative 128K default for
// unknown models. Values are the provider-documented totals; callers apply
// their own threshold (e.g. 80%) when deciding to compact.
func ContextWindowFor(provider, model string) int {
	m := strings.ToLower(model)
	switch strings.ToLower(provider) {
	case "anthropic":
		// 1M-token models: Opus 4.7, Opus 4.6, Sonnet 4.6. Everything else
		// in the 4.x family (Sonnet 4.5, Haiku 4.5) is 200K.
		if strings.Contains(m, "opus-4-7") ||
			strings.Contains(m, "opus-4-6") ||
			strings.Contains(m, "sonnet-4-6") ||
			strings.Contains(m, "1m") {
			return 1000000
		}
		return 200000
	case "openai":
		// GPT-5 family: 400K. GPT-4o / 4.x: 128K.
		if strings.HasPrefix(m, "gpt-5") || strings.HasPrefix(m, "o5") {
			return 400000
		}
		if strings.Contains(m, "4o") || strings.HasPrefix(m, "gpt-4") {
			return 128000
		}
		return 128000
	}
	return 128000
}

// CheapModelFor returns the cheapest model of a given provider suitable for
// summarization. Callers use this when KUBEBOLT_AI_COMPACT_MODEL is unset.
// The chosen models trade capability for price and latency — compaction
// doesn't need reasoning, just faithful summarization.
func CheapModelFor(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		return "claude-haiku-4-5"
	case "openai":
		return "gpt-4o-mini"
	}
	return ""
}
