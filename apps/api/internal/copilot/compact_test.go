package copilot

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// fakeProvider implements Provider by returning a fixed text response.
// Registered under a unique name per test to avoid cross-test interference
// via the shared providerRegistry.
type fakeProvider struct {
	name     string
	reply    string
	calls    atomic.Int32
	lastReq  atomic.Pointer[ChatRequest]
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	p.calls.Add(1)
	p.lastReq.Store(&req)
	return &ChatResponse{
		Text:       p.reply,
		StopReason: "end_turn",
		Usage:      Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func registerFake(t *testing.T, name, reply string) *fakeProvider {
	t.Helper()
	fp := &fakeProvider{name: name, reply: reply}
	RegisterProvider(fp)
	return fp
}

// userTurn builds a minimal user-text message (the "turn start" definition).
func userTurn(content string) Message {
	return Message{Role: RoleUser, Content: content}
}

// assistantTextWithTool builds an assistant message with tool_calls and a
// following user message holding the tool_result.
func assistantTextWithTool(callID, toolName, resultContent string) []Message {
	return []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: callID, Name: toolName, Input: []byte(`{}`)},
		}},
		{Role: RoleUser, ToolResults: []ToolResult{
			{ToolCallID: callID, Content: resultContent},
		}},
	}
}

func TestCompact_NotEnoughTurns(t *testing.T) {
	fp := registerFake(t, "fake-notenough", "summary text")
	msgs := []Message{userTurn("hello")}
	res, err := Compact(context.Background(), msgs, CompactOptions{
		PreserveTurns: 3,
		Provider:      config.ProviderConfig{Provider: fp.name},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.TurnsFolded != 0 {
		t.Errorf("should not fold when turns ≤ preserve; got TurnsFolded=%d", res.TurnsFolded)
	}
	if fp.calls.Load() != 0 {
		t.Error("fake provider should not be called when nothing to compact")
	}
}

func TestCompact_FoldsOlderTurns(t *testing.T) {
	fp := registerFake(t, "fake-folds", "cluster X investigated; pending: check logs")

	// 4 user turns with tool activity between them.
	var msgs []Message
	for i := 1; i <= 4; i++ {
		msgs = append(msgs, userTurn(fmt.Sprintf("question %d", i)))
		msgs = append(msgs, assistantTextWithTool(
			fmt.Sprintf("c%d", i),
			"get_pods",
			strings.Repeat("pod-data ", 20),
		)...)
		msgs = append(msgs, Message{Role: RoleAssistant, Content: fmt.Sprintf("answer %d", i)})
	}

	res, err := Compact(context.Background(), msgs, CompactOptions{
		PreserveTurns: 2,
		Provider:      config.ProviderConfig{Provider: fp.name},
	})
	if err != nil {
		t.Fatalf("Compact err: %v", err)
	}
	if res.TurnsFolded != 2 {
		t.Errorf("want 2 turns folded (4 total - 2 preserved), got %d", res.TurnsFolded)
	}
	if !strings.Contains(res.Summary, "cluster X investigated") {
		t.Errorf("summary missing expected text: %q", res.Summary)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Errorf("tokens after (%d) should be less than before (%d)", res.TokensAfter, res.TokensBefore)
	}
	// First message of NewMessages must be the summary.
	if res.NewMessages[0].Role != RoleAssistant ||
		!strings.Contains(res.NewMessages[0].Content, "Compacted") {
		t.Errorf("first message should be the compaction summary, got %+v", res.NewMessages[0])
	}
}

func TestCompact_ResetAll(t *testing.T) {
	fp := registerFake(t, "fake-resetall", "whole conversation summary")
	msgs := []Message{
		userTurn("q1"),
		{Role: RoleAssistant, Content: "a1"},
		userTurn("q2"),
		{Role: RoleAssistant, Content: "a2"},
	}
	res, err := Compact(context.Background(), msgs, CompactOptions{
		PreserveTurns: 99,
		Provider:      config.ProviderConfig{Provider: fp.name},
		ResetAll:      true,
	})
	if err != nil {
		t.Fatalf("Compact err: %v", err)
	}
	if len(res.NewMessages) != 1 {
		t.Errorf("ResetAll should leave a single summary message, got %d", len(res.NewMessages))
	}
	if res.TurnsFolded != 2 {
		t.Errorf("want 2 turns folded, got %d", res.TurnsFolded)
	}
}

func TestCompact_PicksCheapModelWhenEmpty(t *testing.T) {
	// With no explicit CompactModel and anthropic provider, the compact
	// call should use claude-haiku-4-5.
	fp := registerFake(t, "anthropic", "summary")
	defer RegisterProvider(&stubAnthropic{}) // restore after test

	msgs := []Message{
		userTurn("q1"),
		{Role: RoleAssistant, Content: "a1"},
		userTurn("q2"),
		{Role: RoleAssistant, Content: "a2"},
	}
	_, err := Compact(context.Background(), msgs, CompactOptions{
		PreserveTurns: 1,
		Provider:      config.ProviderConfig{Provider: "anthropic", Model: "claude-opus-4-7"},
	})
	if err != nil {
		t.Fatalf("Compact err: %v", err)
	}
	// Inspect the request the fake received
	req := fp.lastReq.Load()
	if req == nil {
		t.Fatal("fake provider never called")
	}
	if req.Provider.Model != "claude-haiku-4-5" {
		t.Errorf("want model=claude-haiku-4-5, got %q", req.Provider.Model)
	}
}

func TestCompact_ProtectsActiveTurnToolResults(t *testing.T) {
	// Regression test: a compact that fires mid-request (after a round
	// produced tool_results the LLM hasn't yet synthesized) MUST NOT stub
	// those tool_results. Stubbing the active turn's data makes the LLM
	// respond with "output was truncated".
	fp := registerFake(t, "fake-activeturn", "summary")

	heavy := strings.Repeat("cluster-data ", 300) // large enough to trigger stub
	msgs := []Message{
		// Old turn 1 — fully resolved
		userTurn("old question 1"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "get_pods", Input: []byte(`{}`)}}},
		{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "t1", Content: heavy}}},
		{Role: RoleAssistant, Content: "old answer 1"},
		// Old turn 2 — fully resolved
		userTurn("old question 2"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t2", Name: "get_events", Input: []byte(`{}`)}}},
		{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "t2", Content: heavy}}},
		{Role: RoleAssistant, Content: "old answer 2"},
		// Active turn — user just asked, LLM called tools, tool_results arrived,
		// and the LLM hasn't synthesized the response yet.
		userTurn("CURRENT question"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "active", Name: "get_cluster_overview", Input: []byte(`{}`)}}},
		{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "active", Content: heavy}}},
	}

	res, err := Compact(context.Background(), msgs, CompactOptions{
		PreserveTurns: 2,
		Provider:      config.ProviderConfig{Provider: fp.name},
	})
	if err != nil {
		t.Fatalf("Compact err: %v", err)
	}

	// The active turn's tool_result MUST survive intact.
	var activeResult *ToolResult
	for _, m := range res.NewMessages {
		for i := range m.ToolResults {
			if m.ToolResults[i].ToolCallID == "active" {
				activeResult = &m.ToolResults[i]
			}
		}
	}
	if activeResult == nil {
		t.Fatal("active turn's tool_result was dropped — LLM would lose data mid-response")
	}
	if strings.Contains(activeResult.Content, "stubbed during compact") {
		t.Errorf("active turn's tool_result was stubbed — LLM would respond 'output truncated'.\nContent: %q", activeResult.Content)
	}
	if activeResult.Content != heavy {
		t.Errorf("active turn's tool_result content was modified; want %d chars, got %d",
			len(heavy), len(activeResult.Content))
	}
}

func TestCompact_StubsNonActiveTailToolResults(t *testing.T) {
	// Complement to the above: tool_results in preserved turns that the
	// LLM has already synthesized (there's an assistant text after them)
	// SHOULD get stubbed to free memory.
	fp := registerFake(t, "fake-tailstub", "summary")
	heavy := strings.Repeat("pod-logs ", 300)

	msgs := []Message{
		// Turn 1 — will be folded
		userTurn("q1"),
		{Role: RoleAssistant, Content: "a1"},
		// Turn 2 — preserved tail, with resolved tool call (heavy result + assistant summary after)
		userTurn("q2"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t2", Name: "get_logs", Input: []byte(`{}`)}}},
		{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "t2", Content: heavy}}},
		{Role: RoleAssistant, Content: "a2 synthesized"},
		// Active turn — user just asked, nothing else yet
		userTurn("ACTIVE q3"),
	}

	res, err := Compact(context.Background(), msgs, CompactOptions{
		PreserveTurns: 2,
		Provider:      config.ProviderConfig{Provider: fp.name},
	})
	if err != nil {
		t.Fatalf("Compact err: %v", err)
	}
	if res.ToolResultsStubbed < 1 {
		t.Errorf("want >=1 tool_result stubbed in the resolved preserved turn, got %d", res.ToolResultsStubbed)
	}
}

// stubAnthropic restores a placeholder registration under the "anthropic"
// name after TestCompact_PicksCheapModelWhenEmpty replaced it, so other
// tests that rely on the real registration (none currently, but defensive)
// aren't broken by test interleaving.
type stubAnthropic struct{}

func (s *stubAnthropic) Name() string { return "anthropic" }
func (s *stubAnthropic) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return &ChatResponse{Text: "noop", StopReason: "end_turn"}, nil
}
