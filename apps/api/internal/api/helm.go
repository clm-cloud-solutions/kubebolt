package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
)

// handleListHelmReleases returns the latest revision of every Helm release
// the connected ServiceAccount can see (read-only, Sprint 4). Decodes Helm's
// storage Secrets directly — no helm SDK.
func (h *handlers) handleListHelmReleases(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	secrets, err := conn.ListHelmReleaseSecrets(r.Context())
	if err != nil {
		if apierrors.IsForbidden(err) {
			respondError(w, http.StatusForbidden, "not permitted to list Helm release secrets")
			return
		}
		respondError(w, http.StatusInternalServerError, "failed to list Helm releases")
		return
	}
	releases := helm.DecodeReleases(secrets)
	respondJSON(w, http.StatusOK, map[string]any{"items": releases, "total": len(releases)})
}

// handleGetHelmRelease returns the detail view (latest revision values +
// manifest + notes + dependencies, plus full revision history) for one
// release.
func (h *handlers) handleGetHelmRelease(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	secrets, err := conn.ListHelmReleaseSecrets(r.Context())
	if err != nil {
		if apierrors.IsForbidden(err) {
			respondError(w, http.StatusForbidden, "not permitted to list Helm release secrets")
			return
		}
		respondError(w, http.StatusInternalServerError, "failed to read Helm release")
		return
	}
	detail, err := helm.DecodeReleaseDetail(namespace, name, secrets)
	if err != nil {
		respondError(w, http.StatusNotFound, "Helm release not found")
		return
	}
	respondJSON(w, http.StatusOK, detail)
}
