package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Measured on real sessions: 68-89% of a session's cost was cache WRITE, not
// the conversation. A write bills 1.25x input and a read 0.1x, and the default
// 5-minute entry expires between questions asked at a human pace — so every
// question re-bought the same ~27k-token prefix at 12.5x the read price.

func captureAnthropicBody(t *testing.T, msgs []Message) (map[string]any, http.Header) {
	t.Helper()
	var body map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	}))
	t.Cleanup(srv.Close)

	p := GetProvider("anthropic")
	if p == nil {
		t.Fatal("anthropic provider is not registered")
	}
	if _, err := p.Chat(context.Background(), ChatRequest{
		System:    "sys",
		Messages:  msgs,
		Tools:     ToolDefinitions(),
		Provider:  config.ProviderConfig{Provider: "anthropic", Model: "claude-sonnet-5", APIKey: "k", BaseURL: srv.URL},
		MaxTokens: 256,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return body, hdr
}

func ttlOf(t *testing.T, cc any) string {
	t.Helper()
	m, ok := cc.(map[string]any)
	if !ok {
		t.Fatalf("cache_control is %T, want an object", cc)
	}
	if m["type"] != "ephemeral" {
		t.Errorf("type = %v", m["type"])
	}
	if v, ok := m["ttl"]; ok {
		return v.(string)
	}
	return ""
}

// The static prefix — system prompt and tool definitions, ~27k tokens — is the
// part that does not change between questions, so it is the part worth keeping
// for an hour.
func TestAnthropicCache_StaticPrefixAsksForTheHour(t *testing.T) {
	t.Setenv("KUBEBOLT_AI_CACHE_TTL", "")
	body, hdr := captureAnthropicBody(t, []Message{{Role: RoleUser, Content: "hola"}})

	sys, _ := body["system"].([]any)
	if len(sys) == 0 {
		t.Fatal("no system block")
	}
	first, _ := sys[0].(map[string]any)
	if got := ttlOf(t, first["cache_control"]); got != "1h" {
		t.Errorf("system ttl = %q, want 1h — this is the 27k that gets re-bought every question", got)
	}

	tools, _ := body["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	last, _ := tools[len(tools)-1].(map[string]any)
	if got := ttlOf(t, last["cache_control"]); got != "1h" {
		t.Errorf("tools ttl = %q, want 1h", got)
	}

	// The ttl field and the beta header are a pair; sending one without the
	// other is how this 400s.
	if hdr.Get("anthropic-beta") != anthropicExtendedCacheBeta {
		t.Errorf("anthropic-beta = %q, want the extended-cache beta", hdr.Get("anthropic-beta"))
	}
}

// The conversation breakpoint sits on the LAST message, so it moves every
// round and its entry is never read once the conversation ends. Paying 2x to
// keep that for an hour buys nothing.
func TestAnthropicCache_ConversationStaysOnTheDefault(t *testing.T) {
	t.Setenv("KUBEBOLT_AI_CACHE_TTL", "")
	body, _ := captureAnthropicBody(t, []Message{
		{Role: RoleUser, Content: "una pregunta lo bastante larga como para superar el mínimo de caché"},
	})
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := last["content"].([]any)
	block, _ := content[len(content)-1].(map[string]any)
	if got := ttlOf(t, block["cache_control"]); got != "" {
		t.Errorf("conversation ttl = %q, want the 5m default — it is rewritten every round", got)
	}
}

// One isolated question costs ~60% more on the hour, so an install whose usage
// is genuinely one-shot must be able to opt out.
func TestAnthropicCache_OptOutRestoresTheDefaultAndDropsTheBeta(t *testing.T) {
	t.Setenv("KUBEBOLT_AI_CACHE_TTL", "5m")
	body, hdr := captureAnthropicBody(t, []Message{{Role: RoleUser, Content: "hola"}})

	sys, _ := body["system"].([]any)
	first, _ := sys[0].(map[string]any)
	if got := ttlOf(t, first["cache_control"]); got != "" {
		t.Errorf("ttl = %q, want none after opting out", got)
	}
	// The header must go with it: a beta announced for a feature not in use is
	// noise at best.
	if hdr.Get("anthropic-beta") != "" {
		t.Errorf("anthropic-beta = %q, want it absent", hdr.Get("anthropic-beta"))
	}
}

func TestAnthropicStaticCacheTTL_UnknownValueFallsBackToTheAPIDefault(t *testing.T) {
	for _, v := range []string{"5m", "30m", "nonsense", "1 hour"} {
		t.Setenv("KUBEBOLT_AI_CACHE_TTL", v)
		if got := anthropicStaticCacheTTL(); got != "" {
			t.Errorf("%q → %q, want the API default rather than an invalid ttl", v, got)
		}
	}
	for _, v := range []string{"", "1h"} {
		t.Setenv("KUBEBOLT_AI_CACHE_TTL", v)
		if got := anthropicStaticCacheTTL(); got != "1h" {
			t.Errorf("%q → %q, want 1h", v, got)
		}
	}
}
