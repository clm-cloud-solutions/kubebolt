package api

import "net/http"

// handleClusterProgress answers how far the request's cluster's connect has
// gone (cluster.ConnectProgress): what the "Preparing" view shows while a
// cluster's cache loads. It reads; it never starts or waits for a connect.
func (h *handlers) handleClusterProgress(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.manager.ConnectProgressFor(r.Context()))
}
