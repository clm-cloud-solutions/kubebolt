package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Field report 2026-09-21: every tool-carrying turn on gpt-5.6-terra returned
//
//	HTTP 400: Function tools with reasoning_effort are not supported for
//	gpt-5.6-terra in /v1/chat/completions.
//
// KubeBolt never sent the parameter; medium is OpenAI's server-side default
// for the 5.6 line. It was configured as a FALLBACK, so the 400 only ever
// landed while the primary was already failing.

func TestToolsForceReasoningOff_CoversThe56LineAndNothingElse(t *testing.T) {
	for _, m := range []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna", "GPT-5.6-Terra"} {
		if !toolsForceReasoningOff(m) {
			t.Errorf("%s 400s on tools and was not covered", m)
		}
	}
	// Everything else keeps the server's default. A blanket rule would turn
	// reasoning off on models that never had the problem.
	for _, m := range []string{
		"gpt-5.4-mini", "gpt-5.4", "gpt-5.5", "gpt-4o", "gpt-5", "o3-mini", "claude-sonnet-5",
	} {
		if toolsForceReasoningOff(m) {
			t.Errorf("%s does not need the workaround and would lose reasoning", m)
		}
	}
}

// Exercises the REAL path: a stub endpoint captures what Chat actually sends.
// Reconstructing the condition inside the test would only prove the test can
// copy the production code, which is the bug this class of test invites.
func captureOpenAIBody(t *testing.T, model string, tools []ToolDefinition) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)

	p := GetProvider("openai")
	if p == nil {
		t.Fatal("openai provider is not registered")
	}
	if _, err := p.Chat(context.Background(), ChatRequest{
		System:    "sys",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		Tools:     tools,
		Provider:  config.ProviderConfig{Provider: "openai", Model: model, APIKey: "k", BaseURL: srv.URL},
		MaxTokens: 256,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return got
}

func TestOpenAIChat_SendsReasoningOffOnlyWhereItIsNeeded(t *testing.T) {
	// With tools on 5.6: present, or the call 400s.
	if got := captureOpenAIBody(t, "gpt-5.6-terra", ToolDefinitions()); got["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want \"none\" — without it this is a hard 400", got["reasoning_effort"])
	}
	// Without tools — title generation, compaction — the default stands and
	// the model keeps its reasoning.
	if got := captureOpenAIBody(t, "gpt-5.6-terra", nil); got["reasoning_effort"] != nil {
		t.Errorf("a tool-free turn had reasoning disabled for no reason: %v", got["reasoning_effort"])
	}
	// A model that never had the problem must not be touched.
	if got := captureOpenAIBody(t, "gpt-5.4-mini", ToolDefinitions()); got["reasoning_effort"] != nil {
		t.Errorf("gpt-5.4-mini lost its reasoning to a workaround it does not need: %v", got["reasoning_effort"])
	}
}

// omitempty is doing real work here: an empty string would serialise as
// "reasoning_effort":"" and OpenAI rejects it as an invalid enum value — a
// different 400 in place of the one being fixed.
func TestOpenAIRequest_EmptyEffortIsOmittedNotSentBlank(t *testing.T) {
	b, _ := json.Marshal(openaiRequest{Model: "gpt-4o", MaxTokens: 100})
	if strings.Contains(string(b), "reasoning_effort") {
		t.Errorf("serialised an empty reasoning_effort: %s", b)
	}
}
