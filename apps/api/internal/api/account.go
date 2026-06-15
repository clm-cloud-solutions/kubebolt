package api

import (
	"errors"
	"net/http"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// account.go — the authed "this is my org" surface (Track D entry layer).
// Both handlers resolve the requesting org from context (auth.ContextTenantID,
// stamped by the ResolveTenant middleware) and read the stores already wired
// onto the api handlers struct — tenantsStore for the plan, usage for the
// metering summary. No extra main.go wiring needed.

// accountPlanResponse is the org's tenant/plan view behind GET /account/plan.
type accountPlanResponse struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	Plan   string             `json:"plan"`
	Limits *auth.TenantLimits `json:"limits,omitempty"`
}

// handleAccountPlan returns the requesting org's tenant info (id, name, plan,
// limits). 503 when no tenant store is wired (auth/persistence disabled).
func (h *handlers) handleAccountPlan(w http.ResponseWriter, r *http.Request) {
	if h.tenantsStore == nil {
		respondError(w, http.StatusServiceUnavailable, "tenant store unavailable")
		return
	}
	org := auth.ContextTenantID(r)

	// ContextTenantID resolves to the default-tenant NAME ("default") in OSS,
	// which is not a tenant ID. Fall back to the default tenant in that case so
	// GET /account/plan works on a single-org install too.
	t, err := h.tenantsStore.GetTenant(org)
	if err != nil {
		if dt, derr := h.tenantsStore.GetDefaultTenant(); derr == nil && dt != nil {
			t = dt
		} else {
			if errors.Is(err, auth.ErrTenantNotFound) {
				respondError(w, http.StatusNotFound, "organization not found")
				return
			}
			respondError(w, http.StatusInternalServerError, "failed to load organization")
			return
		}
	}

	respondJSON(w, http.StatusOK, accountPlanResponse{
		ID:     t.ID,
		Name:   t.Name,
		Plan:   t.Plan,
		Limits: t.Limits,
	})
}

// accountUsageResponse is the org's metering summary behind GET /account/usage.
type accountUsageResponse struct {
	Usage []usagePoint `json:"usage"`
}

type usagePoint struct {
	Metric string `json:"metric"`
	Total  int64  `json:"total"`
}

// handleAccountUsage returns the requesting org's per-metric usage totals. OSS
// (NoopUsageStore) returns an empty list; EE returns Postgres-backed metering.
func (h *handlers) handleAccountUsage(w http.ResponseWriter, r *http.Request) {
	if h.usage == nil {
		respondJSON(w, http.StatusOK, accountUsageResponse{Usage: []usagePoint{}})
		return
	}
	org := auth.ContextTenantID(r)
	points, err := h.usage.Summary(r.Context(), org)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	out := make([]usagePoint, 0, len(points))
	for _, p := range points {
		out = append(out, usagePoint{Metric: string(p.Metric), Total: p.Total})
	}
	respondJSON(w, http.StatusOK, accountUsageResponse{Usage: out})
}
