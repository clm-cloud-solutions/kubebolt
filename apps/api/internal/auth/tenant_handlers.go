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
// tenant. The lifecycle endpoints that would create a SECOND tenant —
// CreateTenant / DeleteTenant — are gated SERVER-SIDE behind the
// MultiTenantEnabled seam (edition.go): OSS returns 409 + code
// "requires_ee" so the UI can show an upgrade CTA, EE flips the seam to
// unlock them. The read + per-token endpoints (GetTenant / ListTenants /
// ListTokens / IssueToken / RotateToken / RevokeToken / limits) stay open
// because they operate on the existing default tenant and are harmless
// single-tenant. This supersedes the earlier "expose everything, let the
// frontend hide it" policy — a UI-only guard left the REST surface
// reachable, which is not a real boundary.
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
	store          TenantStore
	ingestTokens   IngestTokenStore
	invalidators   []CacheInvalidator
	limitsDefaults EffectiveLimits
}

// NewTenantHandlers constructs the handler set. invalidators are
// optional; pass every BearerIngestAuth / TokenReviewAuth instance the
// process owns so revoke / rotate / disable take effect immediately.
// limitsDefaults are the fleet-wide Prom remote_write defaults used to
// resolve per-tenant overrides on the /admin/tenants/:id/limits
// surface. ingestTokens is the dedicated ingest-token store (tokens no
// longer live inlined in the tenant record).
func NewTenantHandlers(store TenantStore, ingestTokens IngestTokenStore, limitsDefaults EffectiveLimits, invalidators ...CacheInvalidator) *TenantHandlers {
	return &TenantHandlers{
		store:          store,
		ingestTokens:   ingestTokens,
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

func summarizeTenant(t *Tenant, tokens []IngestToken) tenantResponse {
	now := time.Now().UTC()
	var active int
	for i := range tokens {
		if tokens[i].Active(now) {
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
		TokenCount:       len(tokens),
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

// writeErrCode is writeErr plus a machine-readable code the frontend can key
// off (e.g. ErrCodeRequiresEE to render the upgrade CTA) without parsing the
// human message.
func writeErrCode(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]string{"error": msg, "code": errCode})
}

// ─── Handlers ─────────────────────────────────────────────────────────

// resolvedTenantOrg returns the caller's real org, or "" for the default-tenant
// / OSS path (left unrestricted — there is only ever the one tenant there).
// Mirrors the metric-query activeTenantID discriminator: the default-tenant
// NAME sentinel and an unauthenticated request are NOT a real, stamped org.
func resolvedTenantOrg(r *http.Request) string {
	tid := ContextTenantID(r)
	if tid == "" || tid == DefaultTenantName {
		return ""
	}
	return tid
}

func (h *TenantHandlers) ListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.store.ListTenants()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Tenant isolation: a real org sees only its OWN tenant record (its ingest
	// activity, tokens, limits). Without this an org admin saw every other org's
	// tenant card on the Agents & Ingest activity page. Default/OSS ("") is
	// unrestricted (single tenant).
	org := resolvedTenantOrg(r)
	out := make([]tenantResponse, 0, len(tenants))
	for i := range tenants {
		if org != "" && tenants[i].ID != org {
			continue
		}
		toks, _ := h.ingestTokens.ListByTenant(r.Context(), tenants[i].ID)
		out = append(out, summarizeTenant(&tenants[i], toks))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *TenantHandlers) CreateTenant(w http.ResponseWriter, r *http.Request) {
	// OSS guardrail: a second organization is an EE/SaaS capability. The
	// default tenant is auto-seeded at boot, never via this endpoint, so in
	// OSS this is always an attempt to create tenant #2 → 409 requires_ee.
	if !MultiTenantEnabled {
		writeErrCode(w, http.StatusConflict, ErrCodeRequiresEE,
			"creating additional organizations requires KubeBolt SaaS or Enterprise")
		return
	}
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
	writeJSON(w, http.StatusCreated, summarizeTenant(t, nil)) // fresh tenant has no tokens
}

func (h *TenantHandlers) GetTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.store.GetTenant(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	toks, _ := h.ingestTokens.ListByTenant(r.Context(), t.ID)
	resp := tenantWithTokensResponse{tenantResponse: summarizeTenant(t, toks)}
	now := time.Now().UTC()
	resp.IngestTokens = make([]ingestTokenResponse, 0, len(toks))
	for _, tok := range toks {
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
			if err := ValidatePlan(*req.Plan); err != nil {
				return err
			}
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
	toks, _ := h.ingestTokens.ListByTenant(r.Context(), updated.ID)
	writeJSON(w, http.StatusOK, summarizeTenant(updated, toks))
}

func (h *TenantHandlers) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	// OSS guardrail: deleting tenants is multi-tenant lifecycle management.
	// OSS has exactly the default tenant and deleting it would orphan every
	// user/token, so the operation is EE-only.
	if !MultiTenantEnabled {
		writeErrCode(w, http.StatusConflict, ErrCodeRequiresEE,
			"deleting organizations requires KubeBolt SaaS or Enterprise")
		return
	}
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
	toks, _ := h.ingestTokens.ListByTenant(r.Context(), t.ID)
	now := time.Now().UTC()
	out := make([]ingestTokenResponse, 0, len(toks))
	for _, tok := range toks {
		out = append(out, tokenInfo(tok, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *TenantHandlers) IssueToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Label      string `json:"label"`
		TTLSeconds int64  `json:"ttlSeconds,omitempty"`
		// ClusterID scopes the token to a specific cluster
		// (kube-system namespace UID). Empty means "any cluster" —
		// matches the legacy issue flow that didn't capture a
		// cluster, and any integration card that wants
		// cross-cluster visibility.
		ClusterID string `json:"clusterId,omitempty"`
		// TeamID is the team that will own the cluster registered with this
		// token (Track D — team-scoped clusters). Empty = unassigned.
		TeamID string `json:"teamId,omitempty"`
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
	plaintext, tok, err := h.ingestTokens.Issue(r.Context(), id, req.ClusterID, req.TeamID, req.Label, issuer, ttl)
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
	plaintext, tok, err := h.ingestTokens.Rotate(r.Context(), tenantID, tokenID, issuer)
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
	if err := h.ingestTokens.Revoke(r.Context(), tenantID, tokenID); err != nil {
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

// requireOwnTenant blocks cross-tenant access to the /{id} subtree: for a real
// resolved org the URL {id} MUST equal it, otherwise 404 (not 403 — a 404 won't
// confirm that another org's id exists, so it's not an enumeration oracle). The
// default-tenant / OSS path (resolvedTenantOrg=="") is unrestricted. This stops
// an org admin from reading or mutating another org's tokens/limits by guessing
// its tenant UUID.
func (h *TenantHandlers) requireOwnTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if org := resolvedTenantOrg(r); org != "" && chi.URLParam(r, "id") != org {
			writeErr(w, http.StatusNotFound, "tenant not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RegisterRoutes mounts the handlers on r. The caller is responsible
// for wrapping with RequireAuth + RequireRole(RoleAdmin) — see
// router.go for the integration site.
func (h *TenantHandlers) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListTenants)
	r.Post("/", h.CreateTenant)
	// Every /{id} route is tenant-guarded so an org can only touch its own
	// record (paths are unchanged: /{id}, /{id}/tokens, /{id}/limits, …).
	r.Route("/{id}", func(r chi.Router) {
		r.Use(h.requireOwnTenant)
		r.Get("/", h.GetTenant)
		r.Put("/", h.UpdateTenant)
		r.Delete("/", h.DeleteTenant)
		r.Get("/tokens", h.ListTokens)
		r.Post("/tokens", h.IssueToken)
		r.Post("/tokens/{tokenID}/rotate", h.RotateToken)
		r.Delete("/tokens/{tokenID}", h.RevokeToken)
		// Per-tenant Prom remote_write limits (Phase 3 of the Universal
		// Data Plane Plan).
		r.Get("/limits", h.GetTenantLimits)
		r.Put("/limits", h.SetTenantLimits)
		r.Delete("/limits", h.ResetTenantLimits)
	})
}
