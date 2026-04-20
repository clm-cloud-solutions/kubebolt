package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
)

// notificationsChannelInfo describes one configured channel for the config endpoint.
type notificationsChannelInfo struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type notificationsConfigResponse struct {
	Enabled     bool                       `json:"enabled"`
	MinSeverity string                     `json:"minSeverity"`
	Cooldown    string                     `json:"cooldown"` // duration as Go-style string: "1h0m0s"
	Channels    []notificationsChannelInfo `json:"channels"`
}

// handleNotificationsConfig returns the current notification settings.
// Admin-only. Never exposes webhook URLs.
func (h *handlers) handleNotificationsConfig(w http.ResponseWriter, r *http.Request) {
	// Always return a shape even when notifications are disabled, so the
	// frontend can render a consistent "all channels: disabled" view.
	resp := notificationsConfigResponse{
		Channels: []notificationsChannelInfo{
			{Name: "slack", Enabled: false},
			{Name: "discord", Enabled: false},
		},
	}

	if h.notifications == nil || !h.notifications.Enabled() {
		resp.MinSeverity = "warning"
		resp.Cooldown = time.Hour.String()
		respondJSON(w, http.StatusOK, resp)
		return
	}

	resp.Enabled = true
	resp.MinSeverity = h.notifications.MinSeverity()
	resp.Cooldown = h.notifications.Cooldown().String()

	for _, n := range h.notifications.Notifiers() {
		for i := range resp.Channels {
			if resp.Channels[i].Name == n.Name() {
				resp.Channels[i].Enabled = true
			}
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

// handleNotificationsTest sends a synthetic message to one channel.
// URL: POST /notifications/test/{channel} where channel is "slack" or "discord".
func (h *handlers) handleNotificationsTest(w http.ResponseWriter, r *http.Request) {
	if h.notifications == nil || !h.notifications.Enabled() {
		respondError(w, http.StatusBadRequest, "notifications are not configured")
		return
	}

	channel := chi.URLParam(r, "channel")
	if channel == "" {
		respondError(w, http.StatusBadRequest, "channel name is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := h.notifications.SendTest(ctx, channel); err != nil {
		if err == notifications.ErrNoSuchChannel {
			respondError(w, http.StatusNotFound, "channel \""+channel+"\" is not configured")
			return
		}
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
