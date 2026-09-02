package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// Fase 2 read side (PR 2.2): the episode history the DIPRES escalation had
// nobody to ask. Lives OUTSIDE requireConnector on purpose — history must
// answer for clusters that are DEAD (that is precisely when the question
// arrives), so no live connector is required.

// handleListEpisodes — GET /insights/episodes
// Window semantics (the query that was missing): episodes that OVERLAP
// [since, until]. Defaults: the last 24h. ?cluster= overrides the resolved
// request cluster; ?cluster=all widens to the whole org.
func (h *handlers) handleListEpisodes(w http.ResponseWriter, r *http.Request) {
	if h.episodes == nil {
		respondError(w, http.StatusServiceUnavailable, "episode history is not enabled on this install")
		return
	}
	org := auth.ContextTenantID(r)
	q := insights.EpisodeQuery{
		ClusterID: cluster.RuntimeKeyFromContext(r.Context()).Cluster,
		Status:    r.URL.Query().Get("status"),
		Severity:  r.URL.Query().Get("severity"),
		RuleID:    r.URL.Query().Get("rule"),
	}
	if c := r.URL.Query().Get("cluster"); c != "" {
		if c == "all" {
			q.ClusterID = ""
		} else {
			q.ClusterID = h.manager.CanonicalClusterID(r.Context(), c)
		}
	} else if q.ClusterID != "" {
		// The header carries a context NAME for direct clusters; episodes key
		// on the kube-system UID. Same canonicalization the Autopilot proxy
		// applies.
		q.ClusterID = h.manager.CanonicalClusterID(r.Context(), q.ClusterID)
	}

	now := time.Now().UTC()
	q.Since = now.Add(-24 * time.Hour)
	q.Until = now
	var err error
	if raw := r.URL.Query().Get("since"); raw != "" {
		if q.Since, err = time.Parse(time.RFC3339, raw); err != nil {
			respondError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
	}
	if raw := r.URL.Query().Get("until"); raw != "" {
		if q.Until, err = time.Parse(time.RFC3339, raw); err != nil {
			respondError(w, http.StatusBadRequest, "until must be RFC3339")
			return
		}
	}
	// Both conversions are bounded before narrowing to int32: the store clamps
	// the limit to 200 anyway, and a page beyond 100 000 × limit is not a
	// window anyone reads — an unbounded Atoi → int32 is a wraparound waiting
	// for a crafted query string (CodeQL go/incorrect-integer-conversion).
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			q.Limit = int32(n)
		}
	}
	if raw := r.URL.Query().Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 1 && n <= 100000 {
			limit := q.Limit
			if limit <= 0 {
				limit = 50
			}
			q.Offset = int32(n-1) * limit
		}
	}

	eps, err := h.episodes.Window(r.Context(), org, q)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read episode history")
		return
	}
	h.enrichEpisodeClusterNames(r, eps)
	respondJSON(w, http.StatusOK, map[string]any{
		"episodes": eps,
		"window":   map[string]string{"since": q.Since.Format(time.RFC3339), "until": q.Until.Format(time.RFC3339)},
	})
}

// handleGetEpisode — GET /insights/episodes/{id}: the episode, its
// append-only transitions and its recurrence (same fingerprint).
func (h *handlers) handleGetEpisode(w http.ResponseWriter, r *http.Request) {
	if h.episodes == nil {
		respondError(w, http.StatusServiceUnavailable, "episode history is not enabled on this install")
		return
	}
	org := auth.ContextTenantID(r)
	id := chi.URLParam(r, "id")

	ep, transitions, err := h.episodes.Episode(r.Context(), org, id)
	if err != nil {
		respondError(w, http.StatusNotFound, "episode not found")
		return
	}
	recurrence, err := h.episodes.ByFingerprint(r.Context(), org, ep.Fingerprint, 8)
	if err != nil {
		recurrence = nil // best-effort side card
	}
	one := []insights.Episode{ep}
	h.enrichEpisodeClusterNames(r, one)
	ep = one[0]
	h.enrichEpisodeClusterNames(r, recurrence)

	resp := map[string]any{
		"episode":     ep,
		"transitions": transitions,
		"recurrence":  recurrence,
	}
	// The Enterprise build adds `capabilityChanges` here — the org's capability
	// transitions (Autopilot off, notifications discarded, caps exceeded) that
	// overlapped the episode. OSS has no capability registry; the detail page
	// only paints the card when the field arrives.
	respondJSON(w, http.StatusOK, resp)
}

// enrichEpisodeClusterNames stamps each episode's persisted display name —
// resolvable even for clusters no longer in the fleet, which is exactly
// where the expired ghosts live. Cached per request per unique cluster.
func (h *handlers) enrichEpisodeClusterNames(r *http.Request, eps []insights.Episode) {
	if len(eps) == 0 {
		return
	}
	names := map[string]string{}
	for i := range eps {
		cid := eps[i].ClusterID
		if cid == "" {
			continue
		}
		name, seen := names[cid]
		if !seen {
			name = h.manager.DisplayNameForCluster(r.Context(), cid)
			names[cid] = name
		}
		eps[i].ClusterName = name
	}
}
