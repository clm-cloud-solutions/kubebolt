package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// EmailDeliveryConfig is a channel-specific subset of NotificationsConfig.
// Separated from webhook-based channels because SMTP has many more knobs.
type EmailDeliveryConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	To         []string // one or more recipient addresses, comma-separated in env
	DigestMode string   // "instant" | "hourly" | "daily"
}

// Enabled returns true if email is sufficiently configured to send.
// We consider it enabled when the minimum set (host + from + at least one
// recipient) is present.
func (e EmailDeliveryConfig) Enabled() bool {
	return e.Host != "" && e.From != "" && len(e.To) > 0
}

// NotificationsConfig holds notification settings loaded from KUBEBOLT_NOTIFICATIONS_*
// and per-channel env vars. Each channel is enabled by providing its own connection details.
type NotificationsConfig struct {
	SlackWebhookURL   string
	DiscordWebhookURL string
	Email             EmailDeliveryConfig
	// MasterEnabled is a global kill switch. When false, no notifications are
	// sent regardless of which channels are configured. Useful for maintenance
	// windows or temporarily silencing alerts without un-configuring channels.
	MasterEnabled   bool
	MinSeverity     string        // "critical" | "warning" | "info"
	Cooldown        time.Duration // dedup window for the same insight
	BaseURL         string        // optional — included as a link in messages
	IncludeResolved bool          // also notify when an insight is resolved
}

// Enabled returns true if at least one notification channel is configured.
func (c NotificationsConfig) Enabled() bool {
	return c.SlackWebhookURL != "" || c.DiscordWebhookURL != "" || c.Email.Enabled()
}

// LoadNotificationsConfig reads notification settings from env vars.
// All values are optional. If no channel is configured, notifications are disabled.
func LoadNotificationsConfig() NotificationsConfig {
	cfg := NotificationsConfig{
		SlackWebhookURL:   os.Getenv("KUBEBOLT_SLACK_WEBHOOK_URL"),
		DiscordWebhookURL: os.Getenv("KUBEBOLT_DISCORD_WEBHOOK_URL"),
		Email:             loadEmailConfig(),
		MasterEnabled:     parseBoolDefault(os.Getenv("KUBEBOLT_NOTIFICATIONS_ENABLED"), true),
		MinSeverity:       os.Getenv("KUBEBOLT_NOTIFICATIONS_MIN_SEVERITY"),
		BaseURL:           os.Getenv("KUBEBOLT_NOTIFICATIONS_BASE_URL"),
		IncludeResolved:   parseBoolDefault(os.Getenv("KUBEBOLT_NOTIFICATIONS_INCLUDE_RESOLVED"), false),
	}

	// Default to warning if unset or invalid
	switch cfg.MinSeverity {
	case "critical", "warning", "info":
		// valid, keep as-is
	default:
		cfg.MinSeverity = "warning"
	}

	// Cooldown parsing with 1h default
	cfg.Cooldown = time.Hour
	if v := os.Getenv("KUBEBOLT_NOTIFICATIONS_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Cooldown = d
		}
	}

	return cfg
}

// parseBoolDefault parses a string as a boolean, returning def on any parse
// failure or empty input. Accepts the standard Go bool strings (1/0, t/f,
// true/false, TRUE/FALSE) plus a few ergonomic variants.
func parseBoolDefault(s string, def bool) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return def
	}
	switch s {
	case "1", "t", "true", "yes", "y", "on":
		return true
	case "0", "f", "false", "no", "n", "off":
		return false
	}
	return def
}

// loadEmailConfig reads SMTP settings from env vars. Defaults:
//   - Port: 587 (STARTTLS)
//   - DigestMode: instant
//   - To: split on commas, whitespace trimmed
func loadEmailConfig() EmailDeliveryConfig {
	cfg := EmailDeliveryConfig{
		Host:     os.Getenv("KUBEBOLT_SMTP_HOST"),
		Username: os.Getenv("KUBEBOLT_SMTP_USERNAME"),
		Password: os.Getenv("KUBEBOLT_SMTP_PASSWORD"),
		From:     os.Getenv("KUBEBOLT_SMTP_FROM"),
	}

	// Port (defaults to 587)
	cfg.Port = 587
	if v := os.Getenv("KUBEBOLT_SMTP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			cfg.Port = p
		}
	}

	// Recipients (comma-separated)
	if to := os.Getenv("KUBEBOLT_SMTP_TO"); to != "" {
		for _, addr := range strings.Split(to, ",") {
			if trimmed := strings.TrimSpace(addr); trimmed != "" {
				cfg.To = append(cfg.To, trimmed)
			}
		}
	}

	// Digest mode (defaults to instant)
	cfg.DigestMode = "instant"
	if v := strings.ToLower(os.Getenv("KUBEBOLT_SMTP_DIGEST_MODE")); v == "hourly" || v == "daily" {
		cfg.DigestMode = v
	}

	return cfg
}
