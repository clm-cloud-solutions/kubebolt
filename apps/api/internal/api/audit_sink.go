package api

import (
	"net/http"
	"strconv"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// auditStore is this package's handle on the durable trail, kept for the read
// path (handleListActions). The WRITE path goes through audit.Emit, because the
// administrative plane in package auth has to reach the same sink and cannot
// import this package — see audit/sink.go.
var auditStore audit.Store

// SetAuditStore wires the durable audit store + a resolver for the active
// cluster id (stamped onto each record). Call once at boot, after the
// router is built. Safe to call with a nil store (audit stays slog-only).
func SetAuditStore(s audit.Store, clusterIDFn func() string) {
	auditStore = s
	audit.SetSink(s, clusterIDFn)
}

// handleListActions returns the durable action-history (newest first) for
// the admin action-audit view. Admin-only (gated in the router) — but that is
// the ORG admin role, not a platform one, so the org scope below is what keeps
// one tenant's admin out of another's history. It is not defence in depth; it
// is the only thing standing there.
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
	// class filters mutation vs access. Default "all" keeps the existing
	// behaviour, but the filter has to exist: access events (terminals, tunnels,
	// file reads) are far more frequent than mutations, so without a way to
	// separate them a busy day's browsing would push every mutation off the
	// first page — turning a trail that got MORE complete into one that reads as
	// less useful.
	class := r.URL.Query().Get("class")

	recs, err := auditStore.ListOrg(auth.ContextTenantID(r), limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read action history")
		return
	}

	out := make([]audit.Record, 0, len(recs))
	for _, rec := range recs {
		// Normalized on read so the client always receives a class, including
		// for records written before the field existed.
		rec.Class = audit.ClassOf(&rec)
		if class != "" && class != "all" && rec.Class != class {
			continue
		}
		out = append(out, rec)
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}
