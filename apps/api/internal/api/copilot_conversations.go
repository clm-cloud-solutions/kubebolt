package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// Kobi conversation history endpoints. Conversations are PERSONAL: every
// handler scopes by (tenant, user) so a user can only ever reach their own.
// Writes happen in HandleCopilotChat's write-through; these endpoints are the
// read/manage surface (list, resume, rename, archive, delete).

const (
	defaultConversationListLimit = 100
	maxConversationListLimit     = 500
)

// conversationUserID resolves the owning user for conversation scoping,
// falling back to the stable "local" id when auth is disabled so single-user
// installs still get a coherent, isolated history.
func conversationUserID(r *http.Request) string {
	uid := auth.ContextUserID(r)
	if uid == "" {
		uid = copilot.FallbackConversationUser
	}
	return uid
}

// conversationSummary is the list-row shape — metadata only, never the full
// transcript, so the history list stays cheap regardless of conversation size.
type conversationSummary struct {
	ID                   string    `json:"id"`
	Title                string    `json:"title"`
	ClusterID            string    `json:"clusterId"`
	Preview              string    `json:"preview"`
	MessageCount         int       `json:"messageCount"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
	Provider             string    `json:"provider,omitempty"`
	Model                string    `json:"model,omitempty"`
	Trigger              string    `json:"trigger,omitempty"`
	OriginatingInsightID string    `json:"originatingInsightId,omitempty"`
	Archived             bool      `json:"archived,omitempty"`
}

func toConversationSummary(rec *copilot.ConversationRecord) conversationSummary {
	return conversationSummary{
		ID:                   rec.ID,
		Title:                rec.Title,
		ClusterID:            rec.ClusterID,
		Preview:              rec.Preview(),
		MessageCount:         rec.VisibleMessageCount(),
		CreatedAt:            rec.CreatedAt,
		UpdatedAt:            rec.UpdatedAt,
		Provider:             rec.Provider,
		Model:                rec.Model,
		Trigger:              rec.Trigger,
		OriginatingInsightID: rec.OriginatingInsightID,
		Archived:             rec.Archived,
	}
}

// handleListConversations returns the caller's conversations, newest-first.
// Query params: cluster (filter), q (search title + chat text), archived
// (true to include archived), limit.
func (h *handlers) handleListConversations(w http.ResponseWriter, r *http.Request) {
	if h.copilotConversations == nil {
		respondError(w, http.StatusServiceUnavailable, "conversation history is unavailable (persistence not configured)")
		return
	}
	q := copilot.ConversationQuery{
		TenantID:        copilot.DefaultConversationTenant,
		UserID:          conversationUserID(r),
		ClusterID:       strings.TrimSpace(r.URL.Query().Get("cluster")),
		Search:          strings.TrimSpace(r.URL.Query().Get("q")),
		IncludeArchived: r.URL.Query().Get("archived") == "true",
		Limit:           defaultConversationListLimit,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > maxConversationListLimit {
				n = maxConversationListLimit
			}
			q.Limit = n
		}
	}

	records, err := h.copilotConversations.List(q)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list conversations")
		return
	}
	out := make([]conversationSummary, 0, len(records))
	for i := range records {
		out = append(out, toConversationSummary(&records[i]))
	}
	respondJSON(w, http.StatusOK, map[string]any{"conversations": out})
}

// handleGetConversation returns one full transcript for rehydration / resume,
// scoped to the owner. 404 when the conversation doesn't exist for this user.
func (h *handlers) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	if h.copilotConversations == nil {
		respondError(w, http.StatusServiceUnavailable, "conversation history is unavailable (persistence not configured)")
		return
	}
	id := chi.URLParam(r, "id")
	rec, ok, err := h.copilotConversations.Get(copilot.DefaultConversationTenant, conversationUserID(r), id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load conversation")
		return
	}
	if !ok {
		respondError(w, http.StatusNotFound, "conversation not found")
		return
	}
	respondJSON(w, http.StatusOK, rec)
}

// conversationPatch is the rename / archive / transcript-sync request body.
// Pointers so an absent field is left unchanged.
type conversationPatch struct {
	Title    *string `json:"title,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
	// Messages, when present, replaces the stored transcript. Used to persist
	// between-turn client state (an action proposal's executed/dismissed
	// outcome) so a refresh before the next chat turn doesn't resurrect an
	// already-run action's Execute button.
	Messages *[]copilot.Message `json:"messages,omitempty"`
}

// handlePatchConversation renames and/or (un)archives a conversation, scoped
// to the owner.
func (h *handlers) handlePatchConversation(w http.ResponseWriter, r *http.Request) {
	if h.copilotConversations == nil {
		respondError(w, http.StatusServiceUnavailable, "conversation history is unavailable (persistence not configured)")
		return
	}
	id := chi.URLParam(r, "id")
	userID := conversationUserID(r)

	var body conversationPatch
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Title == nil && body.Archived == nil && body.Messages == nil {
		respondError(w, http.StatusBadRequest, "nothing to update (provide title, archived, and/or messages)")
		return
	}

	// Ownership check: SetTitle/SetArchived are no-ops for a non-owner, but we
	// 404 explicitly so the client gets a clear signal rather than a silent
	// success that didn't change anything.
	if _, ok, err := h.copilotConversations.Get(copilot.DefaultConversationTenant, userID, id); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load conversation")
		return
	} else if !ok {
		respondError(w, http.StatusNotFound, "conversation not found")
		return
	}

	if body.Title != nil {
		title := copilot.SanitizeTitle(*body.Title)
		if title == "" {
			respondError(w, http.StatusBadRequest, "title cannot be empty")
			return
		}
		if err := h.copilotConversations.SetTitle(copilot.DefaultConversationTenant, userID, id, title); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to rename conversation")
			return
		}
	}
	if body.Archived != nil {
		if err := h.copilotConversations.SetArchived(copilot.DefaultConversationTenant, userID, id, *body.Archived); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to update conversation")
			return
		}
	}
	if body.Messages != nil {
		if err := h.copilotConversations.SetMessages(copilot.DefaultConversationTenant, userID, id, *body.Messages); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to update conversation")
			return
		}
	}

	rec, _, err := h.copilotConversations.Get(copilot.DefaultConversationTenant, userID, id)
	if err != nil || rec == nil {
		respondJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	respondJSON(w, http.StatusOK, toConversationSummary(rec))
}

// handleDeleteConversation removes one conversation, scoped to the owner.
func (h *handlers) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	if h.copilotConversations == nil {
		respondError(w, http.StatusServiceUnavailable, "conversation history is unavailable (persistence not configured)")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.copilotConversations.Delete(copilot.DefaultConversationTenant, conversationUserID(r), id); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete conversation")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}
