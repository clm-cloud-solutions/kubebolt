package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// withFeedbackStore installs a fresh Bolt feedback store for the test.
func withFeedbackStore(t *testing.T) copilot.FeedbackStore {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "fb.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := copilot.NewBoltFeedbackStore(db, []byte("copilot_feedback"))
	if err != nil {
		t.Fatal(err)
	}
	prev := copilotFeedbackStore()
	SetCopilotFeedbackStore(s)
	t.Cleanup(func() { SetCopilotFeedbackStore(prev) })
	return s
}

func postFeedback(h *handlers, body map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/copilot/feedback", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h.handleCopilotFeedback(rec, req)
	return rec
}

func TestStampTurn(t *testing.T) {
	msgs := []copilot.Message{
		{Role: copilot.RoleUser, Content: "why is payments down?"},
		{Role: copilot.RoleAssistant, Content: "OOMKilled.", TurnID: "t1"},
		{Role: copilot.RoleUser, Content: "and now?"},
		{Role: copilot.RoleAssistant, Content: "Recovered."},
	}
	out := stampTurn(msgs, "t2")
	if out[3].TurnID != "t2" || out[1].TurnID != "t1" {
		t.Fatalf("stamp = %q / %q, want t2 on the last answer and t1 kept", out[3].TurnID, out[1].TurnID)
	}
	if msgs[3].TurnID != "" {
		t.Fatal("stampTurn mutated its input")
	}
	// A turn whose last answer already carries an id is left alone.
	if again := stampTurn(out, "t3"); again[3].TurnID != "t2" {
		t.Fatalf("restamped an answered turn: %q", again[3].TurnID)
	}
}

func TestCopilotFeedback_ServiceUnavailableWithoutStore(t *testing.T) {
	prev := copilotFeedbackStore()
	SetCopilotFeedbackStore(nil)
	t.Cleanup(func() { SetCopilotFeedbackStore(prev) })
	h, _ := newConvHandlers()
	if rec := postFeedback(h, map[string]any{"conversationId": "c1", "turnId": "t1", "rating": "up"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestCopilotFeedback_RateChangeWithdraw(t *testing.T) {
	store := withFeedbackStore(t)
	m := withKobiMetrics(t)
	h, convs := newConvHandlers()
	seedConv(t, convs, "c1", "prod", "payments",
		copilot.Message{Role: copilot.RoleUser, Content: "why is payments down?"},
		copilot.Message{Role: copilot.RoleAssistant, Content: "OOMKilled.", TurnID: "t1"})
	count := func(rating, reason string) float64 {
		return testutil.ToFloat64(m.feedback.WithLabelValues("", "", rating, reason))
	}

	if rec := postFeedback(h, map[string]any{"conversationId": "c1", "turnId": "t1", "rating": "up"}); rec.Code != http.StatusOK {
		t.Fatalf("👍 status = %d (%s)", rec.Code, rec.Body.String())
	}
	if count("up", "none") != 1 {
		t.Fatalf("👍 not counted: %v", count("up", "none"))
	}

	// Changed to 👎 with a reason and a comment: counted again, under the reason.
	rec := postFeedback(h, map[string]any{"conversationId": "c1", "turnId": "t1", "rating": "down",
		"reason": "incorrect", "comment": "it was evicted, not OOMKilled"})
	if rec.Code != http.StatusOK {
		t.Fatalf("👎 status = %d (%s)", rec.Code, rec.Body.String())
	}
	if count("down", "incorrect") != 1 {
		t.Fatal("👎 not counted under its reason")
	}
	got, _ := store.ForConversation(copilot.DefaultConversationTenant, localUser, "c1")
	if len(got) != 1 || got[0].Rating != "down" || got[0].Comment != "it was evicted, not OOMKilled" {
		t.Fatalf("stored = %+v", got)
	}

	// Only the comment edited: no new count.
	postFeedback(h, map[string]any{"conversationId": "c1", "turnId": "t1", "rating": "down",
		"reason": "incorrect", "comment": "evicted on DiskPressure"})
	if count("down", "incorrect") != 1 {
		t.Fatal("a comment edit was counted as a new rating")
	}

	// Withdrawn: gone, not counted.
	if rec := postFeedback(h, map[string]any{"conversationId": "c1", "turnId": "t1", "rating": ""}); rec.Code != http.StatusOK {
		t.Fatalf("withdraw status = %d", rec.Code)
	}
	if got, _ := store.ForConversation(copilot.DefaultConversationTenant, localUser, "c1"); len(got) != 0 {
		t.Fatalf("withdrawn rating still stored: %+v", got)
	}
	if count("up", "none") != 1 || count("down", "incorrect") != 1 {
		t.Fatal("a withdrawal moved the counters")
	}
}

func TestCopilotFeedback_OnlyOwnAnswers(t *testing.T) {
	withFeedbackStore(t)
	h, convs := newConvHandlers()
	seedConv(t, convs, "c1", "prod", "mine",
		copilot.Message{Role: copilot.RoleAssistant, Content: "answer", TurnID: "t1"})
	// Someone else's conversation, with an answer of its own.
	if err := convs.Upsert(&copilot.ConversationRecord{ID: "c2", TenantID: copilot.DefaultConversationTenant, UserID: "bob",
		Messages: []copilot.Message{{Role: copilot.RoleAssistant, Content: "bob's answer", TurnID: "t9"}}}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown turn", map[string]any{"conversationId": "c1", "turnId": "nope", "rating": "up"}, http.StatusNotFound},
		{"another user's conversation", map[string]any{"conversationId": "c2", "turnId": "t9", "rating": "down"}, http.StatusNotFound},
		{"turn of another conversation", map[string]any{"conversationId": "c1", "turnId": "t9", "rating": "up"}, http.StatusNotFound},
		{"unknown reason", map[string]any{"conversationId": "c1", "turnId": "t1", "rating": "down", "reason": "rude"}, http.StatusBadRequest},
		{"a 👍 with a reason", map[string]any{"conversationId": "c1", "turnId": "t1", "rating": "up", "reason": "slow"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := postFeedback(h, c.body); rec.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func TestConversationFeedback_ListAndDeleteWithConversation(t *testing.T) {
	store := withFeedbackStore(t)
	h, convs := newConvHandlers()
	seedConv(t, convs, "c1", "prod", "payments",
		copilot.Message{Role: copilot.RoleAssistant, Content: "one", TurnID: "t1"},
		copilot.Message{Role: copilot.RoleAssistant, Content: "two", TurnID: "t2"})
	postFeedback(h, map[string]any{"conversationId": "c1", "turnId": "t1", "rating": "up"})
	postFeedback(h, map[string]any{"conversationId": "c1", "turnId": "t2", "rating": "down", "reason": "slow"})

	get := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/copilot/conversations/"+id+"/feedback", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		h.handleConversationFeedback(rec, req)
		return rec
	}
	rec := get("c1")
	var resp struct {
		Feedback []copilot.Feedback `json:"feedback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Feedback) != 2 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing conversation = %d, want 404", rec.Code)
	}

	// Deleting the conversation deletes its ratings.
	req := httptest.NewRequest(http.MethodDelete, "/copilot/conversations/c1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "c1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	h.handleDeleteConversation(httptest.NewRecorder(), req)
	if got, _ := store.ForConversation(copilot.DefaultConversationTenant, localUser, "c1"); len(got) != 0 {
		t.Fatalf("ratings survived their conversation: %+v", got)
	}
}
