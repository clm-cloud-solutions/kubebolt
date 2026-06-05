package api

import (
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// Locks the "no LLM consumption without a usage record" rule for the
// out-of-chat-loop calls (auto-title, manual compaction): recordAuxUsage must
// persist a SessionRecord attributed to the user, cross-referenced to the
// conversation, with the model + token usage of the call.
func TestRecordAuxUsage_PersistsAttributedRecord(t *testing.T) {
	h, cleanup := setupUsageTest(t)
	defer cleanup()

	h.recordAuxUsage(
		"alice", "prod", "conv-1", "auto_title",
		"anthropic", "claude-haiku-4-5",
		copilot.Usage{InputTokens: 120, OutputTokens: 8},
		50*time.Millisecond,
	)

	recs, err := h.copilotUsage.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 aux usage record, got %d", len(recs))
	}
	r := recs[0]
	if r.Trigger != "auto_title" {
		t.Fatalf("trigger = %q, want auto_title", r.Trigger)
	}
	if r.UserID != "alice" {
		t.Fatalf("userId = %q, want alice", r.UserID)
	}
	if r.ConversationID != "conv-1" {
		t.Fatalf("conversationId = %q, want conv-1 (cross-ref lost)", r.ConversationID)
	}
	if r.Model != "claude-haiku-4-5" {
		t.Fatalf("model = %q, want the cheap model for pricing", r.Model)
	}
	if r.Usage.Total() != 128 {
		t.Fatalf("usage total = %d, want 128 (token spend not recorded)", r.Usage.Total())
	}
}

func TestRecordAuxUsage_NilStoreIsNoOp(t *testing.T) {
	h := &handlers{} // no copilotUsage wired (auth/persistence disabled)
	// Must not panic and must be a silent no-op.
	h.recordAuxUsage("u", "c", "cid", "manual_compact", "openai", "gpt-4o-mini",
		copilot.Usage{InputTokens: 10}, time.Millisecond)
}
