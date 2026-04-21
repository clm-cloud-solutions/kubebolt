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
	// DigestMode is only set for email. Empty string for webhook channels.
	DigestMode string `json:"digestMode,omitempty"`
}

type notificationsConfigResponse struct {
	// Enabled means "at least one channel configured AND master toggle on".
	Enabled bool `json:"enabled"`
	// MasterEnabled is the global kill switch, independent of whether any
	// channels are configured. Useful for the UI to show "paused" state
	// when the toggle is off but channels remain configured.
	MasterEnabled   bool                       `json:"masterEnabled"`
	MinSeverity     string                     `json:"minSeverity"`
	Cooldown        string                     `json:"cooldown"` // duration as Go-style string: "1h0m0s"
	BaseURL         string                     `json:"baseUrl"`
	IncludeResolved bool                       `json:"includeResolved"`
	Channels        []notificationsChannelInfo `json:"channels"`
}

// digestModeReporter lets us surface the email digest mode without a type
// assertion on the exact email package — keeps the api package decoupled.
type digestModeReporter interface {
	DigestMode() string
}

// handleNotificationsConfig returns the current notification settings.
// Admin-only. Never exposes webhook URLs or SMTP credentials.
func (h *handlers) handleNotificationsConfig(w http.ResponseWriter, r *http.Request) {
	// Always return a shape even when notifications are disabled, so the
	// frontend can render a consistent "all channels: disabled" view.
	resp := notificationsConfigResponse{
		Channels: []notificationsChannelInfo{
			{Name: "slack", Enabled: false},
			{Name: "discord", Enabled: false},
			{Name: "email", Enabled: false},
		},
	}

	if h.notifications == nil {
		resp.MinSeverity = "warning"
		resp.Cooldown = time.Hour.String()
		respondJSON(w, http.StatusOK, resp)
		return
	}

	// Populate the global settings even when notifications are master-disabled
	// so the UI can render the current state accurately.
	resp.MasterEnabled = h.notifications.MasterEnabled()
	resp.MinSeverity = h.notifications.MinSeverity()
	resp.Cooldown = h.notifications.Cooldown().String()
	resp.BaseURL = h.notifications.BaseURL()
	resp.IncludeResolved = h.notifications.IncludeResolved()
	resp.Enabled = h.notifications.Enabled()

	for _, n := range h.notifications.Notifiers() {
		for i := range resp.Channels {
			if resp.Channels[i].Name == n.Name() {
				resp.Channels[i].Enabled = true
				if dm, ok := n.(digestModeReporter); ok {
					resp.Channels[i].DigestMode = dm.DigestMode()
				}
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
