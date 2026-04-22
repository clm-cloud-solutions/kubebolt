package copilot

import "testing"

func TestContextWindowFor(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     int
	}{
		// Anthropic 1M models
		{"anthropic", "claude-opus-4-7", 1_000_000},
		{"anthropic", "claude-opus-4-7-20260101", 1_000_000},
		{"anthropic", "claude-opus-4-6", 1_000_000},
		{"anthropic", "claude-sonnet-4-6", 1_000_000},
		{"Anthropic", "CLAUDE-SONNET-4-6", 1_000_000}, // case-insensitive
		{"anthropic", "claude-sonnet-4-5-1m", 1_000_000},
		// Anthropic 200K default
		{"anthropic", "claude-sonnet-4-5", 200_000},
		{"anthropic", "claude-haiku-4-5", 200_000},
		{"anthropic", "", 200_000},
		// OpenAI
		{"openai", "gpt-5", 400_000},
		{"openai", "gpt-5-mini", 400_000},
		{"openai", "o5-preview", 400_000},
		{"openai", "gpt-4o", 128_000},
		{"openai", "gpt-4o-mini", 128_000},
		{"openai", "gpt-4.1", 128_000},
		{"openai", "anything-else", 128_000},
		// Unknown provider
		{"custom", "whatever", 128_000},
		{"", "", 128_000},
	}
	for _, c := range cases {
		got := ContextWindowFor(c.provider, c.model)
		if got != c.want {
			t.Errorf("ContextWindowFor(%q, %q) = %d, want %d",
				c.provider, c.model, got, c.want)
		}
	}
}

func TestCheapModelFor(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"anthropic", "claude-haiku-4-5"},
		{"Anthropic", "claude-haiku-4-5"},
		{"openai", "gpt-4o-mini"},
		{"OpenAI", "gpt-4o-mini"},
		{"custom", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := CheapModelFor(c.provider); got != c.want {
			t.Errorf("CheapModelFor(%q) = %q, want %q", c.provider, got, c.want)
		}
	}
}
