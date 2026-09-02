package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Fase 3 (PR 3.1): presence beacon + mutes CRUD (#54).

// handleMarkDashboardSeen — POST /account/dashboard-seen
// The frontend fires this when Home renders. It is a beacon, not a query:
// on installs without the store (OSS / no DB) it 204s and does nothing, so
// the client never needs to know whether presence exists here.
func (h *handlers) handleMarkDashboardSeen(w http.ResponseWriter, r *http.Request) {
	if h.presence != nil {
		org := auth.ContextTenantID(r)
		user := auth.ContextUserID(r)
		if org != "" && user != "" {
			if err := h.presence.MarkDashboardRendered(r.Context(), org, user, time.Now().UTC()); err != nil {
				respondError(w, http.StatusInternalServerError, "could not record presence")
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListMutes — GET /insights/mutes?cluster=<uid|all>
// Default scope mirrors the episodes endpoint: the resolved request cluster;
// ?cluster=all widens to the org (the Silenciados tab reads it that way).
func (h *handlers) handleListMutes(w http.ResponseWriter, r *http.Request) {
	if h.mutes == nil {
		respondError(w, http.StatusServiceUnavailable, "mutes are not enabled on this install")
		return
	}
	org := auth.ContextTenantID(r)
	clusterID := cluster.RuntimeKeyFromContext(r.Context()).Cluster
	if c := r.URL.Query().Get("cluster"); c != "" {
		if c == "all" {
			clusterID = ""
		} else {
			clusterID = h.manager.CanonicalClusterID(r.Context(), c)
		}
	} else if clusterID != "" {
		clusterID = h.manager.CanonicalClusterID(r.Context(), clusterID)
	}
	mutes, err := h.mutes.ListMutes(r.Context(), org, clusterID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not list mutes")
		return
	}
	// Names, not UUIDs (in-vivo 01-sep): resolve each unique cluster once —
	// same source the episode history uses, dead clusters included.
	if h.manager != nil {
		names := map[string]string{}
		for i := range mutes {
			uid := mutes[i].ClusterID
			if _, done := names[uid]; !done {
				names[uid] = h.manager.DisplayNameForCluster(r.Context(), uid)
			}
			mutes[i].ClusterName = names[uid]
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"mutes": mutes})
}

// handleCreateMute — POST /insights/mutes
// Body: {clusterId?, ruleId, resource, reason?, expiresAt?, untilResolved?}.
// clusterId empty resolves to the request's cluster — the card action doesn't
// have to know the UID it lives on.
func (h *handlers) handleCreateMute(w http.ResponseWriter, r *http.Request) {
	if h.mutes == nil {
		respondError(w, http.StatusServiceUnavailable, "mutes are not enabled on this install")
		return
	}
	var m insights.Mute
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if m.ClusterID == "" {
		m.ClusterID = h.manager.CanonicalClusterID(r.Context(), cluster.RuntimeKeyFromContext(r.Context()).Cluster)
	} else {
		m.ClusterID = h.manager.CanonicalClusterID(r.Context(), m.ClusterID)
	}
	m.CreatedBy = muteActor(r)
	if err := insights.ValidateMute(m); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := h.mutes.CreateMute(r.Context(), auth.ContextTenantID(r), m)
	// Explicit audit WITH the what and the why (#54: quién, cuándo, por qué,
	// hasta cuándo) — the generic route middleware only recorded THAT a mute
	// was created, which cannot answer "what was silenced?" later. Nothing
	// here is a secret, so the detail belongs on the record.
	params := map[string]any{
		"cluster": m.ClusterID, "rule": m.RuleID, "resource": m.Resource,
		"untilResolved": m.UntilResolved, "reason": m.Reason,
	}
	if m.ExpiresAt != nil {
		params["expiresAt"] = m.ExpiresAt.UTC().Format(time.RFC3339)
	}
	auditMutation(r, "mute", "insight-mute", "", m.RuleID+" "+m.Resource, params, err)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not create mute")
		return
	}
	respondJSON(w, http.StatusCreated, created)
}

// muteActor is the readable name stamped on the mute and its timeline
// entries — username when available, id as fallback.
func muteActor(r *http.Request) string {
	if claims := auth.ContextClaims(r); claims != nil && claims.Username != "" {
		return claims.Username
	}
	return auth.ContextUserID(r)
}

// applyMuteFilter drops muted, non-pierced insights (#54): the silence is a
// DISPLAY overlay, so it must apply wherever insights are shown or counted —
// the first in-vivo pass only covered the Insights list and the Overview KPI
// kept counting the silenced ones. Critical severity pierces (§5).
func applyMuteFilter(items []models.Insight, mutes []insights.Mute) []models.Insight {
	if len(mutes) == 0 {
		return items
	}
	idx := make(map[string]bool, len(mutes))
	for _, m := range mutes {
		idx[m.RuleID+"\x00"+m.Resource] = true
	}
	out := make([]models.Insight, 0, len(items))
	for _, it := range items {
		if idx[it.RuleID+"\x00"+it.Resource] && it.Severity != "critical" {
			continue
		}
		out = append(out, it)
	}
	return out
}

// visibleInsights applies the org's mutes for the request's cluster.
// Nil-safe (OSS: no store → unchanged) and fail-open on read errors — the
// overlay must never break a read path.
func (h *handlers) visibleInsights(r *http.Request, items []models.Insight) []models.Insight {
	if h.mutes == nil || len(items) == 0 {
		return items
	}
	clusterID := cluster.RuntimeKeyFromContext(r.Context()).Cluster
	if h.manager != nil {
		clusterID = h.manager.CanonicalClusterID(r.Context(), clusterID)
	}
	mutes, err := h.mutes.ListMutes(r.Context(), auth.ContextTenantID(r), clusterID)
	if err != nil {
		return items
	}
	return applyMuteFilter(items, mutes)
}

// handleOperationalEpisodes — GET /insights/operational-episodes?since=&until=
// Fase 3 (PR 3.2): the org's bursts, recomputed on demand over the window
// (deterministic ids — recomputing converges) and persisted for the shift
// report. Defaults to the last 24h. Cross-cluster by design (§3.3): the
// Dipres case spanned two clusters and the report lives at org level.
func (h *handlers) handleOperationalEpisodes(w http.ResponseWriter, r *http.Request) {
	if h.operational == nil {
		respondError(w, http.StatusServiceUnavailable, "operational episodes are not enabled on this install")
		return
	}
	now := time.Now().UTC()
	since, until := now.Add(-24*time.Hour), now
	var err error
	if raw := r.URL.Query().Get("since"); raw != "" {
		if since, err = time.Parse(time.RFC3339, raw); err != nil {
			respondError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
	}
	if raw := r.URL.Query().Get("until"); raw != "" {
		if until, err = time.Parse(time.RFC3339, raw); err != nil {
			respondError(w, http.StatusBadRequest, "until must be RFC3339")
			return
		}
	}
	ops, err := h.operational.ClusterAndStore(r.Context(), auth.ContextTenantID(r), since, until)
	if err != nil {
		// The 500 body stays generic; the log carries the cause (the first
		// in-vivo failure was invisible precisely because it didn't).
		slog.Error("operational episodes: cluster-and-store failed",
			slog.String("org", auth.ContextTenantID(r)), slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "could not compute operational episodes")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"episodes": ops})
}

// handleDeleteMute — DELETE /insights/mutes/{id}
func (h *handlers) handleDeleteMute(w http.ResponseWriter, r *http.Request) {
	if h.mutes == nil {
		respondError(w, http.StatusServiceUnavailable, "mutes are not enabled on this install")
		return
	}
	id := chi.URLParam(r, "id")
	err := h.mutes.DeleteMute(r.Context(), auth.ContextTenantID(r), id, muteActor(r))
	auditMutation(r, "unmute", "insight-mute", "", id, nil, err)
	if errors.Is(err, insights.ErrMuteNotFound) {
		respondError(w, http.StatusNotFound, "mute not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not delete mute")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
