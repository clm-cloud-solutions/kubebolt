package api

import (
	"context"
	"net/http"
	"time"
)

// updateCheckResponse is the JSON shape returned by GET /update-check.
// enabled=false means the operator (or env baseline) disabled the
// poller — the UI should not render the chip and must not retry on a
// tighter cadence.
type updateCheckResponse struct {
	Enabled           bool   `json:"enabled"`
	CurrentVersion    string `json:"currentVersion,omitempty"`
	LatestVersion     string `json:"latestVersion,omitempty"`
	IsUpdateAvailable bool   `json:"isUpdateAvailable"`
	ReleaseURL        string `json:"releaseUrl,omitempty"`
	ReleaseName       string `json:"releaseName,omitempty"`
	PublishedAt       string `json:"publishedAt,omitempty"`
}

// handleUpdateCheck returns the cached "latest stable release" lookup
// from the updatecheck service. Auth-required (any role) — the chip
// only appears for logged-in users so unauth callers don't need it.
//
// GitHub fetch errors are swallowed: the UI either gets the previously
// cached result or `isUpdateAvailable: false`. The frontend's standard
// staleTime polling will retry on the cache-TTL cadence.
func (h *handlers) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if h.updateCheck == nil {
		respondJSON(w, http.StatusOK, updateCheckResponse{Enabled: false})
		return
	}

	enabled := true
	if h.settingsRuntime != nil {
		enabled = h.settingsRuntime.General().UpdateCheckEnabled
	}
	if !enabled {
		respondJSON(w, http.StatusOK, updateCheckResponse{Enabled: false})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	rel, _ := h.updateCheck.Latest(ctx)
	resp := updateCheckResponse{
		Enabled:        true,
		CurrentVersion: h.updateCheck.CurrentVersion(),
	}
	if rel != nil {
		resp.LatestVersion = rel.TagName
		resp.IsUpdateAvailable = h.updateCheck.IsUpdateAvailable(rel.TagName)
		resp.ReleaseURL = rel.HTMLURL
		resp.ReleaseName = rel.Name
		if !rel.PublishedAt.IsZero() {
			resp.PublishedAt = rel.PublishedAt.UTC().Format(time.RFC3339)
		}
	}
	respondJSON(w, http.StatusOK, resp)
}
