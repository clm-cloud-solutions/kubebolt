package auth

import (
	"encoding/json"
	"errors"
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
	Clusters   []string     `json:"clusters,omitempty"`
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
		Clusters:   t.Clusters,
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
	// Clusters is the read allow-list (cluster ids) for an API key; empty =
	// every cluster of the org. Ignored for service tokens, which read the
	// whole org by design (Autopilot acts for the org, not for a team).
	Clusters []string `json:"clusters"`
}

type updateAPITokenClustersRequest struct {
	Clusters []string `json:"clusters"` // empty = every cluster of the org
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
	toks, err := h.apiTokens.List(r.Context())
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
	// Service tokens (kbs_) are install-global machine credentials for internal
	// integrations (e.g. Autopilot↔API) — a PLATFORM concern. No single org may
	// mint one. API keys (kbk_) are per-org and stay org-admin. Edition-aware:
	// in OSS the lone admin is the platform admin.
	if typ == TokenTypeService && !IsPlatformAdminRequest(r) {
		respondError(w, http.StatusForbidden, "service tokens are platform-level; only a platform admin may create them")
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
	// A token that can reach only the MCP endpoint is stored as viewer, whatever
	// role was asked for. The endpoint is read-only by itself (path scope plus
	// the tool allow-list in internal/mcp), so a higher role would grant
	// nothing the token can use — it would only make the row claim a power the
	// token does not have. The form locks the role for the same reason; this
	// keeps a direct API call telling the same story.
	if isMCPOnly(scopes) {
		role = RoleViewer
	}

	var clusters []string
	if typ == TokenTypeAPIKey {
		clusters = normalizeClusterList(req.Clusters)
		if err := h.checkTokenClusters(r, clusters); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	var ttl *time.Duration
	if req.TTLHours > 0 {
		d := time.Duration(req.TTLHours) * time.Hour
		ttl = &d
	}

	createdBy := ContextUserID(r)
	plaintext, tok, err := h.apiTokens.Issue(r.Context(), typ, role, scopes, req.Label, createdBy, ttl)
	if err != nil {
		auditAdmin(r, "issue_api_token", "api_token", req.Label, map[string]any{"type": typ, "role": role}, err)
		respondError(w, http.StatusInternalServerError, "issue api token")
		return
	}
	if len(clusters) > 0 {
		// Issue and the allow-list are two writes. If the second fails the
		// token would read EVERY cluster — wider than asked for — so it is
		// revoked instead of returned.
		if err := h.apiTokens.SetClusters(r.Context(), tok.ID, clusters); err != nil {
			_ = h.apiTokens.Revoke(r.Context(), tok.ID)
			auditAdmin(r, "issue_api_token", "api_token", tok.ID, map[string]any{"type": typ, "role": role, "clusters": clusters}, err)
			respondError(w, http.StatusInternalServerError, "issue api token")
			return
		}
		tok.Clusters = clusters
	}
	// The token id, never the plaintext. Type/role/scopes/clusters are what a
	// reviewer needs to judge whether the grant was appropriate.
	auditAdmin(r, "issue_api_token", "api_token", tok.ID, map[string]any{
		"label":    req.Label,
		"type":     typ,
		"role":     role,
		"scopes":   scopes,
		"clusters": clusters,
	}, nil)
	respondJSON(w, http.StatusCreated, createAPITokenResponse{
		Token:    plaintext,
		APIToken: toAPITokenView(*tok),
	})
}

// UpdateAPITokenClusters replaces an API key's read allow-list. Admin only.
// Service tokens have none by design and are refused.
func (h *Handlers) UpdateAPITokenClusters(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		respondError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	id := chi.URLParam(r, "id")
	var req updateAPITokenClustersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tok, ok := h.findAPIToken(r, id)
	if !ok {
		respondError(w, http.StatusNotFound, "token not found")
		return
	}
	if tok.Type != TokenTypeAPIKey {
		respondError(w, http.StatusBadRequest, "only API keys carry a cluster list; service tokens read the whole organization")
		return
	}
	clusters := normalizeClusterList(req.Clusters)
	if err := h.checkTokenClusters(r, clusters); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.apiTokens.SetClusters(r.Context(), id, clusters); err != nil {
		auditAdmin(r, "update_api_token_clusters", "api_token", id, map[string]any{"clusters": clusters}, err)
		if errors.Is(err, ErrTokenNotFound) {
			respondError(w, http.StatusNotFound, "token not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "update api token")
		return
	}
	auditAdmin(r, "update_api_token_clusters", "api_token", id, map[string]any{
		"label":    tok.Label,
		"from":     tok.Clusters,
		"clusters": clusters,
	}, nil)
	tok.Clusters = clusters
	respondJSON(w, http.StatusOK, toAPITokenView(tok))
}

// findAPIToken reads one token through List, which the Postgres store already
// scopes to the caller's org (RLS): an id from another org is not found.
func (h *Handlers) findAPIToken(r *http.Request, id string) (APIToken, bool) {
	all, err := h.apiTokens.List(r.Context())
	if err != nil {
		return APIToken{}, false
	}
	for _, t := range all {
		if t.ID == id {
			return t, true
		}
	}
	return APIToken{}, false
}

// checkTokenClusters asks the wired check (package api) whether every id is a
// cluster of the caller's org. With no check wired it refuses any list: an
// allow-list nobody validated could name another org's cluster.
func (h *Handlers) checkTokenClusters(r *http.Request, clusters []string) error {
	if len(clusters) == 0 {
		return nil
	}
	if h.tokenClusterCheck == nil {
		return errors.New("cluster lists are not available on this install")
	}
	return h.tokenClusterCheck(r, clusters)
}

// DeleteAPIToken revokes a token by ID. Admin only.
func (h *Handlers) DeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		respondError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.apiTokens.Revoke(r.Context(), id); err != nil {
		auditAdmin(r, "revoke_api_token", "api_token", id, nil, err)
		if err == ErrTokenNotFound {
			respondError(w, http.StatusNotFound, "token not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "revoke api token")
		return
	}
	// Return a JSON body (not 204) so the web client's deleteRequest helper,
	// which always parses JSON, works uniformly (mirrors agent-token revoke).
	auditAdmin(r, "revoke_api_token", "api_token", id, nil, nil)
	respondJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ScopeMCP is the path prefix of the read-only Kobi MCP endpoint.
const ScopeMCP = "/api/v1/mcp"

// isMCPOnly reports whether every scope is the MCP endpoint.
func isMCPOnly(scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, s := range scopes {
		if s != ScopeMCP {
			return false
		}
	}
	return true
}
