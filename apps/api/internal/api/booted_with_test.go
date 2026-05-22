package api

import "testing"

// TestIsSensitiveEnv pins the default-deny contract: any KUBEBOLT_*
// var whose name carries a credential-shaped substring is redacted,
// known-benign exceptions (token counts, audience identifiers) are
// explicitly allowlisted. Regressions here would silently leak
// secrets — guard the boundary.
func TestIsSensitiveEnv(t *testing.T) {
	cases := []struct {
		name      string
		want      bool
		rationale string
	}{
		// Known secrets — these MUST be redacted.
		{"KUBEBOLT_JWT_SECRET", true, "JWT signing key"},
		{"KUBEBOLT_ADMIN_PASSWORD", true, "first-boot admin seed"},
		{"KUBEBOLT_AI_API_KEY", true, "Anthropic / OpenAI key"},
		{"KUBEBOLT_AI_FALLBACK_API_KEY", true, "fallback provider key"},
		{"KUBEBOLT_SLACK_WEBHOOK_URL", true, "channel-write token in URL"},
		{"KUBEBOLT_DISCORD_WEBHOOK_URL", true, "channel-write token in URL"},
		{"KUBEBOLT_SMTP_PASSWORD", true, "SMTP-AUTH password"},
		{"KUBEBOLT_RESET_ADMIN_PASSWORD", true, "forgot-password recovery value"},
		{"KUBEBOLT_INTERNAL_TOKEN", true, "any *_TOKEN should redact by default"},
		{"KUBEBOLT_FUTURE_API_KEY", true, "any *_API_KEY should redact"},
		{"KUBEBOLT_DB_CREDENTIAL", true, "any *_CREDENTIAL should redact"},
		{"KUBEBOLT_PRIVATE_KEY_PATH", true, "any *_PRIVATE_* should redact"},

		// Known-benign substring hits — explicit allowlist entries.
		{"KUBEBOLT_AGENT_TOKEN_AUDIENCE", false, "JWT audience name, not a credential"},
		{"KUBEBOLT_AI_MAX_TOKENS", false, "output-token COUNT, not a credential"},
		{"KUBEBOLT_AI_SESSION_BUDGET_TOKENS", false, "context-window COUNT"},
		{"KUBEBOLT_AI_COMPACT_PRESERVE_TURNS", false, "turn count for auto-compact"},

		// Clearly-not-secret vars — substring rule should not hit.
		{"KUBEBOLT_AUTH_ENABLED", false, "plain flag"},
		{"KUBEBOLT_DATA_DIR", false, "filesystem path"},
		{"KUBEBOLT_DISPLAY_NAME", false, "branding string"},
		{"KUBEBOLT_DEFAULT_REFRESH_INTERVAL_SECONDS", false, "integer"},
		{"KUBEBOLT_NOTIFICATIONS_BASE_URL", false, "public URL, not a webhook token"},
		{"KUBEBOLT_NOTIFICATIONS_COOLDOWN", false, "duration"},
		{"KUBEBOLT_AGENT_TLS_KEY_FILE", true, "private-key file path (heuristic catches it; harmless to redact)"},
	}
	for _, c := range cases {
		got := isSensitiveEnv(c.name)
		if got != c.want {
			t.Errorf("isSensitiveEnv(%q) = %v, want %v (%s)", c.name, got, c.want, c.rationale)
		}
	}
}
