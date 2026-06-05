package auth

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// apiTokenView is the safe projection of an APIToken for API responses —
// it omits the Hash (which is persisted but never returned).
type apiTokenView struct {
	ID         string       `json:"id"`
	Prefix     string       `json:"prefix"`
	Label      string       `json:"label"`
	Type       APITokenType `json:"type"`
	Role       Role         `json:"role"`
	Scopes     []string     `json:"scopes,omitempty"`
	TenantID   string       `json:"tenantId,omitempty"`
	ClusterID  string       `json:"clusterId,omitempty"`
	CreatedAt  time.Time    `json:"createdAt"`
	CreatedBy  string       `json:"createdBy"`
	LastUsedAt *time.Time   `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time   `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time   `json:"revokedAt,omitempty"`
}

func toAPITokenView(t APIToken) apiTokenView {
	return apiTokenView{
		ID:         t.ID,
		Prefix:     t.Prefix,
		Label:      t.Label,
		Type:       t.Type,
		Role:       t.Role,
		Scopes:     t.Scopes,
		TenantID:   t.TenantID,
		ClusterID:  t.ClusterID,
		CreatedAt:  t.CreatedAt,
		CreatedBy:  t.CreatedBy,
		LastUsedAt: t.LastUsedAt,
		ExpiresAt:  t.ExpiresAt,
		RevokedAt:  t.RevokedAt,
	}
}

type createAPITokenRequest struct {
	Label    string   `json:"label"`
	Type     string   `json:"type"`     // "service" (default) | "apikey"
	Role     string   `json:"role"`     // "admin" | "editor" (default) | "viewer"
	Scopes   []string `json:"scopes"`   // path prefixes; service default = Autopilot scopes
	TTLHours int      `json:"ttlHours"` // 0 = no expiry
}

type createAPITokenResponse struct {
	// Token is the plaintext secret — shown EXACTLY once, never recoverable.
	Token    string       `json:"token"`
	APIToken apiTokenView `json:"apiToken"`
}

// ListAPITokens returns all REST API tokens (metadata only). Admin only.
func (h *Handlers) ListAPITokens(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		respondError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	toks, err := h.apiTokens.List()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "list api tokens")
		return
	}
	views := make([]apiTokenView, 0, len(toks))
	for _, t := range toks {
		views = append(views, toAPITokenView(t))
	}
	respondJSON(w, http.StatusOK, views)
}

// CreateAPIToken mints a REST API token and returns the plaintext once. Admin only.
func (h *Handlers) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		respondError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	var req createAPITokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	typ := APITokenType(req.Type)
	if req.Type == "" {
		typ = TokenTypeService
	}
	if typ != TokenTypeService && typ != TokenTypeAPIKey {
		respondError(w, http.StatusBadRequest, "type must be 'service' or 'apikey'")
		return
	}

	role := Role(req.Role)
	if req.Role == "" {
		role = RoleEditor // service tokens (Autopilot) mutate; editor is the sane default
	}
	if RoleLevel(role) == 0 {
		respondError(w, http.StatusBadRequest, "role must be 'admin', 'editor' or 'viewer'")
		return
	}

	scopes := req.Scopes
	if len(scopes) == 0 && typ == TokenTypeService {
		scopes = DefaultAutopilotScopes
	}

	var ttl *time.Duration
	if req.TTLHours > 0 {
		d := time.Duration(req.TTLHours) * time.Hour
		ttl = &d
	}

	createdBy := ContextUserID(r)
	plaintext, tok, err := h.apiTokens.Issue(typ, role, scopes, req.Label, createdBy, ttl)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "issue api token")
		return
	}
	respondJSON(w, http.StatusCreated, createAPITokenResponse{
		Token:    plaintext,
		APIToken: toAPITokenView(*tok),
	})
}

// DeleteAPIToken revokes a token by ID. Admin only.
func (h *Handlers) DeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		respondError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.apiTokens.Revoke(id); err != nil {
		if err == ErrTokenNotFound {
			respondError(w, http.StatusNotFound, "token not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "revoke api token")
		return
	}
	// Return a JSON body (not 204) so the web client's deleteRequest helper,
	// which always parses JSON, works uniformly (mirrors agent-token revoke).
	respondJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
