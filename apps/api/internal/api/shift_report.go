package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// Fase 3 (PR 3.3): GET /insights/shift-report — «mientras no estabas» as
// data. The window hangs from the requesting USER's presence anchor
// (user_dashboard_seen); the frontend renders the narrative and moves the
// anchor AFTERWARDS via the beacon, so reading the report never shrinks it.

// shiftWindowMax bounds the window: an anchor older than this clamps and the
// report says so (CoverageNote). Matches the UI's widest history range —
// older activity may also have aged out of the plan's retention.
const shiftWindowMax = 30 * 24 * time.Hour

// shiftDefaultWindow is the first-shift window (no anchor yet).
const shiftDefaultWindow = 24 * time.Hour

type shiftReportResponse struct {
	WindowFrom time.Time `json:"windowFrom"`
	WindowTo   time.Time `json:"windowTo"`
	FirstShift bool      `json:"firstShift"`
	Truncated  bool      `json:"truncated"`

	Bursts   []insights.OperationalEpisode `json:"bursts"`
	Episodes insights.ShiftEpisodeStats    `json:"episodes"`
	Worst    *insights.ShiftWorstEpisode   `json:"worst,omitempty"`
	Mutes    insights.ShiftMuteStats       `json:"mutes"`
	// RulesOff: rules the org's policy layer has switched off — the report
	// counts what the profile hides, it never pretends it didn't happen.
	RulesOff int `json:"rulesOff"`
	// Capabilities: current NON-ok capability states — the «Autopilot did not
	// intervene: not enabled» lines of the Enterprise report. OSS has no
	// capability registry, so the list is always empty and the count zero;
	// the field keeps its shape so the card renders identically.
	Capabilities      []capabilityState `json:"capabilities"`
	CapabilityChanges int               `json:"capabilityChanges"`
	// ClusterNames resolves the burst clusters' UIDs to display names (the
	// narrative says «gke-orquestador», not a UUID). Survives dead clusters
	// — same source the episode history uses.
	ClusterNames map[string]string `json:"clusterNames,omitempty"`
}

// shiftWindow resolves the report window from the presence anchor. Pure, so
// the clamp rules are testable: no anchor → first shift (24h); anchor beyond
// 30d → clamped and flagged.
func shiftWindow(anchor time.Time, hasAnchor bool, now time.Time) (from time.Time, firstShift, truncated bool) {
	switch {
	case !hasAnchor:
		return now.Add(-shiftDefaultWindow), true, false
	case now.Sub(anchor) > shiftWindowMax:
		return now.Add(-shiftWindowMax), false, true
	default:
		return anchor, false, false
	}
}

func (h *handlers) handleShiftReport(w http.ResponseWriter, r *http.Request) {
	if h.shiftStats == nil || h.presence == nil {
		respondError(w, http.StatusServiceUnavailable, "the shift report is not enabled on this install")
		return
	}
	org := auth.ContextTenantID(r)
	user := auth.ContextUserID(r)
	now := time.Now().UTC()

	anchor, hasAnchor, err := h.presence.DashboardLastSeen(r.Context(), org, user)
	if err != nil {
		slog.Error("shift report: presence read failed", slog.String("org", org), slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "could not read presence")
		return
	}
	from, firstShift, truncated := shiftWindow(anchor, hasAnchor, now)

	resp := shiftReportResponse{
		WindowFrom: from, WindowTo: now,
		FirstShift: firstShift, Truncated: truncated,
		Bursts: []insights.OperationalEpisode{},
	}

	if h.operational != nil {
		if bursts, err := h.operational.ClusterAndStore(r.Context(), org, from, now); err != nil {
			slog.Error("shift report: burst clustering failed", slog.String("org", org), slog.String("error", err.Error()))
		} else {
			// Only bursts that STARTED during the absence: the clusterer's
			// overlap fetch also returns ongoing bursts born before the
			// window, and re-narrating those every visit is exactly the
			// standing-state noise this report must not be (in-vivo 31-ago).
			for _, b := range bursts {
				if !b.WindowFrom.Before(from) {
					resp.Bursts = append(resp.Bursts, b)
				}
			}
		}
	}
	if resp.Episodes, err = h.shiftStats.WindowStats(r.Context(), org, from, now); err != nil {
		slog.Error("shift report: window stats failed", slog.String("org", org), slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "could not compute the shift report")
		return
	}
	if resp.Worst, err = h.shiftStats.WorstEpisode(r.Context(), org, from, now); err != nil {
		slog.Error("shift report: worst episode failed", slog.String("org", org), slog.String("error", err.Error()))
	}
	if resp.Mutes, err = h.shiftStats.MuteStats(r.Context(), org, from); err != nil {
		slog.Error("shift report: mute stats failed", slog.String("org", org), slog.String("error", err.Error()))
	}
	resp.RulesOff = h.countRulesOff(r.Context(), org)
	resp.Capabilities, resp.CapabilityChanges = []capabilityState{}, 0

	// Names for the narrative. Best-effort per unique UID across bursts +
	// the worst episode's cluster (already inside a burst's set or absent).
	if h.manager != nil {
		names := map[string]string{}
		for _, b := range resp.Bursts {
			for _, uid := range b.Clusters {
				if _, done := names[uid]; !done {
					if n := h.manager.DisplayNameForCluster(r.Context(), uid); n != "" {
						names[uid] = n
					}
				}
			}
		}
		if len(names) > 0 {
			resp.ClusterNames = names
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

// countRulesOff counts rules the org's policy layer switched off (severity
// "off"). Nil-safe: no policy service → 0.
func (h *handlers) countRulesOff(ctx context.Context, org string) int {
	if h.insightPolicies == nil || h.insightPolicies.Store == nil {
		return 0
	}
	policies, err := h.insightPolicies.Store.List(ctx, org)
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range policies {
		if p.Severity != nil && *p.Severity == insights.SeverityOff {
			n++
		}
	}
	return n
}

// capabilityState mirrors the wire shape of the Enterprise capability
// registry (#50) so the shift report's JSON is identical across editions.
// OSS never populates it.
type capabilityState struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Reason   string         `json:"reason"`
	Detail   map[string]any `json:"detail,omitempty"`
	Since    time.Time      `json:"since"`
	Audience string         `json:"audience"`
}
