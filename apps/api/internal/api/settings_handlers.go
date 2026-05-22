package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// Spec #09 — runtime config via UI. Handlers under /admin/settings/*
// expose the BoltDB-backed override layer that sits on top of env-driven
// boot config. All admin-only via the route group's middleware.
//
// The mental model for the response shape: every GET returns "effective"
// (what's actually being used right now) AND "stored" (what's been
// configured via UI, with secrets masked). The UI uses both to render
// per-field "set here" vs "inherits from env" indicators.

// putCopilotRequest is the wire shape for PUT /admin/settings/copilot.
// All fields are optional — pointer-shaped so the handler can tell
// "leave alone" (nil) from "set to this value" (non-nil) from "clear
// override" (non-nil pointer to empty string, for API keys).
//
// The plaintextAPIKey / plaintextFallbackAPIKey fields are top-level
// (not nested under primary/fallback) so the JSON payload never
// accidentally captures a secret inside a stored-record-shaped object
// that gets logged or echoed in errors. Caller passes the raw key here;
// the handler hands it to the runtime which encrypts before persisting.
type putCopilotRequest struct {
	Patch                   *settings.StoredCopilotSettings `json:"patch,omitempty"`
	PlaintextAPIKey         *string                         `json:"plaintextAPIKey,omitempty"`
	PlaintextFallbackAPIKey *string                         `json:"plaintextFallbackAPIKey,omitempty"`
}

// handleGetSettingsCopilot returns the masked Copilot settings view.
// Secrets never round-trip to the client — only their masked previews.
// The response carries `secretsReadable=false` when the encrypted key
// can't be decrypted with the current JWT secret (rotation scenario).
func (h *handlers) handleGetSettingsCopilot(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	masked, err := h.settingsRuntime.RenderMaskedCopilot()
	if err != nil {
		slog.Error("settings copilot get failed", slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to read copilot settings")
		return
	}
	respondJSON(w, http.StatusOK, masked)
}

// handlePutSettingsCopilot accepts a partial Copilot settings patch.
// Validation errors map to 400 with the offending field name.
// Successful PUT invalidates the runtime cache so subsequent reads
// (including the next /copilot/chat request) pick up the new values.
//
// We also nudge the existing copilot/config endpoint by NOT caching it
// — but that endpoint always reads from h.copilotConfig today. Spec #09
// follow-up wires it through settingsRuntime too so the chat UI's
// "Configured / Not configured" pill updates on save without a refresh.
func (h *handlers) handlePutSettingsCopilot(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	var req putCopilotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Patch == nil {
		// Allow patch:null when the caller only wants to set the
		// plaintext API key — synthesize an empty patch so the runtime
		// accepts it.
		req.Patch = &settings.StoredCopilotSettings{}
	}

	if err := h.settingsRuntime.PutCopilot(req.Patch, req.PlaintextAPIKey, req.PlaintextFallbackAPIKey); err != nil {
		if settings.IsValidation(err) {
			var ve *settings.ValidationError
			if errors.As(err, &ve) {
				respondJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "validation failed",
					"field":   ve.Field,
					"message": ve.Message,
				})
				return
			}
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("settings copilot put failed", slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to persist copilot settings")
		return
	}

	// Audit: which admin changed config when. Keep the user ID and
	// origin; not the secret values themselves. Same `actor_id` field
	// shape the user/tenant audit lines use.
	if uid := auth.ContextUserID(r); uid != "" {
		slog.Info("admin settings change",
			slog.String("domain", "copilot"),
			slog.String("actor_id", uid),
			slog.String("source", "admin_settings_ui"),
		)
	}

	// Re-read the masked view so the UI can update its form state
	// without a separate GET round-trip after save.
	masked, err := h.settingsRuntime.RenderMaskedCopilot()
	if err != nil {
		slog.Warn("settings copilot post-write render failed", slog.String("error", err.Error()))
		// Still report success — the write happened.
		respondJSON(w, http.StatusOK, map[string]any{"status": "saved"})
		return
	}
	respondJSON(w, http.StatusOK, masked)
}

// handleResetSettingsCopilot clears the BoltDB override entirely so the
// next read falls back to the env baseline. Used by "Reset to env
// defaults" on the Copilot tab. Idempotent — calling it when no
// override exists is a no-op success.
func (h *handlers) handleResetSettingsCopilot(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	if err := h.settingsRuntime.ResetCopilot(); err != nil {
		slog.Error("settings copilot reset failed", slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to reset copilot settings")
		return
	}
	if uid := auth.ContextUserID(r); uid != "" {
		slog.Info("admin settings reset",
			slog.String("domain", "copilot"),
			slog.String("actor_id", uid),
			slog.String("source", "admin_settings_ui"),
		)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Notifications ────────────────────────────────────────────────────
//
// Spec #09 — admin-editable notifications (webhooks, SMTP, global
// thresholds). PUT goes through settingsRuntime.PutNotifications to
// persist + crypto.encrypt secrets, then nudges the live manager via
// SetConfig + SetNotifiers so the change takes effect on the very
// next insight without a process restart. The test-notification
// endpoint stays under /admin/notifications/test (existing) and reads
// from the live manager — so a hot-reloaded webhook is testable
// immediately after save.

// putNotificationsRequest is the wire shape for PUT /admin/settings/
// notifications. plaintext* fields are top-level for the same reason
// as the Copilot handler — keep the secrets out of any structured
// "stored record" object that might end up in logs.
type putNotificationsRequest struct {
	Patch                  *settings.StoredNotificationsSettings `json:"patch,omitempty"`
	PlaintextSlackURL      *string                               `json:"plaintextSlackWebhookURL,omitempty"`
	PlaintextDiscordURL    *string                               `json:"plaintextDiscordWebhookURL,omitempty"`
	PlaintextSMTPPassword  *string                               `json:"plaintextSMTPPassword,omitempty"`
}

func (h *handlers) handleGetSettingsNotifications(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	masked, err := h.settingsRuntime.RenderMaskedNotifications()
	if err != nil {
		slog.Error("settings notifications get failed", slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to read notifications settings")
		return
	}
	respondJSON(w, http.StatusOK, masked)
}

func (h *handlers) handlePutSettingsNotifications(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	var req putNotificationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Patch == nil {
		req.Patch = &settings.StoredNotificationsSettings{}
	}

	// Webhook URL format sanity-check happens BEFORE encryption so we
	// don't persist an invalid URL — the BoltDB record stays clean and
	// the UI gets a structured 400.
	if req.PlaintextSlackURL != nil && *req.PlaintextSlackURL != "" {
		if err := settings.ValidateWebhookURL(*req.PlaintextSlackURL); err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "validation failed",
				"field":   "slack.webhookURL",
				"message": err.Error(),
			})
			return
		}
	}
	if req.PlaintextDiscordURL != nil && *req.PlaintextDiscordURL != "" {
		if err := settings.ValidateWebhookURL(*req.PlaintextDiscordURL); err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "validation failed",
				"field":   "discord.webhookURL",
				"message": err.Error(),
			})
			return
		}
	}

	if err := h.settingsRuntime.PutNotifications(req.Patch, req.PlaintextSlackURL, req.PlaintextDiscordURL, req.PlaintextSMTPPassword); err != nil {
		if settings.IsValidation(err) {
			var ve *settings.ValidationError
			if errors.As(err, &ve) {
				respondJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "validation failed",
					"field":   ve.Field,
					"message": ve.Message,
				})
				return
			}
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("settings notifications put failed", slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to persist notifications settings")
		return
	}

	h.applyNotificationsHotReload()

	if uid := auth.ContextUserID(r); uid != "" {
		slog.Info("admin settings change",
			slog.String("domain", "notifications"),
			slog.String("actor_id", uid),
			slog.String("source", "admin_settings_ui"),
		)
	}

	masked, err := h.settingsRuntime.RenderMaskedNotifications()
	if err != nil {
		slog.Warn("settings notifications post-write render failed", slog.String("error", err.Error()))
		respondJSON(w, http.StatusOK, map[string]any{"status": "saved"})
		return
	}
	respondJSON(w, http.StatusOK, masked)
}

func (h *handlers) handleResetSettingsNotifications(w http.ResponseWriter, r *http.Request) {
	if h.settingsRuntime == nil {
		respondError(w, http.StatusServiceUnavailable, "settings runtime not available (persistence disabled)")
		return
	}
	if err := h.settingsRuntime.ResetNotifications(); err != nil {
		slog.Error("settings notifications reset failed", slog.String("error", err.Error()))
		respondError(w, http.StatusInternalServerError, "failed to reset notifications settings")
		return
	}
	h.applyNotificationsHotReload()

	if uid := auth.ContextUserID(r); uid != "" {
		slog.Info("admin settings reset",
			slog.String("domain", "notifications"),
			slog.String("actor_id", uid),
			slog.String("source", "admin_settings_ui"),
		)
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyNotificationsHotReload pushes the freshly-resolved settings
// runtime view into the live notifications.Manager. Idempotent and
// safe to call from any handler. Without this, persistent settings
// changes would only take effect on the next process restart — the
// whole point of UI-edited settings is to avoid that.
//
// SetNotifiers stops any prior notifier that exposes Stop(), so the
// email digest flusher's goroutine doesn't leak across rebuilds.
func (h *handlers) applyNotificationsHotReload() {
	if h.settingsRuntime == nil || h.notifications == nil {
		return
	}
	resolved := h.settingsRuntime.Notifications()
	h.notifications.SetConfig(notifications.ConfigFromNotifications(resolved))
	h.notifications.SetNotifiers(notifications.BuildNotifiers(resolved))
}
