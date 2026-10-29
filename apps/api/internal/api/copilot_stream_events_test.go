package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// streamToolCall and streamToolResult are the two events the chat draws a
// tool card from while the turn runs.
type streamToolCall struct {
	ID       string          `json:"id"`
	ToolName string          `json:"toolName"`
	Input    json.RawMessage `json:"input"`
	Round    int             `json:"round"`
}

type streamToolResult struct {
	ID         string `json:"id"`
	Round      int    `json:"round"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	Content    string `json:"content"`
	IsError    bool   `json:"isError"`
	Digest     string `json:"digest"`
}

func decodeEvent(t *testing.T, ev sseEvent, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(ev.data), v); err != nil {
		t.Fatalf("%s event: %v (%s)", ev.name, err, ev.data)
	}
}

// doneMessages returns the conversation the turn's done event carries.
func doneMessages(t *testing.T, events []sseEvent) []copilot.Message {
	t.Helper()
	var done struct {
		Messages []copilot.Message `json:"messages"`
	}
	decodeEvent(t, events[len(events)-1], &done)
	return done.Messages
}

// The chat builds the turn live in the shape done returns: each round's text
// in its own message, each card completed by its tool_result with the same
// content, status and duration the stored conversation keeps.
func TestKobiChat_StreamCarriesWhatTheCardsNeed(t *testing.T) {
	prov := newScripted(func(req copilot.ChatRequest) *copilot.ChatResponse {
		if len(lastToolResults(req)) == 0 {
			return &copilot.ChatResponse{
				Text: "Checking the database.",
				ToolCalls: []copilot.ToolCall{
					mcpCall("c1"),
					{ID: "c2", Name: "get_external_connections", Input: json.RawMessage(`{"namespace":"shop"}`)},
				},
				StopReason: "tool_use",
			}
		}
		return &copilot.ChatResponse{Text: "orders-db ran out of storage.", StopReason: "end_turn"}
	})
	w := newWiring(t, true, prov.name)
	w.enable(t)

	events := w.chat(t, "why is checkout failing?")

	calls := map[string]streamToolCall{}
	results := map[string]streamToolResult{}
	var texts []struct {
		Text  string `json:"text"`
		Round int    `json:"round"`
	}
	for i, ev := range events {
		switch ev.name {
		case "tool_call":
			var c streamToolCall
			decodeEvent(t, ev, &c)
			calls[c.ID] = c
		case "tool_result":
			var r streamToolResult
			decodeEvent(t, ev, &r)
			if _, ok := calls[r.ID]; !ok {
				t.Errorf("event %d: tool_result %q before its tool_call", i, r.ID)
			}
			results[r.ID] = r
		case "text":
			var tx struct {
				Text  string `json:"text"`
				Round int    `json:"round"`
			}
			decodeEvent(t, ev, &tx)
			texts = append(texts, tx)
		}
	}

	c1 := calls["c1"]
	if c1.ToolName != "mcp__fake__call" || c1.Round != 0 || !strings.Contains(string(c1.Input), "DescribeDBInstances") {
		t.Errorf("tool_call c1 = %+v (input %s): the card names the operation from the input while it runs", c1, c1.Input)
	}
	if calls["c2"].ToolName != "get_external_connections" {
		t.Errorf("tool_call c2 = %+v", calls["c2"])
	}

	// The connector's digest comes from its binding, a built-in tool's from
	// copilot/digest.go (this server has no flow data).
	if results["c1"].Digest != "1 DB instance" || results["c2"].Digest != "no flow data" {
		t.Errorf("digests = %q / %q, want the binding's and the built-in one", results["c1"].Digest, results["c2"].Digest)
	}

	if len(texts) != 2 || texts[0].Round != 0 || texts[0].Text != "Checking the database." || texts[1].Round != 1 {
		t.Errorf("text events = %+v, want one per round with its round", texts)
	}

	// The stored conversation keeps what the live cards showed.
	stored := map[string]copilot.ToolResult{}
	for _, m := range doneMessages(t, events) {
		for _, tr := range m.ToolResults {
			stored[tr.ToolCallID] = tr
		}
	}
	for _, id := range []string{"c1", "c2"} {
		live, ok := results[id]
		if !ok {
			t.Errorf("no tool_result for %s", id)
			continue
		}
		if live.Round != 0 || live.Status != copilot.ToolResultOK || live.IsError {
			t.Errorf("tool_result %s = %+v", id, live)
		}
		st := stored[id]
		if st.Content != live.Content || st.Status != live.Status || st.DurationMs != live.DurationMs || st.IsError != live.IsError || st.Digest != live.Digest {
			t.Errorf("%s: done keeps {status %q, %d ms, %d bytes}, the live card showed {status %q, %d ms, %d bytes}",
				id, st.Status, st.DurationMs, len(st.Content), live.Status, live.DurationMs, len(live.Content))
		}
	}
}

// A connector call past the per-turn budget is never made: its card says not
// sent, not failed, though the model reads it as an error.
func TestKobiChat_CallPastTheBudgetReadsNotSent(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall {
		out := make([]copilot.ToolCall, copilot.MaxMCPCallsPerTurn+1)
		for i := range out {
			out[i] = mcpCall(fmt.Sprintf("c%d", i+1))
		}
		return out
	}))
	w := newWiring(t, true, prov.name)
	w.enable(t)

	events := w.chat(t, "list everything in the account")
	over := fmt.Sprintf("c%d", copilot.MaxMCPCallsPerTurn+1)
	var seen int
	for _, ev := range events {
		if ev.name != "tool_result" {
			continue
		}
		var r streamToolResult
		decodeEvent(t, ev, &r)
		seen++
		want := copilot.ToolResultOK
		if r.ID == over {
			want = copilot.ToolResultNotSent
		}
		if r.Status != want {
			t.Errorf("tool_result %s status = %q, want %q", r.ID, r.Status, want)
		}
	}
	if seen != copilot.MaxMCPCallsPerTurn+1 {
		t.Errorf("tool_result events = %d, want %d", seen, copilot.MaxMCPCallsPerTurn+1)
	}
}

// escalatedAt returns the index of the first meta event naming the
// escalation model, or -1.
func escalatedAt(t *testing.T, events []sseEvent) int {
	t.Helper()
	for i, ev := range events {
		if ev.name != "meta" {
			continue
		}
		var m struct {
			Escalated string `json:"escalated"`
		}
		decodeEvent(t, ev, &m)
		if m.Escalated != "" {
			return i
		}
	}
	return -1
}

// The header names the model each turn runs on: the turn that first leaves
// the cluster says so when it does, and every later turn of that
// conversation says so before its first round.
func TestKobiChat_EscalationIsAnnouncedOnEveryTurnItCovers(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall { return []copilot.ToolCall{mcpCall("c1")} }))
	wiringEscalation = &config.ProviderConfig{Provider: prov.name, Model: "escalation-model"}
	defer func() { wiringEscalation = nil }()
	w := newWiring(t, true, prov.name)
	w.enable(t)

	first := w.chat(t, "why is checkout failing?")
	at := escalatedAt(t, first)
	callAt := -1
	for i, ev := range first {
		if ev.name == "tool_call" {
			callAt = i
			break
		}
	}
	if at < 0 || at < callAt {
		t.Fatalf("first turn: escalation at event %d, the connector call at %d: it escalates when it leaves the cluster", at, callAt)
	}

	// The next question carries the transcript, which already left the
	// cluster: the turn starts escalated and says so before anything else.
	history := doneMessages(t, first)
	history = append(history, copilot.Message{Role: copilot.RoleUser, Content: "and now?"})
	second := w.chatMessagesAs(t, w.admin, history)
	at = escalatedAt(t, second)
	if at < 0 {
		t.Fatal("second turn: no escalation announced")
	}
	for _, ev := range second[:at] {
		if ev.name == "text" || ev.name == "tool_call" || ev.name == "usage" {
			t.Fatalf("second turn: %s came before the escalation was announced", ev.name)
		}
	}
}

// A turn that escalates keeps the model it moved from, so each model's rounds
// are priced at its own price: the first round was the configured model's,
// the answer the escalation model's.
func TestKobiChat_EscalatedSessionRecordsTheModelItMovedFrom(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall { return []copilot.ToolCall{mcpCall("c1")} }))
	wiringEscalation = &config.ProviderConfig{Provider: prov.name, Model: "escalation-model"}
	defer func() { wiringEscalation = nil }()
	w := newWiring(t, true, prov.name)
	w.enable(t)

	w.chat(t, "why is checkout failing?")
	sess := w.sessions.last(t)
	esc := sess.Escalation
	if esc == nil {
		t.Fatal("the session that called the connector did not escalate")
	}
	if esc.FromProvider != prov.name || esc.FromModel != "fake-model" {
		t.Errorf("moved from %s/%s, want %s/fake-model", esc.FromProvider, esc.FromModel, prov.name)
	}
	round := copilot.Usage{InputTokens: 10, OutputTokens: 5}
	want := []copilot.ModelShare{
		{Provider: prov.name, Model: "fake-model", Usage: round},
		{Provider: prov.name, Model: "escalation-model", Usage: round},
	}
	if got := sess.ModelShares(); !reflect.DeepEqual(got, want) {
		t.Errorf("ModelShares() = %+v, want %+v", got, want)
	}
}
