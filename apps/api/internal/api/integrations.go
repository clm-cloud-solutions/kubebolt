package api

import (
	"encoding/json"
	"errors"
	"io"
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

// handleInstallIntegration applies the manifests an integration
// needs to function against the active cluster. Admin-only — the
// router guards that. Returns:
//
//   200 OK   + the post-install detection snapshot
//   400 BadRequest  if the config payload is invalid (e.g. missing backendUrl)
//   404 NotFound    if the integration id isn't registered
//   405 MethodNotAllowed if the integration doesn't implement Installable
//   409 Conflict    if a resource exists but wasn't put there by us
//   500 ...         on any other error
func (h *handlers) handleInstallIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	installable, ok := provider.(integrations.Installable)
	if !ok {
		respondError(w, http.StatusMethodNotAllowed, "integration does not support install")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	// Accept an empty body as "install with all defaults". Real
	// requests will include at least backendUrl, but we validate
	// that in the provider so the error message matches the
	// provider's own field names.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var raw json.RawMessage
	if len(body) > 0 {
		raw = json.RawMessage(body)
	}

	if err := installable.Install(r.Context(), conn.Clientset(), raw); err != nil {
		var conflict *integrations.ConflictError
		if errors.As(err, &conflict) {
			respondJSON(w, http.StatusConflict, map[string]interface{}{
				"error":     conflict.Error(),
				"conflict":  conflict,
			})
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return the fresh state so the UI can update its card without
	// a follow-up GET.
	snap, _ := provider.Detect(r.Context(), conn.Clientset())
	respondJSON(w, http.StatusOK, snap)
}

// handleUninstallIntegration removes the resources an integration
// owns. Only touches resources labeled as managed-by=kubebolt —
// external Helm installs are left alone.
func (h *handlers) handleUninstallIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	provider, ok := h.integrations.Get(id)
	if !ok {
		respondError(w, http.StatusNotFound, "integration not found")
		return
	}
	installable, ok := provider.(integrations.Installable)
	if !ok {
		respondError(w, http.StatusMethodNotAllowed, "integration does not support uninstall")
		return
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	if err := installable.Uninstall(r.Context(), conn.Clientset()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snap, _ := provider.Detect(r.Context(), conn.Clientset())
	respondJSON(w, http.StatusOK, snap)
}

