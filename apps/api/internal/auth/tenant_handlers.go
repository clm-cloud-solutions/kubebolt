// Package auth — tenant_handlers.go exposes admin REST endpoints to
// manage tenants and their long-lived ingest tokens.
//
// Tenant administration is gated by RoleAdmin (global). In Sprint A
// there is no per-tenant scoping of users — a global admin manages
// every tenant's tokens. Self-service for tenant operators (their own
// users seeing only their own tokens) requires User.TenantID and a
// RoleTenantAdmin role; tracked for Sprint B+.
package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// CacheInvalidator is the hook every authenticator that keeps an
// in-memory token cache implements. The tenant admin handlers call
// InvalidateCache() after any mutation so a revoked / rotated token
// cannot keep an agent connected past the cache TTL.
type CacheInvalidator interface {
	InvalidateCache()
}

// TenantHandlers wires the TenantsStore behind chi routes and fans
// cache invalidation out to every registered authenticator.
type TenantHandlers struct {
	store        *TenantsStore
	invalidators []CacheInvalidator
}

// NewTenantHandlers constructs the handler set. invalidators are
// optional; pass every BearerIngestAuth / TokenReviewAuth instance the
// process owns so revoke / rotate / disable take effect immediately.
func NewTenantHandlers(store *TenantsStore, invalidators ...CacheInvalidator) *TenantHandlers {
	return &TenantHandlers{store: store, invalidators: invalidators}
}

func (h *TenantHandlers) onTokenChange() {
	for _, inv := range h.invalidators {
		if inv != nil {
			inv.InvalidateCache()
		}
	}
}

// ─── DTOs ─────────────────────────────────────────────────────────────
//
// We never send Hash on the wire — that's the whole point of storing
// tokens hashed. tokenInfo derives a "Active" boolean so the UI does
// not have to re-implement Active() in JS.

type tenantResponse struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Plan             string    `json:"plan"`
	Disabled         bool      `json:"disabled"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	TokenCount       int       `json:"tokenCount"`
	ActiveTokenCount int       `json:"activeTokenCount"`
}

type tenantWithTokensResponse struct {
	tenantResponse
	IngestTokens []ingestTokenResponse `json:"ingestTokens"`
}

type ingestTokenResponse struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	Active     bool       `json:"active"`
}

// issuedTokenResponse is the only DTO that carries plaintext. Callers
// must surface it to the operator and never persist it again.
type issuedTokenResponse struct {
	Token string              `json:"token"`
	Info  ingestTokenResponse `json:"info"`
}

func summarizeTenant(t *Tenant) tenantResponse {
	now := time.Now().UTC()
	var active int
	for i := range t.IngestTokens {
		if t.IngestTokens[i].Active(now) {
			active++
		}
	}
	return tenantResponse{
		ID:               t.ID,
		Name:             t.Name,
		Plan:             t.Plan,
		Disabled:         t.Disabled,
		CreatedAt:        t.CreatedAt,
		UpdatedAt:        t.UpdatedAt,
		TokenCount:       len(t.IngestTokens),
		ActiveTokenCount: active,
	}
}

func tokenInfo(tok IngestToken, now time.Time) ingestTokenResponse {
	return ingestTokenResponse{
		ID:         tok.ID,
		Prefix:     tok.Prefix,
		Label:      tok.Label,
		CreatedAt:  tok.CreatedAt,
		CreatedBy:  tok.CreatedBy,
		LastUsedAt: tok.LastUsedAt,
		ExpiresAt:  tok.ExpiresAt,
		RevokedAt:  tok.RevokedAt,
		Active:     tok.Active(now),
	}
}

// ─── HTTP plumbing ────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ─── Handlers ─────────────────────────────────────────────────────────

func (h *TenantHandlers) ListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.store.ListTenants()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]tenantResponse, 0, len(tenants))
	for i := range tenants {
		out = append(out, summarizeTenant(&tenants[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *TenantHandlers) CreateTenant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Plan string `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	t, err := h.store.CreateTenant(req.Name, req.Plan)
	if err != nil {
		switch {
		case errors.Is(err, ErrTenantExists):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, summarizeTenant(t))
}

func (h *TenantHandlers) GetTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.store.GetTenant(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	resp := tenantWithTokensResponse{tenantResponse: summarizeTenant(t)}
	now := time.Now().UTC()
	resp.IngestTokens = make([]ingestTokenResponse, 0, len(t.IngestTokens))
	for _, tok := range t.IngestTokens {
		resp.IngestTokens = append(resp.IngestTokens, tokenInfo(tok, now))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *TenantHandlers) UpdateTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name     *string `json:"name,omitempty"`
		Plan     *string `json:"plan,omitempty"`
		Disabled *bool   `json:"disabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var wasDisabled, nowDisabled bool
	updated, err := h.store.UpdateTenant(id, func(t *Tenant) error {
		wasDisabled = t.Disabled
		if req.Name != nil {
			t.Name = *req.Name
		}
		if req.Plan != nil {
			t.Plan = *req.Plan
		}
		if req.Disabled != nil {
			t.Disabled = *req.Disabled
		}
		nowDisabled = t.Disabled
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrTenantNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrTenantExists):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// Disabling an enabled tenant must invalidate caches so already-
	// authenticated agents can't keep streaming through stale entries.
	if !wasDisabled && nowDisabled {
		h.onTokenChange()
	}
	writeJSON(w, http.StatusOK, summarizeTenant(updated))
}

func (h *TenantHandlers) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.DeleteTenant(id); err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.onTokenChange()
	w.WriteHeader(http.StatusNoContent)
}

func (h *TenantHandlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.store.GetTenant(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	now := time.Now().UTC()
	out := make([]ingestTokenResponse, 0, len(t.IngestTokens))
	for _, tok := range t.IngestTokens {
		out = append(out, tokenInfo(tok, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *TenantHandlers) IssueToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Label      string `json:"label"`
		TTLSeconds int64  `json:"ttlSeconds,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Label == "" {
		writeErr(w, http.StatusBadRequest, "label is required")
		return
	}
	var ttl *time.Duration
	if req.TTLSeconds > 0 {
		d := time.Duration(req.TTLSeconds) * time.Second
		ttl = &d
	}
	issuer := ContextUserID(r)
	if issuer == "" {
		issuer = "system"
	}
	plaintext, tok, err := h.store.IssueToken(id, req.Label, issuer, ttl)
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, issuedTokenResponse{
		Token: plaintext,
		Info:  tokenInfo(*tok, time.Now().UTC()),
	})
}

func (h *TenantHandlers) RotateToken(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")
	tokenID := chi.URLParam(r, "tokenID")
	issuer := ContextUserID(r)
	if issuer == "" {
		issuer = "system"
	}
	plaintext, tok, err := h.store.RotateToken(tenantID, tokenID, issuer)
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) || errors.Is(err, ErrTokenNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.onTokenChange()
	writeJSON(w, http.StatusOK, issuedTokenResponse{
		Token: plaintext,
		Info:  tokenInfo(*tok, time.Now().UTC()),
	})
}

func (h *TenantHandlers) RevokeToken(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")
	tokenID := chi.URLParam(r, "tokenID")
	if err := h.store.RevokeToken(tenantID, tokenID); err != nil {
		if errors.Is(err, ErrTenantNotFound) || errors.Is(err, ErrTokenNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.onTokenChange()
	w.WriteHeader(http.StatusNoContent)
}

// RegisterRoutes mounts the handlers on r. The caller is responsible
// for wrapping with RequireAuth + RequireRole(RoleAdmin) — see
// router.go for the integration site.
func (h *TenantHandlers) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListTenants)
	r.Post("/", h.CreateTenant)
	r.Get("/{id}", h.GetTenant)
	r.Put("/{id}", h.UpdateTenant)
	r.Delete("/{id}", h.DeleteTenant)
	r.Get("/{id}/tokens", h.ListTokens)
	r.Post("/{id}/tokens", h.IssueToken)
	r.Post("/{id}/tokens/{tokenID}/rotate", h.RotateToken)
	r.Delete("/{id}/tokens/{tokenID}", h.RevokeToken)
}
