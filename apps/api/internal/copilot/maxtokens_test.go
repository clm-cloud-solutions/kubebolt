package copilot

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Kobi on Haiku 5.5, which reasons by default, stopped mid-sentence with the
// old 4096 cap: on Anthropic the reasoning counts against max_tokens.
func TestMaxTokensFor(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		p          config.ProviderConfig
		want       int
	}{
		{"an explicit value wins", 8000, config.ProviderConfig{Provider: "anthropic", Model: "claude-haiku-5-5"}, 8000},
		{"Claude reasons within the cap", 0, config.ProviderConfig{Provider: "anthropic", Model: "claude-haiku-5-5"}, 16384},
		{"Claude with the provider's default model", 0, config.ProviderConfig{Provider: "anthropic"}, 16384},
		{"Claude 3 answers at most 4096", 0, config.ProviderConfig{Provider: "anthropic", Model: "claude-3-haiku-20240307"}, 4096},
		{"OpenAI reasoning model", 0, config.ProviderConfig{Provider: "openai", Model: "o3"}, 16384},
		{"GPT-5 line", 0, config.ProviderConfig{Provider: "openai", Model: "gpt-5.6-terra"}, 16384},
		{"OpenAI chat model", 0, config.ProviderConfig{Provider: "openai", Model: "gpt-4o-mini"}, 4096},
		{"OpenAI with its default model (gpt-4o)", 0, config.ProviderConfig{Provider: "openai"}, 4096},
		// DeepSeek Chat refuses a max_tokens above 8192.
		{"an OpenAI-compatible endpoint", 0, config.ProviderConfig{Provider: "openai", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/v1"}, 4096},
	}
	for _, c := range cases {
		if got := MaxTokensFor(c.configured, c.p); got != c.want {
			t.Errorf("%s: MaxTokensFor = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCutByTokenLimit(t *testing.T) {
	for _, r := range []string{"max_tokens", "length"} {
		if !CutByTokenLimit(r) {
			t.Errorf("%q is a cut", r)
		}
	}
	for _, r := range []string{"end_turn", "stop", "tool_use", "tool_calls", ""} {
		if CutByTokenLimit(r) {
			t.Errorf("%q is not a cut", r)
		}
	}
}
