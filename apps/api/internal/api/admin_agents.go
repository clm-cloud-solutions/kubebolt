package api

import (
	"net/http"
)

// AdminAgentEntry is the wire shape returned by GET /admin/agents.
// Mirrors channel.AgentSummary verbatim — we wrap so the public API
// stays decoupled from the internal type's future evolution (e.g.
// adding agent-side fields the admin panel shouldn't see). camelCase
// for the JSON keys per the frontend convention.
type AdminAgentEntry struct {
	ClusterID string `json:"clusterId"`
	AgentID   string `json:"agentId"`
	NodeName  string `json:"nodeName"`
	TenantID  string `json:"tenantId,omitempty"`
	AuthMode  string `json:"authMode,omitempty"`
	// Connected is the unix-seconds timestamp the stream first
	// opened. The frontend renders "Xm ago" from this.
	ConnectedAt int64 `json:"connectedAt"`
}

// handleAdminListAgents returns the live agent registry — every
// gRPC stream currently open to this backend. Spec #09 V2 Item 5b —
// powers the /admin/ingest-activity panel's per-tenant heartbeat
// list. Admin-only via the route group middleware.
//
// Why not Prometheus: the registry carries NodeName + Connected
// timestamp which we don't push into label cardinality (would
// scale poorly in a fleet of hundreds of nodes). Reading
// directly from the in-memory registry is O(N) per request, fine
// for the admin-only call path where the page polls every 30s.
//
// Empty list when agentRegistry is nil (auth-disabled / no agents
// boot path) — no error, just an empty array.
func (h *handlers) handleAdminListAgents(w http.ResponseWriter, r *http.Request) {
	if h.agentRegistry == nil {
		respondJSON(w, http.StatusOK, []AdminAgentEntry{})
		return
	}
	summaries := h.agentRegistry.List()
	out := make([]AdminAgentEntry, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, AdminAgentEntry{
			ClusterID:   s.ClusterID,
			AgentID:     s.AgentID,
			NodeName:    s.NodeName,
			TenantID:    s.TenantID,
			AuthMode:    s.AuthMode,
			ConnectedAt: s.Connected.Unix(),
		})
	}
	respondJSON(w, http.StatusOK, out)
}
