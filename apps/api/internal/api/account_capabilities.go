package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// GET /account/capabilities — the OSS source of the capability states (#50).
//
// The Enterprise build keeps a per-org capability registry (plan caps, AI
// credits, Autopilot, notification routing, subscription linkage) evaluated
// on a ticker and persisted with its history. OSS has none of those axes —
// but it does have the one cap that silently changes what every dashboard
// panel can see: the active-series ceiling the ingest gate enforces. This
// endpoint reports that single row, live from the cardinality tracker, so the
// truncation banner on the Overview renders identically in both editions.
//
// The wire shape mirrors the EE registry's State. `since` is the moment the
// current status began, kept in memory: a restart forgets it, which reads as
// "since just now" — acceptable for a banner, and honest about what an OSS
// install records.

type capabilityStateResponse struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Reason   string         `json:"reason"`
	Detail   map[string]any `json:"detail,omitempty"`
	Since    time.Time      `json:"since"`
	Audience string         `json:"audience"`
}

// capabilityWarnPercent is the "near" threshold, the same 80 % the EE
// evaluator uses by default.
const capabilityWarnPercent = 80

type capabilitySince struct {
	mu    sync.Mutex
	state map[string]string
	since map[string]time.Time
}

var capSince = &capabilitySince{state: map[string]string{}, since: map[string]time.Time{}}

// mark records a status change and returns when the current status began.
func (c *capabilitySince) mark(id, status string, now time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state[id] != status {
		c.state[id] = status
		c.since[id] = now
	}
	return c.since[id]
}

func (h *handlers) handleAccountCapabilities(w http.ResponseWriter, r *http.Request) {
	out := []capabilityStateResponse{}
	if h.promCardinality != nil {
		tenant := auth.ContextTenantID(r)
		if s, ok := activeSeriesCapability(h.promCardinality, tenant, time.Now().UTC()); ok {
			out = append(out, s)
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"capabilities": out})
}

// activeSeriesCapability derives the active_series row from the tracker.
// Nothing is reported until the tracker has a fresh count and a positive cap
// — a boot window or a disabled gate is the absence of a state, not a state.
func activeSeriesCapability(t *CardinalityTracker, tenant string, now time.Time) (capabilityStateResponse, bool) {
	count, fresh := t.CurrentCount(tenant)
	max := t.Cap(tenant, nil)
	if !fresh || max <= 0 {
		return capabilityStateResponse{}, false
	}
	s := capabilityStateResponse{
		ID: "active_series", Status: "ok", Audience: "customer",
		Detail: map[string]any{"count": count, "max": max},
	}
	switch {
	case count >= max:
		// OSS caps are hard: the ingest gate refuses samples past the ceiling.
		s.Status = "degraded"
		s.Reason = "active-series cap engaged — new series are being dropped"
	case count*100 >= max*capabilityWarnPercent:
		s.Status = "near"
		s.Reason = "approaching the active-series cap — spikes may lose data"
	}
	s.Since = capSince.mark(s.ID, s.Status, now)
	return s, true
}
