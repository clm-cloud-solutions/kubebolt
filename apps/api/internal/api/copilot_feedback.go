package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// 👍/👎 on Kobi answers (doc #67 §4.4, phase 1c). A rating names the turn the
// answer closed — the chat handler stamps the turn's id on its final assistant
// message (Message.TurnID) — inside the user's own conversation, so a user can
// only ever rate what Kobi told them. The last rating given is the one kept;
// rating "" withdraws it.

type feedbackStoreBox struct{ s copilot.FeedbackStore }

var feedbackStorePtr atomic.Pointer[feedbackStoreBox]

// SetCopilotFeedbackStore installs the store the feedback endpoints use. Unset,
// they answer 503.
func SetCopilotFeedbackStore(s copilot.FeedbackStore) { feedbackStorePtr.Store(&feedbackStoreBox{s}) }

func copilotFeedbackStore() copilot.FeedbackStore {
	if b := feedbackStorePtr.Load(); b != nil {
		return b.s
	}
	return nil
}

// newTurnID names a chat turn: 16 random hex characters.
func newTurnID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// stampTurn marks the turn's final assistant message with its id — the answer a
// 👍/👎 is given against. Messages already stamped by an earlier turn keep
// their id.
func stampTurn(msgs []copilot.Message, turnID string) []copilot.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != copilot.RoleAssistant {
			continue
		}
		if msgs[i].TurnID == "" && msgs[i].Content != "" {
			out := append([]copilot.Message(nil), msgs...)
			out[i].TurnID = turnID
			return out
		}
		return msgs
	}
	return msgs
}

type feedbackRequest struct {
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId"`
	Rating         string `json:"rating"` // "up" | "down" | "" (withdraw)
	Reason         string `json:"reason,omitempty"`
	Comment        string `json:"comment,omitempty"`
}

// handleCopilotFeedback sets or withdraws the caller's rating of one answer.
func (h *handlers) handleCopilotFeedback(w http.ResponseWriter, r *http.Request) {
	store := copilotFeedbackStore()
	if store == nil || h.copilotConversations == nil {
		respondError(w, http.StatusServiceUnavailable, "feedback is unavailable (persistence not configured)")
		return
	}
	var req feedbackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tenant, user := auth.ContextTenantID(r), conversationUserID(r)
	conv, ok, err := h.copilotConversations.Get(tenant, user, req.ConversationID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load conversation")
		return
	}
	if !ok || !conversationHasTurn(conv, req.TurnID) {
		respondError(w, http.StatusNotFound, "answer not found")
		return
	}

	if req.Rating == "" {
		if _, err := store.Delete(tenant, user, req.TurnID); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to withdraw the rating")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"feedback": nil})
		return
	}

	f := copilot.Feedback{
		TenantID:       tenant,
		UserID:         user,
		ConversationID: req.ConversationID,
		TurnID:         req.TurnID,
		Rating:         req.Rating,
		Reason:         req.Reason,
		Comment:        req.Comment,
		Provider:       conv.Provider,
		Model:          conv.Model,
	}
	if err := f.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.manager != nil {
		f.ClusterID, f.TeamID = h.sessionClusterAndTeam(r.Context(), conv.ClusterID, false)
	}
	prev, err := store.Set(f)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save the rating")
		return
	}
	// The metric counts a rating when it is given or changed — not a comment
	// edited under the same rating, nor a withdrawal. One org in this edition:
	// the tenant label stays empty, like every other Kobi series.
	if prev == nil || prev.Rating != f.Rating || prev.Reason != f.Reason {
		kobiMetrics().ObserveFeedback("", f.ClusterID, f.Rating, f.Reason)
	}
	respondJSON(w, http.StatusOK, map[string]any{"feedback": f})
}

// handleConversationFeedback lists the caller's ratings in one of their
// conversations, so a resumed chat shows its 👍/👎.
func (h *handlers) handleConversationFeedback(w http.ResponseWriter, r *http.Request) {
	store := copilotFeedbackStore()
	if store == nil || h.copilotConversations == nil {
		respondError(w, http.StatusServiceUnavailable, "feedback is unavailable (persistence not configured)")
		return
	}
	tenant, user, id := auth.ContextTenantID(r), conversationUserID(r), chi.URLParam(r, "id")
	if _, ok, err := h.copilotConversations.Get(tenant, user, id); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load conversation")
		return
	} else if !ok {
		respondError(w, http.StatusNotFound, "conversation not found")
		return
	}
	list, err := store.ForConversation(tenant, user, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load the ratings")
		return
	}
	if list == nil {
		list = []copilot.Feedback{}
	}
	respondJSON(w, http.StatusOK, map[string]any{"feedback": list})
}

// conversationHasTurn reports whether an answer of the conversation carries
// the turn id.
func conversationHasTurn(conv *copilot.ConversationRecord, turnID string) bool {
	if conv == nil || turnID == "" {
		return false
	}
	for _, m := range conv.Messages {
		if m.Role == copilot.RoleAssistant && m.TurnID == turnID {
			return true
		}
	}
	return false
}
