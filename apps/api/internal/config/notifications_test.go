package config

import (
	"os"
	"testing"
	"time"
)

func clearNotifEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"KUBEBOLT_SLACK_WEBHOOK_URL", "KUBEBOLT_DISCORD_WEBHOOK_URL",
		"KUBEBOLT_SMTP_HOST", "KUBEBOLT_SMTP_PORT", "KUBEBOLT_SMTP_USERNAME",
		"KUBEBOLT_SMTP_PASSWORD", "KUBEBOLT_SMTP_FROM", "KUBEBOLT_SMTP_TO",
		"KUBEBOLT_SMTP_DIGEST_MODE",
		"KUBEBOLT_NOTIFICATIONS_ENABLED", "KUBEBOLT_NOTIFICATIONS_MIN_SEVERITY",
		"KUBEBOLT_NOTIFICATIONS_COOLDOWN", "KUBEBOLT_NOTIFICATIONS_BASE_URL",
		"KUBEBOLT_NOTIFICATIONS_INCLUDE_RESOLVED",
	} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}

func TestLoadNotificationsConfig_Defaults(t *testing.T) {
	clearNotifEnv(t)
	cfg := LoadNotificationsConfig()

	if cfg.Enabled() {
		t.Error("no channel configured → Enabled() should be false")
	}
	if !cfg.MasterEnabled {
		t.Error("master enabled should default to true")
	}
	if cfg.MinSeverity != "warning" {
		t.Errorf("default min severity = %q, want warning", cfg.MinSeverity)
	}
	if cfg.Cooldown != time.Hour {
		t.Errorf("default cooldown = %v, want 1h", cfg.Cooldown)
	}
	if cfg.IncludeResolved {
		t.Error("IncludeResolved should default to false")
	}
}

func TestLoadNotificationsConfig_Slack(t *testing.T) {
	clearNotifEnv(t)
	t.Setenv("KUBEBOLT_SLACK_WEBHOOK_URL", "https://hooks.slack.com/test")
	cfg := LoadNotificationsConfig()
	if !cfg.Enabled() {
		t.Error("slack webhook should enable notifications")
	}
	if cfg.SlackWebhookURL != "https://hooks.slack.com/test" {
		t.Errorf("slack URL = %q", cfg.SlackWebhookURL)
	}
}

func TestLoadNotificationsConfig_Email(t *testing.T) {
	clearNotifEnv(t)
	t.Setenv("KUBEBOLT_SMTP_HOST", "smtp.example.com")
	t.Setenv("KUBEBOLT_SMTP_PORT", "465")
	t.Setenv("KUBEBOLT_SMTP_FROM", "alerts@example.com")
	t.Setenv("KUBEBOLT_SMTP_TO", "a@x,  b@y ,c@z")
	t.Setenv("KUBEBOLT_SMTP_DIGEST_MODE", "hourly")

	cfg := LoadNotificationsConfig()
	if !cfg.Email.Enabled() {
		t.Error("email should be enabled with host+from+to")
	}
	if cfg.Email.Port != 465 {
		t.Errorf("port = %d", cfg.Email.Port)
	}
	if len(cfg.Email.To) != 3 {
		t.Errorf("recipients count = %d, want 3", len(cfg.Email.To))
	}
	if cfg.Email.To[0] != "a@x" || cfg.Email.To[1] != "b@y" || cfg.Email.To[2] != "c@z" {
		t.Errorf("recipients = %v", cfg.Email.To)
	}
	if cfg.Email.DigestMode != "hourly" {
		t.Errorf("digest mode = %q", cfg.Email.DigestMode)
	}
}

func TestLoadNotificationsConfig_EmailDisabledWhenIncomplete(t *testing.T) {
	clearNotifEnv(t)
	t.Setenv("KUBEBOLT_SMTP_HOST", "smtp.example.com")
	// Missing from + to
	cfg := LoadNotificationsConfig()
	if cfg.Email.Enabled() {
		t.Error("email without from/to should be disabled")
	}
}

func TestLoadNotificationsConfig_MinSeverity(t *testing.T) {
	cases := map[string]string{
		"":         "warning",
		"critical": "critical",
		"warning":  "warning",
		"info":     "info",
		"weird":    "warning", // invalid → default
	}
	for input, want := range cases {
		clearNotifEnv(t)
		t.Setenv("KUBEBOLT_NOTIFICATIONS_MIN_SEVERITY", input)
		cfg := LoadNotificationsConfig()
		if cfg.MinSeverity != want {
			t.Errorf("input %q → MinSeverity %q, want %q", input, cfg.MinSeverity, want)
		}
	}
}

func TestLoadNotificationsConfig_Cooldown(t *testing.T) {
	clearNotifEnv(t)
	t.Setenv("KUBEBOLT_NOTIFICATIONS_COOLDOWN", "5m")
	cfg := LoadNotificationsConfig()
	if cfg.Cooldown != 5*time.Minute {
		t.Errorf("cooldown = %v, want 5m", cfg.Cooldown)
	}

	// Invalid duration should fall back to default
	t.Setenv("KUBEBOLT_NOTIFICATIONS_COOLDOWN", "not-a-duration")
	cfg = LoadNotificationsConfig()
	if cfg.Cooldown != time.Hour {
		t.Errorf("bad duration should default to 1h; got %v", cfg.Cooldown)
	}
}

func TestLoadNotificationsConfig_MasterDisabled(t *testing.T) {
	clearNotifEnv(t)
	t.Setenv("KUBEBOLT_NOTIFICATIONS_ENABLED", "false")
	cfg := LoadNotificationsConfig()
	if cfg.MasterEnabled {
		t.Error("master should be disabled when env=false")
	}
}

func TestParseBoolDefault(t *testing.T) {
	cases := []struct {
		input string
		def   bool
		want  bool
	}{
		{"", true, true},
		{"", false, false},
		{"true", false, true},
		{"TRUE", false, true},
		{"yes", false, true},
		{"1", false, true},
		{"on", false, true},
		{"false", true, false},
		{"no", true, false},
		{"0", true, false},
		{"off", true, false},
		{"weird", true, true},   // fallback to default
		{"weird", false, false}, // fallback to default
	}
	for _, c := range cases {
		got := parseBoolDefault(c.input, c.def)
		if got != c.want {
			t.Errorf("parseBoolDefault(%q, %v) = %v, want %v", c.input, c.def, got, c.want)
		}
	}
}
