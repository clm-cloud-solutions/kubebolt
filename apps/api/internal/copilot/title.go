package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Conversation title generation. A title is set in two passes:
//   1. HeuristicTitle on first save — instant, so the history list never
//      shows a blank row.
//   2. GenerateTitle (cheap model) refines it in the background — best-effort,
//      falls back to the heuristic on any error.

// HeuristicTitle derives a short label from the operator's first prompt with
// no LLM call. Used immediately on first persist and as the fallback.
func HeuristicTitle(firstUserMsg string) string {
	s := strings.TrimSpace(collapseWhitespace(firstUserMsg))
	if s == "" {
		return "New conversation"
	}
	// Prefer the first sentence if it's a reasonable length.
	if idx := strings.IndexAny(s, ".?!"); idx > 0 && idx <= conversationTitleMaxLen {
		s = strings.TrimSpace(s[:idx])
	}
	return truncateRunes(s, conversationTitleMaxLen)
}

// SanitizeTitle normalizes an LLM-produced title to the stored shape: single
// line, no surrounding quotes/backticks, no trailing punctuation, capped.
func SanitizeTitle(s string) string {
	s = collapseWhitespace(s)
	s = strings.Trim(s, " \t\n\r\"'`")
	s = strings.TrimRight(s, ".")
	s = strings.TrimSpace(s)
	return truncateRunes(s, conversationTitleMaxLen)
}

// GenerateTitle asks the cheap model of the configured provider for a short
// (3–6 word) title summarizing the opening exchange. Best-effort: returns an
// error the caller treats as "keep the heuristic". Mirrors compact.go's
// cheap-model invocation so it shares the same provider/model fallback.
func GenerateTitle(ctx context.Context, provider config.ProviderConfig, firstUserMsg, assistantReply string) (string, error) {
	p := provider
	if model := CheapModelFor(provider.Provider); model != "" {
		p.Model = model
	}
	prov := GetProvider(p.Provider)
	if prov == nil {
		return "", fmt.Errorf("unknown title provider %q", p.Provider)
	}

	sys := "You write a SHORT title (3-6 words) for a Kubernetes operations chat. " +
		"Capture the specific resource and problem (e.g. \"payments pod OOMKilled\", " +
		"\"ingress 503 after deploy\"). No quotes, no trailing punctuation, no preamble. " +
		"Reply with ONLY the title."
	content := "First user message:\n" + truncateRunes(strings.TrimSpace(firstUserMsg), 600)
	if a := strings.TrimSpace(assistantReply); a != "" {
		content += "\n\nAssistant reply:\n" + truncateRunes(a, 600)
	}

	resp, err := prov.Chat(ctx, ChatRequest{
		System:    sys,
		Messages:  []Message{{Role: RoleUser, Content: content}},
		Provider:  p,
		MaxTokens: 32,
	})
	if err != nil {
		return "", err
	}
	title := SanitizeTitle(resp.Text)
	if title == "" {
		return "", fmt.Errorf("title generation returned empty")
	}
	return title, nil
}
