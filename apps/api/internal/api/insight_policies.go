package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// InsightPolicyService wires the rule-policy store (#44 step 1) into the
// router. nil on installs without Postgres (OSS) — the endpoints 503.
type InsightPolicyService struct {
	Store interface {
		List(ctx context.Context, org string) ([]insights.StoredRulePolicy, error)
		Upsert(ctx context.Context, org string, p insights.StoredRulePolicy) error
		Delete(ctx context.Context, org, ruleID, category string) error
	}
	// Invalidate drops the org's cached snapshot so the next engine
	// evaluation (≤30s tick) picks the change up.
	Invalidate func(org string)
}

// insightPolicyView is one rule in the GET response: shipped defaults +
// the org's global override, ready for the (future, Fase 4) matrix UI and
// usable today via the API for the global knob.
type insightPolicyView struct {
	ID    string `json:"id"`
	Class string `json:"class"`
	// Name (from the rule catalog) + one-sentence Description — the hub
	// shows them under the id so nobody has to know all 24 rules by heart.
	Name             string   `json:"name,omitempty"`
	Description      string   `json:"description,omitempty"`
	DefaultSeverity  string   `json:"defaultSeverity"`
	HasThreshold     bool     `json:"hasThreshold"`
	DefaultThreshold *float64 `json:"defaultThreshold,omitempty"`
	ThresholdLabel   string   `json:"thresholdLabel,omitempty"`
	// Global override (category "global"), when the org set one.
	Threshold *float64 `json:"threshold,omitempty"`
	Severity  *string  `json:"severity,omitempty"`
	UpdatedBy string   `json:"updatedBy,omitempty"`
	// Categories: the env-layer overrides (Fase 4), category → knobs.
	// Effective per cluster = category > global > default, knob by knob.
	Categories map[string]insightPolicyOverride `json:"categories,omitempty"`
	// Ignored30d: episodes of this rule closed in the last 30d without ack,
	// action or mute — the honesty column (#44).
	Ignored30d *insightIgnoredStat `json:"ignored30d,omitempty"`
}

type insightPolicyOverride struct {
	Threshold *float64 `json:"threshold,omitempty"`
	Severity  *string  `json:"severity,omitempty"`
	UpdatedBy string   `json:"updatedBy,omitempty"`
}

// insightIgnoredStat — «Ignorados 30d» (#44): closed without a human ack,
// an action, or a mute. The column that keeps the tuning honest.
type insightIgnoredStat struct {
	Ignored int64 `json:"ignored"`
	Total   int64 `json:"total"`
}

// handleListInsightPolicies — GET /admin/insight-policies
func (h *handlers) handleListInsightPolicies(w http.ResponseWriter, r *http.Request) {
	if h.insightPolicies == nil || h.insightPolicies.Store == nil {
		respondError(w, http.StatusServiceUnavailable, "insight policies are not enabled on this install")
		return
	}
	org := auth.ContextTenantID(r)
	stored, err := h.insightPolicies.Store.List(r.Context(), org)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read rule policies")
		return
	}
	overrides := map[string]insights.StoredRulePolicy{}
	byCategory := map[string]map[string]insightPolicyOverride{}
	for _, p := range stored {
		if p.Category == insights.PolicyCategoryGlobal {
			overrides[p.RuleID] = p
			continue
		}
		// Fase 4: the env-category layers, keyed rule → category → knobs,
		// ready for the matrix UI.
		if byCategory[p.RuleID] == nil {
			byCategory[p.RuleID] = map[string]insightPolicyOverride{}
		}
		byCategory[p.RuleID][p.Category] = insightPolicyOverride{
			Threshold: p.Threshold, Severity: p.Severity, UpdatedBy: p.UpdatedBy,
		}
	}
	ruleNames := map[string]string{}
	for _, rl := range insights.AllRules() {
		ruleNames[rl.ID] = rl.Name
	}
	out := make([]insightPolicyView, 0, len(insights.PolicyCatalog))
	for id, def := range insights.PolicyCatalog {
		v := insightPolicyView{
			ID:              id,
			Class:           string(def.Class),
			Name:            ruleNames[id],
			Description:     insights.PolicyDescriptions[id],
			DefaultSeverity: def.Severity,
			HasThreshold:    def.HasThreshold,
			ThresholdLabel:  def.ThresholdLabel,
			Categories:      byCategory[id],
		}
		if def.HasThreshold {
			t := def.Threshold
			v.DefaultThreshold = &t
		}
		if ov, ok := overrides[id]; ok {
			v.Threshold = ov.Threshold
			v.Severity = ov.Severity
			v.UpdatedBy = ov.UpdatedBy
		}
		out = append(out, v)
	}
	// «Ignorados 30d» from the episode lifecycle (Fase 2). Best-effort:
	// installs without the episode store just omit the column.
	if h.episodes != nil {
		if rates, err := h.episodes.IgnoredRate(r.Context(), org, 30*24*time.Hour); err == nil {
			for i := range out {
				if rt, ok := rates[out[i].ID]; ok {
					out[i].Ignored30d = &insightIgnoredStat{Ignored: rt[0], Total: rt[1]}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class // expectation < malfunction (alphabetical)
		}
		return out[i].ID < out[j].ID
	})
	respondJSON(w, http.StatusOK, map[string]any{"rules": out})
}

type insightPolicyPatch struct {
	Threshold *float64 `json:"threshold,omitempty"`
	Severity  *string  `json:"severity,omitempty"`
	// Category: "global" (default) or one of the four closed env categories
	// (Fase 4). A category override layers ON TOP of the global one for the
	// clusters classified into it.
	Category string `json:"category,omitempty"`
}

// handlePutInsightPolicy — PUT /admin/insight-policies/{rule}
// The class contract is enforced by insights.ValidatePolicyChange:
// malfunctions move only their bar; expectations move only their severity
// (off allowed). The same contract applies to every category layer.
func (h *handlers) handlePutInsightPolicy(w http.ResponseWriter, r *http.Request) {
	if h.insightPolicies == nil || h.insightPolicies.Store == nil {
		respondError(w, http.StatusServiceUnavailable, "insight policies are not enabled on this install")
		return
	}
	ruleID := chi.URLParam(r, "rule")
	var patch insightPolicyPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := insights.ValidatePolicyChange(ruleID, patch.Threshold, patch.Severity); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if patch.Category == "" {
		patch.Category = insights.PolicyCategoryGlobal
	}
	if err := insights.ValidatePolicyCategory(patch.Category); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	org := auth.ContextTenantID(r)
	def := insights.PolicyCatalog[ruleID]
	rec := insights.StoredRulePolicy{
		RuleID:    ruleID,
		Class:     def.Class,
		Category:  patch.Category,
		Threshold: patch.Threshold,
		Severity:  patch.Severity,
		UpdatedBy: auth.ContextUserID(r),
	}
	if err := h.insightPolicies.Store.Upsert(r.Context(), org, rec); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store rule policy")
		return
	}
	if h.insightPolicies.Invalidate != nil {
		h.insightPolicies.Invalidate(org)
	}
	respondJSON(w, http.StatusOK, rec)
}

// handleDeleteInsightPolicy — DELETE /admin/insight-policies/{rule}
// ?category=<layer> (default global): back to the layer below — deleting a
// category override falls back to global, deleting global to shipped.
func (h *handlers) handleDeleteInsightPolicy(w http.ResponseWriter, r *http.Request) {
	if h.insightPolicies == nil || h.insightPolicies.Store == nil {
		respondError(w, http.StatusServiceUnavailable, "insight policies are not enabled on this install")
		return
	}
	ruleID := chi.URLParam(r, "rule")
	if _, ok := insights.PolicyCatalog[ruleID]; !ok {
		respondError(w, http.StatusNotFound, "unknown rule")
		return
	}
	category := r.URL.Query().Get("category")
	if category == "" {
		category = insights.PolicyCategoryGlobal
	}
	if err := insights.ValidatePolicyCategory(category); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	org := auth.ContextTenantID(r)
	if err := h.insightPolicies.Store.Delete(r.Context(), org, ruleID, category); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete rule policy")
		return
	}
	if h.insightPolicies.Invalidate != nil {
		h.insightPolicies.Invalidate(org)
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "reset", "rule": ruleID})
}
