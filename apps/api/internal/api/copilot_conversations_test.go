package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// localUser matches conversationUserID's fallback when no auth context is set,
// so seeded records are owned by the same user the handlers resolve.
const localUser = copilot.FallbackConversationUser

func seedConv(t *testing.T, store copilot.ConversationStore, id, cluster, title string, msgs ...copilot.Message) {
	t.Helper()
	rec := &copilot.ConversationRecord{
		ID: id, TenantID: copilot.DefaultConversationTenant, UserID: localUser,
		ClusterID: cluster, Title: title, Messages: msgs,
	}
	if err := store.Upsert(rec); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
}

func newConvHandlers() (*handlers, copilot.ConversationStore) {
	store := copilot.NewMemoryConversationStore()
	return &handlers{copilotConversations: store}, store
}

func TestConversations_ServiceUnavailableWhenNil(t *testing.T) {
	h := &handlers{} // no store
	req := httptest.NewRequest(http.MethodGet, "/copilot/conversations", nil)
	rec := httptest.NewRecorder()
	h.handleListConversations(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestListConversations_NewestFirst_NoTranscriptLeak(t *testing.T) {
	h, store := newConvHandlers()
	seedConv(t, store, "c1", "prod", "older", copilot.Message{Role: copilot.RoleUser, Content: "first question"})
	seedConv(t, store, "c2", "prod", "newer", copilot.Message{Role: copilot.RoleUser, Content: "second question"})

	req := httptest.NewRequest(http.MethodGet, "/copilot/conversations", nil)
	rec := httptest.NewRecorder()
	h.handleListConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Conversations []map[string]json.RawMessage `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Conversations) != 2 {
		t.Fatalf("want 2 conversations, got %d", len(resp.Conversations))
	}
	// Newest-first (c2 was upserted last → newer UpdatedAt).
	var firstID string
	json.Unmarshal(resp.Conversations[0]["id"], &firstID)
	if firstID != "c2" {
		t.Fatalf("not newest-first: first id = %q", firstID)
	}
	// The list MUST NOT leak the full transcript.
	if _, leaked := resp.Conversations[0]["messages"]; leaked {
		t.Fatalf("list response leaked the messages transcript")
	}
	// Preview + messageCount present.
	if _, ok := resp.Conversations[0]["preview"]; !ok {
		t.Fatalf("summary missing preview")
	}
}

func TestListConversations_SearchAndClusterFilter(t *testing.T) {
	h, store := newConvHandlers()
	seedConv(t, store, "c1", "prod", "payments OOM", copilot.Message{Role: copilot.RoleUser, Content: "payments pod OOMing"})
	seedConv(t, store, "c2", "stage", "ingress issue", copilot.Message{Role: copilot.RoleUser, Content: "503 from ingress"})

	// Cluster filter.
	req := httptest.NewRequest(http.MethodGet, "/copilot/conversations?cluster=stage", nil)
	rec := httptest.NewRecorder()
	h.handleListConversations(rec, req)
	var resp struct {
		Conversations []conversationSummary `json:"conversations"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Conversations) != 1 || resp.Conversations[0].ID != "c2" {
		t.Fatalf("cluster filter wrong: %+v", resp.Conversations)
	}

	// Search over content.
	req = httptest.NewRequest(http.MethodGet, "/copilot/conversations?q=OOMing", nil)
	rec = httptest.NewRecorder()
	h.handleListConversations(rec, req)
	resp.Conversations = nil
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Conversations) != 1 || resp.Conversations[0].ID != "c1" {
		t.Fatalf("search wrong: %+v", resp.Conversations)
	}
}

func TestGetConversation_FoundAndNotFound(t *testing.T) {
	h, store := newConvHandlers()
	seedConv(t, store, "c1", "prod", "payments OOM",
		copilot.Message{Role: copilot.RoleUser, Content: "why OOM?"},
		copilot.Message{Role: copilot.RoleAssistant, Content: "memory limit too low"},
	)

	// Found — full transcript returned for rehydration.
	r := chi.NewRouter()
	r.Get("/copilot/conversations/{id}", h.handleGetConversation)

	req := httptest.NewRequest(http.MethodGet, "/copilot/conversations/c1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got copilot.ConversationRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("transcript not returned in full: %d msgs", len(got.Messages))
	}

	// Not found.
	req = httptest.NewRequest(http.MethodGet, "/copilot/conversations/nope", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPatchConversation_RenameArchiveValidate(t *testing.T) {
	h, store := newConvHandlers()
	seedConv(t, store, "c1", "prod", "heuristic", copilot.Message{Role: copilot.RoleUser, Content: "hi"})

	r := chi.NewRouter()
	r.Patch("/copilot/conversations/{id}", h.handlePatchConversation)

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/copilot/conversations/c1", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// Rename.
	if rec := patch(`{"title":"  Refined Title  "}`); rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d (%s)", rec.Code, rec.Body.String())
	}
	got, _, _ := store.Get(copilot.DefaultConversationTenant, localUser, "c1")
	if got.Title != "Refined Title" {
		t.Fatalf("title not sanitized/applied: %q", got.Title)
	}

	// Empty title rejected.
	if rec := patch(`{"title":"   "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty title status = %d, want 400", rec.Code)
	}

	// Nothing to update rejected.
	if rec := patch(`{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d, want 400", rec.Code)
	}

	// Archive.
	if rec := patch(`{"archived":true}`); rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d", rec.Code)
	}
	got, _, _ = store.Get(copilot.DefaultConversationTenant, localUser, "c1")
	if !got.Archived {
		t.Fatalf("archived not applied")
	}

	// Transcript sync (persists an action-proposal outcome before the next turn).
	if rec := patch(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"done"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("messages patch status = %d (%s)", rec.Code, rec.Body.String())
	}
	got, _, _ = store.Get(copilot.DefaultConversationTenant, localUser, "c1")
	if len(got.Messages) != 2 {
		t.Fatalf("transcript not synced via PATCH: %d msgs", len(got.Messages))
	}

	// Patch a non-existent conversation → 404.
	req := httptest.NewRequest(http.MethodPatch, "/copilot/conversations/nope", bytes.NewBufferString(`{"title":"x"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch missing status = %d, want 404", rec.Code)
	}
}

func TestDeleteConversation(t *testing.T) {
	h, store := newConvHandlers()
	seedConv(t, store, "c1", "prod", "x", copilot.Message{Role: copilot.RoleUser, Content: "hi"})

	r := chi.NewRouter()
	r.Delete("/copilot/conversations/{id}", h.handleDeleteConversation)

	req := httptest.NewRequest(http.MethodDelete, "/copilot/conversations/c1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	if _, ok, _ := store.Get(copilot.DefaultConversationTenant, localUser, "c1"); ok {
		t.Fatalf("record not deleted")
	}
}
