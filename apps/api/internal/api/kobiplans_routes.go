package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// The read side of Kobi Plans (plans-04): the chat's plan card and the
// /plans/:id view read a plan here. Mounted outside requireConnector, inside
// WithClusterScope: a plan is a stored record that must answer when its
// cluster is down, and a caller sees only the plans of clusters they may read.

const (
	defaultPlansListed = 50
	maxPlansListed     = 200
)

// planView is a plan as the screens read it.
type planView struct {
	kobiplan.Plan
	ClusterName string `json:"clusterName,omitempty"`
	// EffectiveStatus is the status the person should see: a draft past its
	// expiry reads expired (unexported) before the sweep marks it.
	EffectiveStatus        kobiplan.Status        `json:"effectiveStatus"`
	EffectiveExpiredReason kobiplan.ExpiredReason `json:"effectiveExpiredReason,omitempty"`
	// MaxRisk is the highest risk among the steps, empty without steps.
	MaxRisk kobiplan.RiskLevel `json:"maxRisk,omitempty"`
	// CreatedBy is the author's display name today, else the username the
	// plan recorded (a person since removed).
	CreatedBy string `json:"createdBy,omitempty"`
	// DependsOnPlans are the plans this one counts on and LeadsTo the live
	// consolidation plans that count on it (plans-16).
	DependsOnPlans []planLink `json:"dependsOnPlans,omitempty"`
	LeadsTo        []planLink `json:"leadsTo,omitempty"`
	// ClusterBill is the bill a consolidation plan's headline starts from: its
	// baseline once exported, else the bill today. Nil without OpenCost.
	ClusterBill *kobiplan.Money `json:"clusterBill,omitempty"`
}

// planRefLite names a related plan in a list row.
type planRefLite struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// planSummary is a plan in a list.
type planSummary struct {
	ID                     string                 `json:"id"`
	Title                  string                 `json:"title"`
	Kind                   kobiplan.Kind          `json:"kind"`
	ClusterID              string                 `json:"clusterId"`
	ClusterName            string                 `json:"clusterName,omitempty"`
	Version                int                    `json:"version"`
	Status                 kobiplan.Status        `json:"status"`
	EffectiveStatus        kobiplan.Status        `json:"effectiveStatus"`
	EffectiveExpiredReason kobiplan.ExpiredReason `json:"effectiveExpiredReason,omitempty"`
	CreatedAt              time.Time              `json:"createdAt"`
	ExpiresAt              time.Time              `json:"expiresAt"`
	CreatedBy              string                 `json:"createdBy,omitempty"`
	Estimated              kobiplan.Savings       `json:"estimated"`
	StepCount              int                    `json:"stepCount"`
	AppliedCount           int                    `json:"appliedCount"`
	ExcludedCount          int                    `json:"excludedCount"`
	MaxRisk                kobiplan.RiskLevel     `json:"maxRisk,omitempty"`
	// NodesRemoved is how many nodes a consolidation plan's live steps take
	// out (plans-16).
	NodesRemoved int `json:"nodesRemoved,omitempty"`
	// DependsOn and LeadsTo name the related plans the same list holds.
	DependsOn []planRefLite `json:"dependsOn,omitempty"`
	LeadsTo   []planRefLite `json:"leadsTo,omitempty"`
}

var riskRank = map[kobiplan.RiskLevel]int{kobiplan.RiskLow: 1, kobiplan.RiskMedium: 2, kobiplan.RiskHigh: 3}

// appliedSteps counts the steps already made in the cluster (a verified step
// was applied first): the list tells a plan under way from one untouched.
func appliedSteps(steps []kobiplan.Step) int {
	n := 0
	for _, s := range steps {
		if s.Status == kobiplan.StepApplied || s.Status == kobiplan.StepVerified {
			n++
		}
	}
	return n
}

// nodesRemoved counts the nodes a consolidation plan's steps take out, the
// expired ones aside.
func nodesRemoved(steps []kobiplan.Step) int {
	n := 0
	for _, s := range steps {
		if s.Action != kobiplan.ActionRemoveNodes || s.Status == kobiplan.StepExpired {
			continue
		}
		if p, err := kobiplan.DecodeRemoveNodes(s.Params); err == nil {
			n += len(p.Nodes)
		}
	}
	return n
}

func maxRisk(steps []kobiplan.Step) kobiplan.RiskLevel {
	var out kobiplan.RiskLevel
	for _, s := range steps {
		if riskRank[s.Risk] > riskRank[out] {
			out = s.Risk
		}
	}
	return out
}

// planViewOf is one plan as the card and the view read it, after a change.
func (h *handlers) planViewOf(r *http.Request, p kobiplan.Plan, now time.Time) planView {
	status, reason := effectiveStatus(p, now)
	v := planView{
		Plan: p, ClusterName: h.clusterNames(r)(p.ClusterID),
		EffectiveStatus: status, EffectiveExpiredReason: reason,
		MaxRisk: maxRisk(p.Steps), CreatedBy: h.authorNames(r)(p.Source),
	}
	if store := kobiPlanStore(); store != nil {
		v.DependsOnPlans, v.LeadsTo = planLinks(store, auth.ContextTenantID(r), p, now)
	}
	v.ClusterBill = h.consolidationBill(r.Context(), p, now)
	return v
}

func effectiveStatus(p kobiplan.Plan, now time.Time) (kobiplan.Status, kobiplan.ExpiredReason) {
	if p.Status == kobiplan.StatusDraft && !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(now) {
		return kobiplan.StatusExpired, kobiplan.ExpiredUnexported
	}
	return p.Status, p.ExpiredReason
}

// clusterNames resolves display names once per cluster in a request.
func (h *handlers) clusterNames(r *http.Request) func(string) string {
	names := map[string]string{}
	return func(id string) string {
		if id == "" || h.manager == nil {
			return ""
		}
		name, ok := names[id]
		if !ok {
			name = h.manager.DisplayNameForCluster(r.Context(), id)
			names[id] = name
		}
		return name
	}
}

// authorNames resolves a plan author's display name once per person in a
// request, falling back to the username the plan recorded.
func (h *handlers) authorNames(r *http.Request) func(kobiplan.Source) string {
	names := map[string]string{}
	return func(src kobiplan.Source) string {
		if src.UserID == "" || h.authHandlers == nil {
			return src.UserName
		}
		name, ok := names[src.UserID]
		if !ok {
			name = h.authHandlers.UserDisplayName(r.Context(), src.UserID)
			names[src.UserID] = name
		}
		if name == "" {
			return src.UserName
		}
		return name
	}
}

// GET /plans/{id}
func (h *handlers) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	store := kobiPlanStore()
	if store == nil {
		respondError(w, http.StatusServiceUnavailable, "plans are not enabled on this install")
		return
	}
	p, err := store.Get(auth.ContextTenantID(r), chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, copilot.ErrPlanNotFound):
		respondError(w, http.StatusNotFound, "plan not found")
		return
	case err != nil:
		respondError(w, http.StatusInternalServerError, "could not read the plan")
		return
	}
	// A plan of a cluster the caller may not read does not exist for them: a
	// 403 would confirm it does.
	if !ClusterScopeFrom(r.Context()).May(p.ClusterID) {
		respondError(w, http.StatusNotFound, "plan not found")
		return
	}
	respondJSON(w, http.StatusOK, h.planViewOf(r, *p, time.Now()))
}

// GET /plans?cluster=&status=draft,exported&limit=
func (h *handlers) handleListPlans(w http.ResponseWriter, r *http.Request) {
	store := kobiPlanStore()
	if store == nil {
		respondError(w, http.StatusServiceUnavailable, "plans are not enabled on this install")
		return
	}
	scope := ClusterScopeFrom(r.Context())
	if scope.EntitledToNothing() {
		respondJSON(w, http.StatusOK, map[string]any{"plans": []planSummary{}})
		return
	}
	limit := defaultPlansListed
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			respondError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, maxPlansListed)
	}
	var statuses []kobiplan.Status
	if v := r.URL.Query().Get("status"); v != "" {
		for _, s := range strings.Split(v, ",") {
			st := kobiplan.Status(strings.TrimSpace(s))
			if !knownPlanStatus(st) {
				respondError(w, http.StatusBadRequest, "unknown status "+string(st))
				return
			}
			statuses = append(statuses, st)
		}
	}
	plans, err := store.List(auth.ContextTenantID(r), copilot.PlanFilter{
		ClusterID: scope.Requested(), Statuses: statuses, Limit: limit,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not list plans")
		return
	}
	now := time.Now()
	name := h.clusterNames(r)
	author := h.authorNames(r)
	out := make([]planSummary, 0, len(plans))
	for _, p := range plans {
		if !scope.May(p.ClusterID) {
			continue
		}
		status, reason := effectiveStatus(p, now)
		out = append(out, planSummary{
			ID: p.ID, Title: p.Title, Kind: p.Kind, ClusterID: p.ClusterID, ClusterName: name(p.ClusterID),
			Version: p.Version, Status: p.Status, EffectiveStatus: status, EffectiveExpiredReason: reason,
			CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, CreatedBy: author(p.Source),
			Estimated: p.Impact.Estimated, StepCount: len(p.Steps), AppliedCount: appliedSteps(p.Steps),
			ExcludedCount: len(p.Excluded), MaxRisk: maxRisk(p.Steps), NodesRemoved: nodesRemoved(p.Steps),
		})
	}
	linkListed(out, plans, now)
	respondJSON(w, http.StatusOK, map[string]any{"plans": out})
}

// linkListed names, on each row, the related plans the same list holds: a
// consolidation plan's right-sizing plans, and a live consolidation plan on
// the right-sizing plans it depends on.
func linkListed(rows []planSummary, plans []kobiplan.Plan, now time.Time) {
	byID := map[string]kobiplan.Plan{}
	for _, p := range plans {
		byID[p.ID] = p
	}
	row := map[string]int{}
	for i, r := range rows {
		row[r.ID] = i
	}
	for i, r := range rows {
		p := byID[r.ID]
		for _, dep := range p.DependsOn {
			q, ok := byID[dep]
			if !ok {
				continue
			}
			rows[i].DependsOn = append(rows[i].DependsOn, planRefLite{ID: q.ID, Title: q.Title})
			if j, listed := row[dep]; listed && livePlan(p, now) {
				rows[j].LeadsTo = append(rows[j].LeadsTo, planRefLite{ID: p.ID, Title: p.Title})
			}
		}
	}
}

func knownPlanStatus(s kobiplan.Status) bool {
	switch s {
	case kobiplan.StatusDraft, kobiplan.StatusExported, kobiplan.StatusVerified, kobiplan.StatusExpired,
		kobiplan.StatusApproved, kobiplan.StatusExecuting, kobiplan.StatusFailed:
		return true
	}
	return false
}
