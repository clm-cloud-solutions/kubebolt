package api

import (
	"net/http"
	"strconv"
	"time"
)

// handleDeploys returns rollout events cluster-wide within the
// requested window. Consumed by the Capacity dashboard to overlay
// deploy markers on the trend charts.
//
// The frontend syncs the window with its current chart range — a
// 15m view fetches 15m of deploys, a 7d view fetches 7d. Callers
// without a windowMinutes param fall back to 24h, which covers the
// "I'm scrolling back" case without overshooting common ranges.
func (h *handlers) handleDeploys(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	minutes, _ := strconv.Atoi(r.URL.Query().Get("windowMinutes"))
	if minutes <= 0 {
		minutes = 1440
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute)
	deploys := conn.GetDeploys(since)
	respondJSON(w, http.StatusOK, deploys)
}
