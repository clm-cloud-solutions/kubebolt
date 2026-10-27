package copilot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func cacheTestNotes(now time.Time, view string) SessionNotesInput {
	return SessionNotesInput{
		ClusterName:        "agent:5f0c1d2e",
		ClusterDisplayName: "shop-back",
		CurrentPath:        view,
		Now:                now,
		ClientTimezone:     "Europe/Madrid",
		Connectors:         []string{"azure"},
		ActionsEnabled:     true,
		DestructiveEnabled: true,
		OperatorLanguage:   "es",
	}
}

// providerView is what a provider adapter sends for each message: the role
// and the content (the tool calls and results are not touched here).
func providerView(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m.Role) + "|" + m.Content
	}
	return out
}

// roundTripLikeTheBrowser sends messages through JSON and keeps a timestamp
// the way the chat panel does (new Date(ts).toISOString(): UTC, milliseconds).
func roundTripLikeTheBrowser(t *testing.T, ms []Message) []Message {
	t.Helper()
	raw, err := json.Marshal(ms)
	if err != nil {
		t.Fatal(err)
	}
	var out []Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for i := range out {
		if !out[i].Timestamp.IsZero() {
			out[i].Timestamp = out[i].Timestamp.UTC().Truncate(time.Millisecond)
		}
	}
	return out
}

// TestPrefixSessionNotes_FirstTurnIsTheSessionNotes pins that a one-message
// conversation reads exactly as before: the session notes, then the text,
// stamped or not. The eval cases and every first turn depend on it.
func TestPrefixSessionNotes_FirstTurnIsTheSessionNotes(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 55, 8, 987654321, time.UTC)
	in := cacheTestNotes(now, "/deployments/shop-backend/orders-api")
	want := SessionNotes(in) + "\n\nLa tienda está dando errores"

	plain := []Message{{Role: RoleUser, Content: "La tienda está dando errores"}}
	if got := PrefixSessionNotes(plain, in)[0].Content; got != want {
		t.Errorf("unstamped first message:\n got %q\nwant %q", got, want)
	}
	stamped := []Message{{Role: RoleUser, Content: "La tienda está dando errores"}}
	StampTurn(stamped, now, in.CurrentPath)
	if got := PrefixSessionNotes(stamped, in)[0].Content; got != want {
		t.Errorf("stamped first message:\n got %q\nwant %q", got, want)
	}
}

// TestPrefixSessionNotes_HistoryReadsTheSameNextTurn is the cache contract:
// everything a turn sent reads byte for byte the same in the next turn's
// request, after the browser sent it back, while the new message carries the
// new time and page. Built from the request, the first message's "# Now"
// changed every turn and a follow-up rewrote the whole history (2026-10-12).
func TestPrefixSessionNotes_HistoryReadsTheSameNextTurn(t *testing.T) {
	t1 := time.Date(2026, 10, 12, 14, 55, 8, 987654321, time.UTC)
	t2 := t1.Add(80 * time.Second)

	msgs := []Message{{Role: RoleUser, Content: "La tienda está dando errores", Timestamp: t1.Add(-2 * time.Second)}}
	StampTurn(msgs, t1, "/deployments/shop-frontend/shop-web")
	in1 := cacheTestNotes(t1, "/deployments/shop-frontend/shop-web")
	msgs = append(msgs,
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "get_pod_logs", Input: json.RawMessage(`{}`)}}, Timestamp: t1},
		Message{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "c1", Content: "57014"}}, Timestamp: t1},
		Message{Role: RoleAssistant, Content: "La causa es statement_timeout.", Timestamp: t1},
	)
	turn1 := providerView(PrefixSessionNotes(msgs, in1))

	next := roundTripLikeTheBrowser(t, msgs)
	next = append(next, Message{Role: RoleUser, Content: "¿a qué valor lo dejo?", Timestamp: t2.Add(-time.Second)})
	StampTurn(next, t2, "/pods")
	in2 := cacheTestNotes(t2, "/pods")
	turn2 := providerView(PrefixSessionNotes(next, in2))

	for i := range turn1 {
		if turn1[i] != turn2[i] {
			t.Fatalf("message %d changed between turns:\nturn 1 %q\nturn 2 %q", i, turn1[i], turn2[i])
		}
	}
	last := turn2[len(turn2)-1]
	for _, want := range []string{"current_view: /pods", "now (Europe/Madrid): 2026-10-12T16:56:28+02:00", "\n\n¿a qué valor lo dejo?"} {
		if !strings.Contains(last, want) {
			t.Errorf("the new message lacks %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, "cluster:") || strings.Contains(last, "Operator language") {
		t.Errorf("a later message repeats the session block:\n%s", last)
	}
}

// TestStampTurn_OnlyTheTurnsMessage: the stamp goes on the operator message
// the turn answers and nowhere else; the server's clock replaces the
// browser's.
func TestStampTurn_OnlyTheTurnsMessage(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 56, 28, 0, time.UTC)
	browser := now.Add(-37 * time.Second)

	msgs := []Message{
		{Role: RoleUser, Content: "q1", Timestamp: browser, View: "/a"},
		{Role: RoleAssistant, Content: "a1", Timestamp: browser},
		{Role: RoleUser, Content: "q2", Timestamp: browser},
	}
	StampTurn(msgs, now, "")
	if !msgs[2].Timestamp.Equal(now) || msgs[2].View != "/" {
		t.Errorf("turn's message = %v %q, want the server's time and /", msgs[2].Timestamp, msgs[2].View)
	}
	if !msgs[0].Timestamp.Equal(browser) || msgs[0].View != "/a" {
		t.Errorf("an earlier message was restamped: %v %q", msgs[0].Timestamp, msgs[0].View)
	}

	ending := []Message{{Role: RoleUser, Content: "q"}, {Role: RoleAssistant, Content: "a"}}
	StampTurn(ending, now, "/x")
	if !ending[0].Timestamp.IsZero() || ending[0].View != "" {
		t.Errorf("stamped a message the turn does not answer: %+v", ending[0])
	}
}
