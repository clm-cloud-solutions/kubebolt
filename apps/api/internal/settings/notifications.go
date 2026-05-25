package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// notificationsSettingsKey is the BoltDB key under the settings bucket
// where the JSON-encoded StoredNotificationsSettings lives. One row per
// process (per-tenant notification routing is out of scope for V1).
const notificationsSettingsKey = "notifications"

// StoredNotificationsSettings is the on-disk shape for the Notifications
// domain. Every field is a pointer so nil means "fall back to env
// baseline" — same partial-override semantics as Copilot. The PUT
// handler accepts a patch with this shape, merges onto the existing
// record, and writes back.
//
// Webhook URLs (Slack, Discord) and the SMTP password are treated as
// secrets — encrypted at rest via secretCrypto and surfaced through a
// reveal-and-replace UI flow. The Slack/Discord webhook URL contains
// the channel-write token in the path, so the full URL IS the secret.
type StoredNotificationsSettings struct {
	Global  *StoredNotificationsGlobal `json:"global,omitempty"`
	Slack   *StoredSlackSettings       `json:"slack,omitempty"`
	Discord *StoredDiscordSettings     `json:"discord,omitempty"`
	Email   *StoredEmailSettings       `json:"email,omitempty"`
}

// StoredNotificationsGlobal mirrors the 5 cluster-wide notification
// knobs. CooldownSeconds is stored as an int rather than a Go duration
// string because the UI's input is a number-of-seconds field; round-
// tripping through time.ParseDuration would require extra encoding.
type StoredNotificationsGlobal struct {
	MasterEnabled   *bool   `json:"masterEnabled,omitempty"`
	MinSeverity     *string `json:"minSeverity,omitempty"`
	CooldownSeconds *int    `json:"cooldownSeconds,omitempty"`
	BaseURL         *string `json:"baseURL,omitempty"`
	IncludeResolved *bool   `json:"includeResolved,omitempty"`
}

// StoredSlackSettings holds the encrypted Slack webhook URL and the
// per-channel enable toggle. Single channel-pause flag today; kept as a
// struct so future per-channel knobs (custom username, icon) slot in
// without a JSON shape break.
type StoredSlackSettings struct {
	Enabled           *bool   `json:"enabled,omitempty"`
	WebhookURLEncoded *string `json:"webhookURLEncoded,omitempty"`
}

// StoredDiscordSettings is the Discord equivalent of Slack — same
// encrypted-webhook + enabled-toggle shape, separate type for clarity.
type StoredDiscordSettings struct {
	Enabled           *bool   `json:"enabled,omitempty"`
	WebhookURLEncoded *string `json:"webhookURLEncoded,omitempty"`
}

// StoredEmailSettings carries the SMTP delivery overrides. To is stored
// as a slice (one entry per recipient) so the UI can render a chip-list
// editor without splitting/joining on commas at every render.
type StoredEmailSettings struct {
	Enabled         *bool     `json:"enabled,omitempty"`
	Host            *string   `json:"host,omitempty"`
	Port            *int      `json:"port,omitempty"`
	Username        *string   `json:"username,omitempty"`
	PasswordEncoded *string   `json:"passwordEncoded,omitempty"`
	From            *string   `json:"from,omitempty"`
	To              *[]string `json:"to,omitempty"`
	DigestMode      *string   `json:"digestMode,omitempty"`
}

// Notifications returns the live NotificationsConfig (env baseline +
// BoltDB override merged). Caches the resolved value until
// InvalidateNotifications is called. Safe for concurrent use.
func (r *Runtime) Notifications() config.NotificationsConfig {
	r.mu.RLock()
	if r.notificationsValid {
		cfg := r.notifications
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.notificationsValid {
		return r.notifications
	}
	r.notifications = r.resolveNotificationsLocked()
	r.notificationsValid = true
	return r.notifications
}

// InvalidateNotifications marks the cached Notifications config as
// stale. Called by PUT and reset handlers. Safe to call from any
// goroutine.
func (r *Runtime) InvalidateNotifications() {
	r.mu.Lock()
	r.notificationsValid = false
	r.mu.Unlock()
}

// resolveNotificationsLocked merges the BoltDB-persisted partial
// override onto the env baseline. Caller must hold r.mu (write or
// read). On any decode error, returns the env baseline unchanged.
func (r *Runtime) resolveNotificationsLocked() config.NotificationsConfig {
	cfg := r.envNotifications

	raw, err := r.store.GetSetting(notificationsSettingsKey)
	if err != nil {
		return cfg
	}
	var stored StoredNotificationsSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		return cfg
	}
	applyStoredNotifications(&cfg, &stored, r.crypto)
	return cfg
}

// applyStoredNotifications merges a non-nil StoredNotificationsSettings
// onto an existing NotificationsConfig. Decryption errors leave the
// corresponding field at its env baseline.
func applyStoredNotifications(cfg *config.NotificationsConfig, stored *StoredNotificationsSettings, crypto *secretCrypto) {
	if g := stored.Global; g != nil {
		if g.MasterEnabled != nil {
			cfg.MasterEnabled = *g.MasterEnabled
		}
		if g.MinSeverity != nil {
			cfg.MinSeverity = *g.MinSeverity
		}
		if g.CooldownSeconds != nil && *g.CooldownSeconds > 0 {
			cfg.Cooldown = time.Duration(*g.CooldownSeconds) * time.Second
		}
		if g.BaseURL != nil {
			cfg.BaseURL = *g.BaseURL
		}
		if g.IncludeResolved != nil {
			cfg.IncludeResolved = *g.IncludeResolved
		}
	}
	if s := stored.Slack; s != nil {
		if s.Enabled != nil {
			cfg.SlackEnabled = *s.Enabled
		}
		if s.WebhookURLEncoded != nil && *s.WebhookURLEncoded != "" {
			if pt, err := crypto.decrypt(*s.WebhookURLEncoded); err == nil {
				cfg.SlackWebhookURL = pt
			}
		}
	}
	if d := stored.Discord; d != nil {
		if d.Enabled != nil {
			cfg.DiscordEnabled = *d.Enabled
		}
		if d.WebhookURLEncoded != nil && *d.WebhookURLEncoded != "" {
			if pt, err := crypto.decrypt(*d.WebhookURLEncoded); err == nil {
				cfg.DiscordWebhookURL = pt
			}
		}
	}
	if e := stored.Email; e != nil {
		if e.Enabled != nil {
			cfg.EmailEnabled = *e.Enabled
		}
		applyStoredEmail(&cfg.Email, e, crypto)
	}
}

func applyStoredEmail(cfg *config.EmailDeliveryConfig, stored *StoredEmailSettings, crypto *secretCrypto) {
	if stored.Host != nil {
		cfg.Host = *stored.Host
	}
	if stored.Port != nil {
		cfg.Port = *stored.Port
	}
	if stored.Username != nil {
		cfg.Username = *stored.Username
	}
	if stored.PasswordEncoded != nil && *stored.PasswordEncoded != "" {
		if pt, err := crypto.decrypt(*stored.PasswordEncoded); err == nil {
			cfg.Password = pt
		}
	}
	if stored.From != nil {
		cfg.From = *stored.From
	}
	if stored.To != nil {
		cfg.To = append([]string(nil), (*stored.To)...)
	}
	if stored.DigestMode != nil {
		cfg.DigestMode = *stored.DigestMode
	}
}

// PutNotifications validates and persists a partial Notifications
// settings patch. Plaintext secrets are encrypted on the way in via
// the same secretCrypto used for Copilot — caller passes plaintext,
// store sees only the wrapped form. Nil pointer for a secret means
// "unchanged"; non-nil pointer to empty string means "clear it".
//
// Returns ValidationError on bad input — the handler maps that to 400.
func (r *Runtime) PutNotifications(patch *StoredNotificationsSettings, plaintextSlackURL, plaintextDiscordURL, plaintextSMTPPassword *string) error {
	if err := validateNotificationsPatch(patch); err != nil {
		return err
	}

	if plaintextSlackURL != nil {
		if patch.Slack == nil {
			patch.Slack = &StoredSlackSettings{}
		}
		enc, err := r.crypto.encrypt(*plaintextSlackURL)
		if err != nil {
			return fmt.Errorf("encrypt slack webhook: %w", err)
		}
		patch.Slack.WebhookURLEncoded = &enc
	}
	if plaintextDiscordURL != nil {
		if patch.Discord == nil {
			patch.Discord = &StoredDiscordSettings{}
		}
		enc, err := r.crypto.encrypt(*plaintextDiscordURL)
		if err != nil {
			return fmt.Errorf("encrypt discord webhook: %w", err)
		}
		patch.Discord.WebhookURLEncoded = &enc
	}
	if plaintextSMTPPassword != nil {
		if patch.Email == nil {
			patch.Email = &StoredEmailSettings{}
		}
		enc, err := r.crypto.encrypt(*plaintextSMTPPassword)
		if err != nil {
			return fmt.Errorf("encrypt smtp password: %w", err)
		}
		patch.Email.PasswordEncoded = &enc
	}

	existing, _ := r.loadNotifications()
	merged := mergeNotifications(existing, *patch)

	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := r.store.SetSetting(notificationsSettingsKey, encoded); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	r.InvalidateNotifications()
	return nil
}

// loadNotifications reads the existing BoltDB record (no env merge).
func (r *Runtime) loadNotifications() (StoredNotificationsSettings, error) {
	raw, err := r.store.GetSetting(notificationsSettingsKey)
	if err != nil {
		return StoredNotificationsSettings{}, nil
	}
	var out StoredNotificationsSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return StoredNotificationsSettings{}, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// GetNotifications returns the BoltDB record (decrypted in memory) +
// the env baseline + a secret-readability flag — same shape as the
// Copilot GET path so the UI can render "this overrides env" hints.
func (r *Runtime) GetNotifications() (stored StoredNotificationsSettings, baseline config.NotificationsConfig, secretsReadable bool, err error) {
	stored, err = r.loadNotifications()
	if err != nil {
		return StoredNotificationsSettings{}, r.envNotifications, false, err
	}
	secretsReadable = true
	checkSecret := func(enc *string) {
		if enc == nil || *enc == "" {
			return
		}
		if _, decErr := r.crypto.decrypt(*enc); decErr != nil {
			secretsReadable = false
		}
	}
	if stored.Slack != nil {
		checkSecret(stored.Slack.WebhookURLEncoded)
	}
	if stored.Discord != nil {
		checkSecret(stored.Discord.WebhookURLEncoded)
	}
	if stored.Email != nil {
		checkSecret(stored.Email.PasswordEncoded)
	}
	return stored, r.envNotifications, secretsReadable, nil
}

// ResetNotifications clears the BoltDB record so every field falls
// back to env baseline.
func (r *Runtime) ResetNotifications() error {
	if err := r.store.SetSetting(notificationsSettingsKey, []byte("null")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	r.InvalidateNotifications()
	return nil
}

// ─── Masked render shapes ─────────────────────────────────────────────

// MaskedNotifications is the GET response shape — effective live config
// (with secrets masked) plus per-field "is overridden by store" flags so
// the UI can show source attribution without exposing plaintext.
type MaskedNotifications struct {
	Effective       MaskedEffectiveNotifications `json:"effective"`
	Stored          MaskedStoredNotifications    `json:"stored"`
	SecretsReadable bool                         `json:"secretsReadable"`
}

type MaskedEffectiveNotifications struct {
	MasterEnabled   bool   `json:"masterEnabled"`
	MinSeverity     string `json:"minSeverity"`
	CooldownSeconds int    `json:"cooldownSeconds"`
	BaseURL         string `json:"baseURL,omitempty"`
	IncludeResolved bool   `json:"includeResolved"`

	// Three flags per channel so the UI can render the tri-state header chip:
	//   - Configured: required fields are filled (webhook URL / SMTP fields)
	//   - Enabled:    operator's per-channel toggle is on
	//   - Active:     Configured AND Enabled — actually dispatching
	// Active=true is what BuildNotifiers gates on. The UI maps:
	//   !Configured     → "Not configured" (gray)
	//   Configured && !Enabled → "Paused" (gray)
	//   Configured && Enabled  → "Enabled" (green)
	SlackConfigured      bool   `json:"slackConfigured"`
	SlackEnabled         bool   `json:"slackEnabled"`
	SlackActive          bool   `json:"slackActive"`
	SlackWebhookMasked   string `json:"slackWebhookMasked,omitempty"`

	DiscordConfigured    bool   `json:"discordConfigured"`
	DiscordEnabled       bool   `json:"discordEnabled"`
	DiscordActive        bool   `json:"discordActive"`
	DiscordWebhookMasked string `json:"discordWebhookMasked,omitempty"`

	EmailConfigured     bool     `json:"emailConfigured"`
	EmailEnabled        bool     `json:"emailEnabled"`
	EmailActive         bool     `json:"emailActive"`
	EmailHost           string   `json:"emailHost,omitempty"`
	EmailPort           int      `json:"emailPort,omitempty"`
	EmailUsername       string   `json:"emailUsername,omitempty"`
	EmailPasswordMasked string   `json:"emailPasswordMasked,omitempty"`
	EmailFrom           string   `json:"emailFrom,omitempty"`
	EmailTo             []string `json:"emailTo,omitempty"`
	EmailDigestMode     string   `json:"emailDigestMode,omitempty"`
}

// MaskedStoredNotifications carries per-field "is the BoltDB override
// the source for this field?" booleans. The frontend uses these to
// render the source badge — same convention as the Copilot tab.
type MaskedStoredNotifications struct {
	HasGlobalOverride bool `json:"hasGlobalOverride"`
	HasSlackOverride  bool `json:"hasSlackOverride"`
	HasDiscordOverride bool `json:"hasDiscordOverride"`
	HasEmailOverride  bool `json:"hasEmailOverride"`

	Global  *MaskedStoredGlobalNotifications `json:"global,omitempty"`
	Slack   *MaskedStoredSlack               `json:"slack,omitempty"`
	Discord *MaskedStoredDiscord             `json:"discord,omitempty"`
	Email   *MaskedStoredEmail               `json:"email,omitempty"`
}

type MaskedStoredGlobalNotifications struct {
	MasterEnabled   *bool   `json:"masterEnabled,omitempty"`
	MinSeverity     *string `json:"minSeverity,omitempty"`
	CooldownSeconds *int    `json:"cooldownSeconds,omitempty"`
	BaseURL         *string `json:"baseURL,omitempty"`
	IncludeResolved *bool   `json:"includeResolved,omitempty"`
}

type MaskedStoredSlack struct {
	Enabled           *bool  `json:"enabled,omitempty"`
	WebhookConfigured bool   `json:"webhookConfigured"`
	WebhookMasked     string `json:"webhookMasked,omitempty"`
}

type MaskedStoredDiscord struct {
	Enabled           *bool  `json:"enabled,omitempty"`
	WebhookConfigured bool   `json:"webhookConfigured"`
	WebhookMasked     string `json:"webhookMasked,omitempty"`
}

type MaskedStoredEmail struct {
	Enabled            *bool     `json:"enabled,omitempty"`
	Host               *string   `json:"host,omitempty"`
	Port               *int      `json:"port,omitempty"`
	Username           *string   `json:"username,omitempty"`
	PasswordConfigured bool      `json:"passwordConfigured"`
	PasswordMasked     string    `json:"passwordMasked,omitempty"`
	From               *string   `json:"from,omitempty"`
	To                 *[]string `json:"to,omitempty"`
	DigestMode         *string   `json:"digestMode,omitempty"`
}

// RenderMaskedNotifications builds the GET response from the stored
// record + env baseline + crypto.
func (r *Runtime) RenderMaskedNotifications() (MaskedNotifications, error) {
	stored, _, secretsReadable, err := r.GetNotifications()
	if err != nil {
		return MaskedNotifications{}, err
	}
	effective := r.Notifications()

	emailCfg := effective.Email

	slackConfigured := effective.SlackWebhookURL != ""
	discordConfigured := effective.DiscordWebhookURL != ""
	emailConfigured := emailCfg.Enabled() // "has the SMTP minimum fields"

	out := MaskedNotifications{
		Effective: MaskedEffectiveNotifications{
			MasterEnabled:   effective.MasterEnabled,
			MinSeverity:     effective.MinSeverity,
			CooldownSeconds: int(effective.Cooldown / time.Second),
			BaseURL:         effective.BaseURL,
			IncludeResolved: effective.IncludeResolved,

			SlackConfigured:    slackConfigured,
			SlackEnabled:       effective.SlackEnabled,
			SlackActive:        slackConfigured && effective.SlackEnabled,
			SlackWebhookMasked: maskSecret(effective.SlackWebhookURL),

			DiscordConfigured:    discordConfigured,
			DiscordEnabled:       effective.DiscordEnabled,
			DiscordActive:        discordConfigured && effective.DiscordEnabled,
			DiscordWebhookMasked: maskSecret(effective.DiscordWebhookURL),

			EmailConfigured:     emailConfigured,
			EmailEnabled:        effective.EmailEnabled,
			EmailActive:         emailConfigured && effective.EmailEnabled,
			EmailHost:           emailCfg.Host,
			EmailPort:           emailCfg.Port,
			EmailUsername:       emailCfg.Username,
			EmailPasswordMasked: maskSecret(emailCfg.Password),
			EmailFrom:           emailCfg.From,
			EmailTo:             append([]string(nil), emailCfg.To...),
			EmailDigestMode:     emailCfg.DigestMode,
		},
		Stored:          renderStoredNotificationsMask(stored),
		SecretsReadable: secretsReadable,
	}
	return out, nil
}

func renderStoredNotificationsMask(s StoredNotificationsSettings) MaskedStoredNotifications {
	out := MaskedStoredNotifications{}
	if g := s.Global; g != nil {
		out.HasGlobalOverride = true
		out.Global = &MaskedStoredGlobalNotifications{
			MasterEnabled:   g.MasterEnabled,
			MinSeverity:     g.MinSeverity,
			CooldownSeconds: g.CooldownSeconds,
			BaseURL:         g.BaseURL,
			IncludeResolved: g.IncludeResolved,
		}
	}
	if sl := s.Slack; sl != nil {
		out.HasSlackOverride = true
		out.Slack = &MaskedStoredSlack{Enabled: sl.Enabled}
		if sl.WebhookURLEncoded != nil && *sl.WebhookURLEncoded != "" {
			out.Slack.WebhookConfigured = true
			out.Slack.WebhookMasked = "***configured***"
		}
	}
	if dc := s.Discord; dc != nil {
		out.HasDiscordOverride = true
		out.Discord = &MaskedStoredDiscord{Enabled: dc.Enabled}
		if dc.WebhookURLEncoded != nil && *dc.WebhookURLEncoded != "" {
			out.Discord.WebhookConfigured = true
			out.Discord.WebhookMasked = "***configured***"
		}
	}
	if e := s.Email; e != nil {
		out.HasEmailOverride = true
		out.Email = &MaskedStoredEmail{
			Enabled:    e.Enabled,
			Host:       e.Host,
			Port:       e.Port,
			Username:   e.Username,
			From:       e.From,
			To:         e.To,
			DigestMode: e.DigestMode,
		}
		if e.PasswordEncoded != nil && *e.PasswordEncoded != "" {
			out.Email.PasswordConfigured = true
			out.Email.PasswordMasked = "***configured***"
		}
	}
	return out
}

// ─── Validation ───────────────────────────────────────────────────────

func validateNotificationsPatch(p *StoredNotificationsSettings) error {
	if p == nil {
		return &ValidationError{Field: "patch", Message: "patch is required"}
	}
	if g := p.Global; g != nil {
		if g.MinSeverity != nil {
			switch *g.MinSeverity {
			case "critical", "warning", "info":
			default:
				return &ValidationError{Field: "global.minSeverity", Message: "must be one of: critical, warning, info"}
			}
		}
		if g.CooldownSeconds != nil && *g.CooldownSeconds < 0 {
			return &ValidationError{Field: "global.cooldownSeconds", Message: "must be >= 0"}
		}
	}
	if p.Slack != nil && p.Slack.WebhookURLEncoded != nil && *p.Slack.WebhookURLEncoded != "" {
		// Webhook envelope is opaque post-encryption; format is validated
		// pre-encryption in the handler. Skip here.
	}
	if e := p.Email; e != nil {
		if e.Port != nil && (*e.Port <= 0 || *e.Port > 65535) {
			return &ValidationError{Field: "email.port", Message: "must be in (0, 65535]"}
		}
		if e.From != nil && *e.From != "" {
			if _, err := mail.ParseAddress(*e.From); err != nil {
				return &ValidationError{Field: "email.from", Message: "must be a valid email address"}
			}
		}
		if e.To != nil {
			for i, addr := range *e.To {
				if _, err := mail.ParseAddress(addr); err != nil {
					return &ValidationError{Field: fmt.Sprintf("email.to[%d]", i), Message: fmt.Sprintf("%q is not a valid email address", addr)}
				}
			}
		}
		if e.DigestMode != nil {
			switch *e.DigestMode {
			case "instant", "hourly", "daily":
			default:
				return &ValidationError{Field: "email.digestMode", Message: "must be one of: instant, hourly, daily"}
			}
		}
	}
	return nil
}

// ValidateWebhookURL is a pre-encryption sanity check for caller-
// provided webhook URLs. We accept https:// only — Slack/Discord both
// require it, and rejecting http here saves an operator from creating
// a record that silently fails at first dispatch.
func ValidateWebhookURL(url string) error {
	if url == "" {
		return errors.New("webhook URL cannot be empty")
	}
	if !strings.HasPrefix(url, "https://") {
		return errors.New("webhook URL must use https://")
	}
	return nil
}

// ─── Merge helper ─────────────────────────────────────────────────────

func mergeNotifications(base, patch StoredNotificationsSettings) StoredNotificationsSettings {
	out := base
	if patch.Global != nil {
		if out.Global == nil {
			out.Global = &StoredNotificationsGlobal{}
		}
		mergeGlobal(out.Global, patch.Global)
	}
	if patch.Slack != nil {
		if out.Slack == nil {
			out.Slack = &StoredSlackSettings{}
		}
		if patch.Slack.Enabled != nil {
			out.Slack.Enabled = patch.Slack.Enabled
		}
		if patch.Slack.WebhookURLEncoded != nil {
			if *patch.Slack.WebhookURLEncoded == "" {
				out.Slack.WebhookURLEncoded = nil
			} else {
				out.Slack.WebhookURLEncoded = patch.Slack.WebhookURLEncoded
			}
		}
	}
	if patch.Discord != nil {
		if out.Discord == nil {
			out.Discord = &StoredDiscordSettings{}
		}
		if patch.Discord.Enabled != nil {
			out.Discord.Enabled = patch.Discord.Enabled
		}
		if patch.Discord.WebhookURLEncoded != nil {
			if *patch.Discord.WebhookURLEncoded == "" {
				out.Discord.WebhookURLEncoded = nil
			} else {
				out.Discord.WebhookURLEncoded = patch.Discord.WebhookURLEncoded
			}
		}
	}
	if patch.Email != nil {
		if out.Email == nil {
			out.Email = &StoredEmailSettings{}
		}
		mergeEmail(out.Email, patch.Email)
	}
	return out
}

func mergeGlobal(out, patch *StoredNotificationsGlobal) {
	if patch.MasterEnabled != nil {
		out.MasterEnabled = patch.MasterEnabled
	}
	if patch.MinSeverity != nil {
		out.MinSeverity = patch.MinSeverity
	}
	if patch.CooldownSeconds != nil {
		out.CooldownSeconds = patch.CooldownSeconds
	}
	if patch.BaseURL != nil {
		out.BaseURL = patch.BaseURL
	}
	if patch.IncludeResolved != nil {
		out.IncludeResolved = patch.IncludeResolved
	}
}

func mergeEmail(out, patch *StoredEmailSettings) {
	if patch.Enabled != nil {
		out.Enabled = patch.Enabled
	}
	if patch.Host != nil {
		out.Host = patch.Host
	}
	if patch.Port != nil {
		out.Port = patch.Port
	}
	if patch.Username != nil {
		out.Username = patch.Username
	}
	if patch.PasswordEncoded != nil {
		if *patch.PasswordEncoded == "" {
			out.PasswordEncoded = nil
		} else {
			out.PasswordEncoded = patch.PasswordEncoded
		}
	}
	if patch.From != nil {
		out.From = patch.From
	}
	if patch.To != nil {
		out.To = patch.To
	}
	if patch.DigestMode != nil {
		out.DigestMode = patch.DigestMode
	}
}
