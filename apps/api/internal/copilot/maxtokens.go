package copilot

import (
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// The response cap when the operator set none (KUBEBOLT_AI_MAX_TOKENS, or
// Max tokens in the Kobi settings). Where a model's reasoning is billed
// against the cap — Anthropic's max_tokens, OpenAI's max_completion_tokens —
// 4096 cut long answers: Kobi on Haiku 5.5, which reasons by default, stopped
// mid-sentence twice in one evening, the table and the options lost
// (three-tier-shop lab, 2026-10-10). Elsewhere the cap stays at 4096, because
// some OpenAI-compatible endpoints refuse more (DeepSeek Chat stops at 8192).
const (
	reasoningMaxTokens = 16384
	plainMaxTokens     = 4096
)

// MaxTokensFor is the cap for one call: the configured value when set, else
// the default for the provider and model the call goes to.
func MaxTokensFor(configured int, p config.ProviderConfig) int {
	if configured > 0 {
		return configured
	}
	if reasonsWithinCap(p) {
		return reasoningMaxTokens
	}
	return plainMaxTokens
}

// reasonsWithinCap reports whether the model's reasoning counts against the
// response cap.
func reasonsWithinCap(p config.ProviderConfig) bool {
	model := strings.ToLower(p.Model)
	if strings.EqualFold(p.Provider, "anthropic") {
		// Claude 3 models answer at most 4096 tokens.
		return !strings.HasPrefix(model, "claude-3")
	}
	if model == "" {
		model = openaiDefaultModel
	}
	return needsMaxCompletionTokens(model)
}

// CutByTokenLimit reports whether a response stopped at the cap rather than
// where the model meant to end it: Anthropic's max_tokens, OpenAI's length.
func CutByTokenLimit(stopReason string) bool {
	return stopReason == "max_tokens" || stopReason == "length"
}

// TokenLimitNote is added to an answer that stopped at the cap, so the cut is
// visible instead of an answer that simply ends mid-sentence.
// Where the setting lives depends on the edition (the platform's AI settings,
// or Administration), so the note names the setting, not a page.
const TokenLimitNote = "\n\n_This answer stopped at the response token limit before it was complete. Raise Max tokens in Kobi's AI settings, or ask Kobi to continue._"
