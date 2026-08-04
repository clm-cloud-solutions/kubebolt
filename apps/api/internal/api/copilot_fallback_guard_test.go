package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// fallbackUsable gates every fallback path. A fallback whose provider name
// doesn't resolve to a registered adapter can never serve a request, and
// retrying on it replaces the primary's error with a config complaint — which
// is how an upstream rate limit surfaced to operators as `unknown provider: `.
func TestFallbackUsable(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.CopilotConfig
		want bool
	}{
		{
			name: "no fallback configured",
			cfg:  config.CopilotConfig{Primary: config.ProviderConfig{Provider: "anthropic"}},
			want: false,
		},
		{
			name: "fallback with empty provider (the stored-override bug)",
			cfg: config.CopilotConfig{
				Primary:  config.ProviderConfig{Provider: "anthropic"},
				Fallback: &config.ProviderConfig{Provider: "", Model: "gpt-4o-mini", APIKey: "k"},
			},
			want: false,
		},
		{
			name: "fallback with an unregistered provider name",
			cfg: config.CopilotConfig{
				Primary:  config.ProviderConfig{Provider: "anthropic"},
				Fallback: &config.ProviderConfig{Provider: "not-a-provider", APIKey: "k"},
			},
			want: false,
		},
		{
			name: "properly configured fallback",
			cfg: config.CopilotConfig{
				Primary:  config.ProviderConfig{Provider: "anthropic"},
				Fallback: &config.ProviderConfig{Provider: "openai", Model: "gpt-4o-mini", APIKey: "k"},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fallbackUsable(&tc.cfg); got != tc.want {
				t.Errorf("fallbackUsable = %v, want %v", got, tc.want)
			}
		})
	}
}

// An empty provider name must produce an actionable message, not the bare
// `unknown provider: ` that told the operator nothing about which slot was unset.
func TestCallProvider_EmptyProviderMessage(t *testing.T) {
	h := &handlers{}
	r := httptest.NewRequest("POST", "/api/v1/copilot/chat", nil)

	_, err := h.callProvider(r, copilot.ChatRequest{Provider: config.ProviderConfig{Provider: ""}})
	if err == nil {
		t.Fatal("expected an error for an empty provider name")
	}
	if strings.TrimSpace(err.Error()) == "unknown provider:" {
		t.Fatalf("error is still the bare confusing form: %q", err.Error())
	}
	for _, want := range []string{"no AI provider configured", "fallback provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}

	// A non-empty but unregistered name keeps naming the offender.
	_, err = h.callProvider(r, copilot.ChatRequest{Provider: config.ProviderConfig{Provider: "gemini"}})
	if err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error = %v, want it to name the unknown provider", err)
	}
}
