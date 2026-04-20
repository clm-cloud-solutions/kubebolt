package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// --- POST /clusters — upload a kubeconfig ---

type addClusterRequest struct {
	Kubeconfig string `json:"kubeconfig"` // raw YAML string (alternative: use raw body with content-type application/yaml)
}

type addClusterResponse struct {
	Added []string `json:"added"` // context names that were added
}

// handleAddCluster accepts a kubeconfig and persists its contexts.
// Accepts either:
//   - Content-Type: application/json with {"kubeconfig": "<yaml-content>"}
//   - Content-Type: application/yaml with the raw YAML as body
func (h *handlers) handleAddCluster(w http.ResponseWriter, r *http.Request) {
	var rawYAML []byte

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/yaml") ||
		strings.HasPrefix(contentType, "text/yaml") ||
		strings.HasPrefix(contentType, "application/x-yaml") {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			respondError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		rawYAML = body
	} else {
		var req addClusterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Kubeconfig == "" {
			respondError(w, http.StatusBadRequest, "kubeconfig field is required")
			return
		}
		rawYAML = []byte(req.Kubeconfig)
	}

	// Size limit to prevent abuse (kubeconfigs are usually a few KB at most)
	if len(rawYAML) > 1024*1024 { // 1MB
		respondError(w, http.StatusBadRequest, "kubeconfig is too large (max 1MB)")
		return
	}

	// Track which admin uploaded this
	claims := auth.ContextClaims(r)
	uploadedBy := ""
	if claims != nil {
		uploadedBy = claims.Username
	}

	added, err := h.manager.AddKubeconfig(rawYAML, uploadedBy)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Notify clients that the cluster list has changed
	if h.wsHub != nil {
		h.wsHub.Broadcast("clusters.changed", nil)
	}

	respondJSON(w, http.StatusCreated, addClusterResponse{Added: added})
}

// --- DELETE /clusters/:context — remove an uploaded cluster ---

func (h *handlers) handleDeleteCluster(w http.ResponseWriter, r *http.Request) {
	contextName := chi.URLParam(r, "context")
	if contextName == "" {
		respondError(w, http.StatusBadRequest, "context name is required")
		return
	}

	if err := h.manager.RemoveUploadedContext(contextName); err != nil {
		// Distinguish "not found / not uploaded" (400) from actual server errors (500)
		if strings.Contains(err.Error(), "not") {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if h.wsHub != nil {
		h.wsHub.Broadcast("clusters.changed", nil)
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- PUT /clusters/:context/rename — set a friendly display name ---

type renameClusterRequest struct {
	DisplayName string `json:"displayName"` // empty string clears the override
}

func (h *handlers) handleRenameCluster(w http.ResponseWriter, r *http.Request) {
	contextName := chi.URLParam(r, "context")
	if contextName == "" {
		respondError(w, http.StatusBadRequest, "context name is required")
		return
	}

	var req renameClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Prevent ridiculous values
	if len(req.DisplayName) > 100 {
		respondError(w, http.StatusBadRequest, "display name too long (max 100 characters)")
		return
	}

	if err := h.manager.SetClusterDisplayName(contextName, req.DisplayName); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.wsHub != nil {
		h.wsHub.Broadcast("clusters.changed", nil)
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
