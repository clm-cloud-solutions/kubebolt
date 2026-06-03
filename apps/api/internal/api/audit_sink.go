package api

import (
	"net/http"
	"strconv"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
)

// auditStore is the durable mutation-audit sink (Sprint 1). nil until
// SetAuditStore wires it (only when the BoltDB store is available, i.e.
// auth enabled), in which case auditMutation persists every mutation in
// addition to the slog line. Package-level rather than a handlers field
// because auditMutation has 56 call sites as a free function — a sink var
// avoids churning every one.
var (
	auditStore     audit.Store
	auditClusterID func() string
)

// SetAuditStore wires the durable audit store + a resolver for the active
// cluster id (stamped onto each record). Call once at boot, after the
// router is built. Safe to call with a nil store (audit stays slog-only).
func SetAuditStore(s audit.Store, clusterIDFn func() string) {
	auditStore = s
	auditClusterID = clusterIDFn
}

// handleListActions returns the durable action-history (newest first) for
// the admin action-audit view. Admin-only (gated in the router).
func (h *handlers) handleListActions(w http.ResponseWriter, r *http.Request) {
	if auditStore == nil {
		respondJSON(w, http.StatusOK, map[string]any{"items": []any{}, "total": 0})
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	recs, err := auditStore.List(limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read action history")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": recs, "total": len(recs)})
}
