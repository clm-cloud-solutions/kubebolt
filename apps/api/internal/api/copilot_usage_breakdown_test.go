package api

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

func rec(user, trigger, reason string, fallback bool) copilot.SessionRecord {
	return copilot.SessionRecord{
		UserID: user, Trigger: trigger, Reason: reason, Fallback: fallback,
		Provider: "anthropic", Model: "claude-sonnet-4-6",
		Usage: copilot.Usage{InputTokens: 100, OutputTokens: 20},
	}
}

func TestComputeUsageSummary_AuxAndReliability(t *testing.T) {
	records := []copilot.SessionRecord{
		rec("alice", "manual", "done", false),
		rec("alice", "manual", "error", false),     // error
		rec("alice", "insight", "max_rounds", true), // error + max_rounds + fallback
		rec("alice", "auto_title", "done", false),   // AUX — not an interactive session
		rec("alice", "manual_compact", "done", true), // AUX — fallback, not interactive
	}
	s := computeUsageSummary(records)

	if s.Sessions != 5 {
		t.Fatalf("Sessions = %d, want 5 (all records incl aux)", s.Sessions)
	}
	if s.InteractiveSessions != 3 {
		t.Fatalf("InteractiveSessions = %d, want 3 (excludes auto_title + manual_compact)", s.InteractiveSessions)
	}
	if s.ErrorSessions != 2 {
		t.Fatalf("ErrorSessions = %d, want 2 (error + max_rounds)", s.ErrorSessions)
	}
	if s.MaxRoundsSessions != 1 {
		t.Fatalf("MaxRoundsSessions = %d, want 1", s.MaxRoundsSessions)
	}
	if s.FallbackSessions != 2 {
		t.Fatalf("FallbackSessions = %d, want 2", s.FallbackSessions)
	}
	// Rates are over ALL sessions (5).
	if s.ErrorRate < 39.9 || s.ErrorRate > 40.1 {
		t.Fatalf("ErrorRate = %.1f, want 40.0", s.ErrorRate)
	}
	if s.FallbackRate < 39.9 || s.FallbackRate > 40.1 {
		t.Fatalf("FallbackRate = %.1f, want 40.0", s.FallbackRate)
	}
	if s.InputTokens != 500 {
		t.Fatalf("InputTokens = %d, want 500 (aux spend counted)", s.InputTokens)
	}
}

func TestGroupKeyFor(t *testing.T) {
	cases := []struct {
		groupBy string
		rec     copilot.SessionRecord
		want    string
	}{
		{"user", copilot.SessionRecord{UserID: "alice"}, "alice"},
		{"user", copilot.SessionRecord{UserID: ""}, "(unknown)"},
		{"trigger", copilot.SessionRecord{Trigger: "insight"}, "insight"},
		{"trigger", copilot.SessionRecord{Trigger: ""}, "manual"},
		{"cluster", copilot.SessionRecord{Cluster: ""}, "(unattached)"},
		{"conversation", copilot.SessionRecord{ConversationID: ""}, "(aux)"},
		{"reason", copilot.SessionRecord{Reason: "max_rounds"}, "max_rounds"},
		{"model", copilot.SessionRecord{Provider: "anthropic", Model: "claude-sonnet-4-6"}, "anthropic · claude-sonnet-4-6"},
	}
	for _, c := range cases {
		if got := groupKeyFor(c.rec, c.groupBy); got != c.want {
			t.Errorf("groupKeyFor(%s) = %q, want %q", c.groupBy, got, c.want)
		}
	}
	if !validGroupBy("trigger") || validGroupBy("bogus") {
		t.Fatalf("validGroupBy gate wrong")
	}
}
