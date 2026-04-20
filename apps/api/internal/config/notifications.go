package config

import (
	"os"
	"time"
)

// NotificationsConfig holds notification settings loaded from KUBEBOLT_NOTIFICATIONS_*
// and per-channel env vars. Each channel is enabled by providing its webhook URL.
type NotificationsConfig struct {
	SlackWebhookURL   string
	DiscordWebhookURL string
	MinSeverity       string        // "critical" | "warning" | "info"
	Cooldown          time.Duration // dedup window for the same insight
	BaseURL           string        // optional — included as a link in messages
}

// Enabled returns true if at least one notification channel is configured.
func (c NotificationsConfig) Enabled() bool {
	return c.SlackWebhookURL != "" || c.DiscordWebhookURL != ""
}

// LoadNotificationsConfig reads notification settings from env vars.
// All values are optional. If no webhook URL is set, notifications are disabled.
func LoadNotificationsConfig() NotificationsConfig {
	cfg := NotificationsConfig{
		SlackWebhookURL:   os.Getenv("KUBEBOLT_SLACK_WEBHOOK_URL"),
		DiscordWebhookURL: os.Getenv("KUBEBOLT_DISCORD_WEBHOOK_URL"),
		MinSeverity:       os.Getenv("KUBEBOLT_NOTIFICATIONS_MIN_SEVERITY"),
		BaseURL:           os.Getenv("KUBEBOLT_NOTIFICATIONS_BASE_URL"),
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
