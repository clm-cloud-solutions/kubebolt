package notifications

import "github.com/kubebolt/kubebolt/apps/api/internal/config"

// BuildNotifiers constructs the concrete notifier set from a
// NotificationsConfig. Returns one entry per channel that has the
// minimum required fields set; an empty/zero config yields an empty
// slice (the manager interprets that as "all channels disabled").
//
// Shared between boot-time wiring in cmd/server/main.go and the hot
// reload path in the admin Settings PUT handler — both need the same
// "only Slack and email enabled" composition logic so they stay in
// lockstep. Without this helper the two paths would drift the first
// time a new channel type is added.
func BuildNotifiers(cfg config.NotificationsConfig) []Notifier {
	var notifiers []Notifier
	// Each channel is gated on TWO conditions:
	//   1. It's configured (has the minimum required fields), AND
	//   2. Its per-channel enable toggle is on.
	// A "paused" channel has #1 but not #2 — config persists, no
	// notifier instance is created, no dispatches happen.
	if cfg.SlackWebhookURL != "" && cfg.SlackEnabled {
		notifiers = append(notifiers, NewSlackNotifier(cfg.SlackWebhookURL))
	}
	if cfg.DiscordWebhookURL != "" && cfg.DiscordEnabled {
		notifiers = append(notifiers, NewDiscordNotifier(cfg.DiscordWebhookURL))
	}
	if cfg.Email.Enabled() && cfg.EmailEnabled {
		notifiers = append(notifiers, NewEmailNotifier(EmailConfig{
			Host:       cfg.Email.Host,
			Port:       cfg.Email.Port,
			Username:   cfg.Email.Username,
			Password:   cfg.Email.Password,
			From:       cfg.Email.From,
			To:         cfg.Email.To,
			DigestMode: DigestMode(cfg.Email.DigestMode),
		}))
	}
	return notifiers
}

// ConfigFromNotifications extracts the manager-level Config from a
// full NotificationsConfig. The split mirrors how the manager treats
// channels (constructed from the same source, fed via SetNotifiers)
// separately from global knobs (fed via SetConfig).
func ConfigFromNotifications(cfg config.NotificationsConfig) Config {
	return Config{
		MasterEnabled:   cfg.MasterEnabled,
		MinSeverity:     cfg.MinSeverity,
		Cooldown:        cfg.Cooldown,
		BaseURL:         cfg.BaseURL,
		IncludeResolved: cfg.IncludeResolved,
	}
}
