package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// handleListIntegrations returns every registered integration with
// its current detected state against the active cluster. Read-only;
// safe for any authenticated role.
//
// Shape: []integrations.Integration — the UI renders one card per
// entry. Individual detection failures surface as StatusUnknown on
// that entry rather than failing the whole list, so a single broken
// adapter doesn't blank the page.
func (h *handlers) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	out := h.integrations.List(r.Context(), conn.Clientset())
	respondJSON(w, http.StatusOK, out)
}

// handleGetIntegration returns the detail for a single integration.
// Identical payload shape to the list endpoint — just scoped to one
// entry, and 404s on an unknown id.
func (h *handlers) handleGetIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	snap, err := provider.Detect(r.Context(), conn.Clientset())
	if err != nil {
		// Detection errors aren't HTTP errors — they're "we couldn't
		// tell" which is a state the UI needs to show. Mirror the
		// list-endpoint behavior: return Meta with StatusUnknown and
		// the error in Health.Message.
		meta := provider.Meta()
		meta.Status = integrations.StatusUnknown
		meta.Health = &integrations.Health{Message: err.Error()}
		respondJSON(w, http.StatusOK, meta)
		return
	}
	respondJSON(w, http.StatusOK, snap)
}
