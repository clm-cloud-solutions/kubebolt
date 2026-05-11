// Package auth — tenant_handlers.go exposes admin REST endpoints to
// manage tenants and their long-lived ingest tokens.
//
// Tenant administration is gated by RoleAdmin (global). In Sprint A
// there is no per-tenant scoping of users — a global admin manages
// every tenant's tokens. Self-service for tenant operators (their own
// users seeing only their own tokens) requires User.TenantID and a
// RoleTenantAdmin role; tracked for Sprint B+.
//
// ENTERPRISE-CANDIDATE (multi-tenant management):
// The OSS edition operates against a single auto-seeded "default"
// tenant. The endpoints in this file are split:
//
//   OSS               GetTenant/ListTokens/IssueToken/RotateToken/RevokeToken
//                     (operating on the default tenant)
//   Enterprise        ListTenants / CreateTenant / UpdateTenant /
//                     DeleteTenant — and operating any of the
//                     token endpoints against a non-default tenant.
//
// The split is enforced in the frontend (commit 8): the OSS UI only
// shows the default tenant. The backend exposes everything because
// drawing the line server-side would require a license check, which
// we are deferring per ENTERPRISE-CANDIDATE policy until the SaaS
// edition has a license model.
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
//
// limitsDefaults is the fleet-wide fallback the per-tenant limits
// handlers resolve against when a tenant has no override on a
// particular field. Loaded once at startup via
// config.LoadPromWriteLimitsConfig() and converted to EffectiveLimits.
// Stored on the handler (not re-read per request) because env vars
// don't change between restarts — operators raising the fleet
// default need a backend restart, which is the right semantic for a
// system-wide policy change.
type TenantHandlers struct {
	store          *TenantsStore
	invalidators   []CacheInvalidator
	limitsDefaults EffectiveLimits
}

// NewTenantHandlers constructs the handler set. invalidators are
// optional; pass every BearerIngestAuth / TokenReviewAuth instance the
// process owns so revoke / rotate / disable take effect immediately.
// limitsDefaults are the fleet-wide Prom remote_write defaults used to
// resolve per-tenant overrides on the /admin/tenants/:id/limits
// surface.
func NewTenantHandlers(store *TenantsStore, limitsDefaults EffectiveLimits, invalidators ...CacheInvalidator) *TenantHandlers {
	return &TenantHandlers{
		store:          store,
		invalidators:   invalidators,
		limitsDefaults: limitsDefaults,
	}
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

// ─── Per-tenant Prom remote_write limits ──────────────────────────────
//
// These three handlers wrap TenantsStore.SetLimits / ClearLimits + the
// ResolveLimits helper to give the admin UI a clean GET/PUT/DELETE
// surface. The PUT body is a partial patch (TenantLimits JSON with
// omitempty on every field) — sending only `writeBurstSamples`
// preserves the other two overrides.
//
// The GET response carries three views (effective / custom / defaults)
// so the UI can render a "default" vs "custom" badge per field by
// comparing the maps client-side. Validation warnings (soft rules like
// burst < rate) are surfaced via the `X-KubeBolt-Validation-Warnings`
// response header rather than failing the write — operators may want
// the configuration intentionally and the rule is advisory.

// GetTenantLimits returns the effective + custom + defaults view of a
// tenant's Prom remote_write limits.
func (h *TenantHandlers) GetTenantLimits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.store.GetTenant(id)
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, LimitsResponse{
		Effective: ResolveLimits(t.Limits, h.limitsDefaults),
		Custom:    t.Limits,
		Defaults:  h.limitsDefaults,
	})
}

// SetTenantLimits applies a partial override of the tenant's per-tenant
// Prom remote_write limits. Fields present in the body overwrite the
// existing override; fields omitted preserve whatever was already
// there. To clear an individual field, send a value of 0 (which is
// validated as a permissive "block all" posture) or use the DELETE
// endpoint to reset all overrides.
func (h *TenantHandlers) SetTenantLimits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var patch TenantLimits
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	t, val, err := h.store.SetLimits(id, &patch)
	if err != nil {
		switch {
		case errors.Is(err, ErrTenantNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrLimitsValidation):
			writeErr(w, http.StatusBadRequest, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if len(val.Warnings) > 0 {
		// Stringly-typed header keeps the response body shape stable
		// (effective/custom/defaults) — UI can opt in to reading
		// warnings without parsing a polymorphic body.
		w.Header().Set("X-KubeBolt-Validation-Warnings", joinWarnings(val.Warnings))
	}
	writeJSON(w, http.StatusOK, LimitsResponse{
		Effective: ResolveLimits(t.Limits, h.limitsDefaults),
		Custom:    t.Limits,
		Defaults:  h.limitsDefaults,
	})
}

// ResetTenantLimits removes every per-tenant override so the tenant
// inherits the system defaults on every field. Idempotent: returns
// 200 + the resolved view even if the tenant had no overrides.
func (h *TenantHandlers) ResetTenantLimits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.store.ClearLimits(id)
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, LimitsResponse{
		Effective: ResolveLimits(t.Limits, h.limitsDefaults),
		Custom:    t.Limits,
		Defaults:  h.limitsDefaults,
	})
}

func joinWarnings(ws []string) string {
	// Plain semicolon join — no need to import strings for a 3-line
	// helper. Header values must not contain CR/LF; the validator
	// produces clean ASCII so this stays safe.
	out := ""
	for i, w := range ws {
		if i > 0 {
			out += "; "
		}
		out += w
	}
	return out
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
	// Per-tenant Prom remote_write limits (Phase 3 of the Universal
	// Data Plane Plan).
	r.Get("/{id}/limits", h.GetTenantLimits)
	r.Put("/{id}/limits", h.SetTenantLimits)
	r.Delete("/{id}/limits", h.ResetTenantLimits)
}
