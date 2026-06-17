package settings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Tests for the runtime settings layer: env baseline + BoltDB override
// merge, secret encryption round-trip, validation, and cache
// invalidation. Crypto + merge math are pure functions, easy to pin.
// The BoltDB-backed paths use a real auth.Store with a tmp file (small
// footprint and matches production usage exactly — no separate mock).

// ─── Test helpers ────────────────────────────────────────────────────

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Env baseline matching the typical helm-installed shape.
	envBase := config.CopilotConfig{
		Primary: config.ProviderConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-6",
			APIKey:   "env-key-from-helm",
		},
		MaxTokens:             4096,
		AutoCompact:           true,
		AutoCompactThreshold:  0.8,
		CompactPreserveTurns:  3,
		ShowToolCalls:         true,
		ActionProgressTimeout: config.DefaultActionProgressTimeout,
		MaxRounds:             config.DefaultMaxRounds,
	}
	envBase.Enabled = true

	// JWT secret must be at least 16 bytes — same constraint as
	// production. Use a fixed value so encryption is deterministic
	// across the test run.
	jwt := []byte("test-jwt-secret-32-bytes-padding-")

	rt, err := NewRuntime(store, store, envBase, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, jwt)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	return rt
}

func strPtr(s string) *string   { return &s }
func intPtr(n int) *int         { return &n }
func boolPtr(b bool) *bool      { return &b }
func floatPtr(f float64) *float64 { return &f }

// ─── Crypto ──────────────────────────────────────────────────────────

func TestSecretCrypto_RoundTrip(t *testing.T) {
	c, err := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	plaintexts := []string{
		"sk-ant-api03-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"sk-proj-short",
		"https://hooks.slack.com/services/T00/B00/XXXX",
	}
	for _, pt := range plaintexts {
		t.Run(pt[:min(10, len(pt))], func(t *testing.T) {
			enc, err := c.encrypt(pt)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			if enc == pt {
				t.Error("ciphertext equals plaintext — encryption no-op")
			}
			if !strings.HasPrefix(enc, secretEnvelopePrefix) {
				t.Errorf("ciphertext missing version prefix: %s", enc)
			}
			dec, err := c.decrypt(enc)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if dec != pt {
				t.Errorf("round-trip mismatch: got %q want %q", dec, pt)
			}
		})
	}
}

func TestSecretCrypto_RejectsShortJWTSecret(t *testing.T) {
	_, err := newSecretCrypto([]byte("too-short"))
	if err == nil {
		t.Fatal("expected error for short secret")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("error should mention 'too short': %v", err)
	}
}

func TestSecretCrypto_DetectsKeyRotation(t *testing.T) {
	// Encrypt with one key, try to decrypt with a different key — the
	// AES-GCM authentication tag fails, surface a typed error so the
	// handler can render the "re-enter your key" UX.
	c1, _ := newSecretCrypto([]byte("first-jwt-secret-with-32-chars--"))
	c2, _ := newSecretCrypto([]byte("second-jwt-secret-other-32-chars"))
	enc, err := c1.encrypt("sk-original-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = c2.decrypt(enc)
	if err == nil {
		t.Fatal("expected decrypt to fail with different key")
	}
	if !IsSecretUnreadable(err) {
		t.Errorf("expected IsSecretUnreadable to match, got: %v", err)
	}
}

func TestSecretCrypto_EmptyPassThrough(t *testing.T) {
	c, _ := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	enc, err := c.encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if enc != "" {
		t.Errorf("empty encrypt should return empty, got %q", enc)
	}
	dec, err := c.decrypt("")
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if dec != "" {
		t.Errorf("empty decrypt should return empty, got %q", dec)
	}
}

func TestSecretCrypto_RejectsMalformedEnvelope(t *testing.T) {
	c, _ := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	_, err := c.decrypt("not-an-envelope-no-prefix")
	if err == nil {
		t.Fatal("expected error for missing prefix")
	}
}

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short", "abc12", "***"},
		{"typical anthropic key", "sk-ant-api03-xxxxxxxxxxxxxxxxxxxxabcd", "sk-ant-***abcd"},
		{"typical openai key", "sk-proj-1234567890abcdef1234567890wxyz", "sk-proj***wxyz"},
		{"short configured value", "shortish", "***"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maskSecret(c.in); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// ─── Resolution: env baseline + BoltDB overrides ─────────────────────

func TestCopilot_NoOverride_ReturnsEnvBaseline(t *testing.T) {
	rt := newTestRuntime(t)
	cfg := rt.Copilot(context.Background())
	if cfg.Primary.Provider != "anthropic" {
		t.Errorf("provider: got %q want anthropic", cfg.Primary.Provider)
	}
	if cfg.Primary.Model != "claude-sonnet-4-6" {
		t.Errorf("model: got %q want claude-sonnet-4-6", cfg.Primary.Model)
	}
	if cfg.Primary.APIKey != "env-key-from-helm" {
		t.Errorf("api key should fall back to env baseline")
	}
}

func TestCopilot_PartialOverride_OnlyChangesSetFields(t *testing.T) {
	rt := newTestRuntime(t)
	// Admin sets only the model in UI — provider and key should remain from env.
	patch := &StoredCopilotSettings{
		Primary: &StoredProviderSettings{
			Model: strPtr("claude-opus-4-7"),
		},
	}
	if err := rt.PutCopilot(context.Background(), patch, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	cfg := rt.Copilot(context.Background())
	if cfg.Primary.Model != "claude-opus-4-7" {
		t.Errorf("model not overridden: got %q", cfg.Primary.Model)
	}
	if cfg.Primary.Provider != "anthropic" {
		t.Errorf("provider should inherit from env: got %q", cfg.Primary.Provider)
	}
	if cfg.Primary.APIKey != "env-key-from-helm" {
		t.Errorf("api key should inherit from env: got %q", cfg.Primary.APIKey)
	}
}

func TestCopilot_APIKeyOverride_EncryptsAtRest(t *testing.T) {
	rt := newTestRuntime(t)
	plaintextKey := "sk-ant-api03-new-key-from-ui"
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{}, strPtr(plaintextKey), nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Effective config sees the new key.
	cfg := rt.Copilot(context.Background())
	if cfg.Primary.APIKey != plaintextKey {
		t.Errorf("effective key: got %q want %q", cfg.Primary.APIKey, plaintextKey)
	}
	// Raw stored record has the encrypted envelope, not the plaintext.
	// Copilot is a per-org setting now, so it lives in the org store.
	raw, _ := rt.orgStore.GetOrgSetting(context.Background(), copilotSettingsKey)
	if strings.Contains(string(raw), plaintextKey) {
		t.Error("plaintext key leaked into BoltDB record — encryption skipped")
	}
	if !strings.Contains(string(raw), secretEnvelopePrefix) {
		t.Error("encrypted envelope missing version prefix in stored record")
	}
}

func TestCopilot_Reset_FallsBackToEnv(t *testing.T) {
	rt := newTestRuntime(t)
	// Set an override, then reset.
	patch := &StoredCopilotSettings{
		Primary: &StoredProviderSettings{Model: strPtr("claude-opus-4-7")},
	}
	if err := rt.PutCopilot(context.Background(), patch, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := rt.Copilot(context.Background()).Primary.Model; got != "claude-opus-4-7" {
		t.Fatalf("setup: expected override applied, got %q", got)
	}
	if err := rt.ResetCopilot(context.Background()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := rt.Copilot(context.Background()).Primary.Model; got != "claude-sonnet-4-6" {
		t.Errorf("after reset, model should be env baseline %q, got %q", "claude-sonnet-4-6", got)
	}
}

func TestCopilot_ActionProgressTimeout_OverrideAndFloor(t *testing.T) {
	// No override → env baseline (90s).
	rt := newTestRuntime(t)
	if got := rt.Copilot(context.Background()).ActionProgressTimeout; got != config.DefaultActionProgressTimeout {
		t.Fatalf("no override: got %v, want env baseline %v", got, config.DefaultActionProgressTimeout)
	}

	// Valid override (120s, sent as 120000 ms) is applied verbatim.
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{ActionProgressTimeoutMs: intPtr(120000)}, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := rt.Copilot(context.Background()).ActionProgressTimeout; got != 120*time.Second {
		t.Errorf("override: got %v, want 2m", got)
	}

	// Sub-floor override (5s) passes validation (>0) but is clamped to the
	// floor in applyStoredCopilot — same defence the env path has.
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{ActionProgressTimeoutMs: intPtr(5000)}, nil, nil); err != nil {
		t.Fatalf("put sub-floor: %v", err)
	}
	if got := rt.Copilot(context.Background()).ActionProgressTimeout; got != config.MinActionProgressTimeout {
		t.Errorf("sub-floor: got %v, want clamp to %v", got, config.MinActionProgressTimeout)
	}

	// The effective value is surfaced (in ms) by the masked render.
	masked, err := rt.RenderMaskedCopilot(context.Background())
	if err != nil {
		t.Fatalf("render masked: %v", err)
	}
	if masked.Effective.ActionProgressTimeoutMs != int(config.MinActionProgressTimeout.Milliseconds()) {
		t.Errorf("masked effective: got %d ms, want %d", masked.Effective.ActionProgressTimeoutMs, config.MinActionProgressTimeout.Milliseconds())
	}
}

func TestCopilot_MaxRounds_OverrideAndClamp(t *testing.T) {
	// No override → env baseline (DefaultMaxRounds).
	rt := newTestRuntime(t)
	if got := rt.Copilot(context.Background()).MaxRounds; got != config.DefaultMaxRounds {
		t.Fatalf("no override: got %d, want env baseline %d", got, config.DefaultMaxRounds)
	}

	// In-range override applied verbatim.
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{MaxRounds: intPtr(30)}, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := rt.Copilot(context.Background()).MaxRounds; got != 30 {
		t.Errorf("override: got %d, want 30", got)
	}

	// Above the ceiling → clamped to MaxMaxRounds (passes validation since >0).
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{MaxRounds: intPtr(999)}, nil, nil); err != nil {
		t.Fatalf("put over-ceiling: %v", err)
	}
	if got := rt.Copilot(context.Background()).MaxRounds; got != config.MaxMaxRounds {
		t.Errorf("over-ceiling: got %d, want clamp to %d", got, config.MaxMaxRounds)
	}

	// Below the floor (but >0) → clamped up to MinMaxRounds.
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{MaxRounds: intPtr(1)}, nil, nil); err != nil {
		t.Fatalf("put sub-floor: %v", err)
	}
	if got := rt.Copilot(context.Background()).MaxRounds; got != config.MinMaxRounds {
		t.Errorf("sub-floor: got %d, want clamp to %d", got, config.MinMaxRounds)
	}

	// Effective value is surfaced by the masked render.
	masked, err := rt.RenderMaskedCopilot(context.Background())
	if err != nil {
		t.Fatalf("render masked: %v", err)
	}
	if masked.Effective.MaxRounds != config.MinMaxRounds {
		t.Errorf("masked effective: got %d, want %d", masked.Effective.MaxRounds, config.MinMaxRounds)
	}

	// Zero/negative is rejected at validation (the UI must surface an error,
	// not silently floor a typo).
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{MaxRounds: intPtr(0)}, nil, nil); err == nil {
		t.Error("maxRounds=0 should fail validation")
	}
}

func TestCopilot_InvalidationOnPut(t *testing.T) {
	rt := newTestRuntime(t)
	// The cache keys per org; context.Background() resolves to the default
	// tenant (single-tenant OSS shape).
	org := auth.DefaultTenantName
	// Prime the cache with the env baseline read.
	_ = rt.Copilot(context.Background())
	if !rt.copilotValid[org] {
		t.Fatal("cache should be valid after first read")
	}
	// PUT must invalidate.
	patch := &StoredCopilotSettings{
		Primary: &StoredProviderSettings{Model: strPtr("claude-opus-4-7")},
	}
	if err := rt.PutCopilot(context.Background(), patch, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if rt.copilotValid[org] {
		t.Error("cache should be invalidated after PUT")
	}
}

// TestCopilot_PerOrgIsolation proves two orgs get independent Copilot config:
// org A's API key never bleeds into org B's resolved config, and B's cache
// isn't poisoned by A's read. The §9 "cache bleed" guardrail. Exercises the
// Bolt org-store keying + the per-org Runtime cache (RLS enforces the same at
// the engine level in EE; this covers the edition-neutral logic).
func TestCopilot_PerOrgIsolation(t *testing.T) {
	rt := newTestRuntime(t)
	ctxA := auth.WithTenantID(context.Background(), "org-a")
	ctxB := auth.WithTenantID(context.Background(), "org-b")

	if err := rt.PutCopilot(ctxA, &StoredCopilotSettings{}, strPtr("sk-ant-org-a-key"), nil); err != nil {
		t.Fatalf("put A: %v", err)
	}
	if err := rt.PutCopilot(ctxB, &StoredCopilotSettings{}, strPtr("sk-ant-org-b-key"), nil); err != nil {
		t.Fatalf("put B: %v", err)
	}

	if got := rt.Copilot(ctxA).Primary.APIKey; got != "sk-ant-org-a-key" {
		t.Errorf("org A key: got %q want sk-ant-org-a-key", got)
	}
	if got := rt.Copilot(ctxB).Primary.APIKey; got != "sk-ant-org-b-key" {
		t.Errorf("org B key: got %q want sk-ant-org-b-key", got)
	}

	// A third org with no override falls back to the env baseline — never to
	// another org's stored key.
	ctxC := auth.WithTenantID(context.Background(), "org-c")
	if got := rt.Copilot(ctxC).Primary.APIKey; got != "env-key-from-helm" {
		t.Errorf("org C (no override) key: got %q want env baseline", got)
	}

	// Resetting A must not touch B.
	if err := rt.ResetCopilot(ctxA); err != nil {
		t.Fatalf("reset A: %v", err)
	}
	if got := rt.Copilot(ctxA).Primary.APIKey; got != "env-key-from-helm" {
		t.Errorf("org A after reset: got %q want env baseline", got)
	}
	if got := rt.Copilot(ctxB).Primary.APIKey; got != "sk-ant-org-b-key" {
		t.Errorf("org B after A reset: got %q want sk-ant-org-b-key (bleed!)", got)
	}
}

// TestGeneral_PerOrgAndPlatformSplit proves the MIXED General domain: ORG fields
// (ProdNamespacePattern, DefaultRefresh) are per-org, while PLATFORM fields
// (UpdateCheckEnabled, CacheSyncTimeoutSeconds) are install-global — one value
// shared by every org and reflected by GeneralGlobal().
func TestGeneral_PerOrgAndPlatformSplit(t *testing.T) {
	rt := newTestRuntime(t)
	ctxA := auth.WithTenantID(context.Background(), "org-a")
	ctxB := auth.WithTenantID(context.Background(), "org-b")

	// Org A sets an ORG field (prod-ns pattern) + a PLATFORM field (cache sync).
	if err := rt.PutGeneral(ctxA, &StoredGeneralSettings{
		ProdNamespacePattern:    strPtr("^prod-a-.*$"),
		CacheSyncTimeoutSeconds: intPtr(90),
	}); err != nil {
		t.Fatalf("put A: %v", err)
	}
	// Org B sets a different ORG field value, no platform field.
	if err := rt.PutGeneral(ctxB, &StoredGeneralSettings{
		ProdNamespacePattern: strPtr("^prod-b-.*$"),
	}); err != nil {
		t.Fatalf("put B: %v", err)
	}

	// ORG field is isolated per org.
	if got := rt.General(ctxA).ProdNamespacePattern; got != "^prod-a-.*$" {
		t.Errorf("org A prodNs: got %q", got)
	}
	if got := rt.General(ctxB).ProdNamespacePattern; got != "^prod-b-.*$" {
		t.Errorf("org B prodNs: got %q", got)
	}

	// PLATFORM field is GLOBAL: org A set it, but org B + GeneralGlobal see it too.
	if got := rt.General(ctxA).CacheSyncTimeoutSeconds; got != 90 {
		t.Errorf("org A cacheSync: got %d want 90", got)
	}
	if got := rt.General(ctxB).CacheSyncTimeoutSeconds; got != 90 {
		t.Errorf("org B cacheSync (platform is shared): got %d want 90", got)
	}
	if got := rt.GeneralGlobal().CacheSyncTimeoutSeconds; got != 90 {
		t.Errorf("GeneralGlobal cacheSync: got %d want 90", got)
	}
	// GeneralGlobal does NOT carry org overrides — prodNs stays the env baseline.
	if got := rt.GeneralGlobal().ProdNamespacePattern; got == "^prod-a-.*$" || got == "^prod-b-.*$" {
		t.Errorf("GeneralGlobal must not leak a per-org ORG field, got %q", got)
	}

	// Reset org A's ORG override → back to env baseline; platform + org B intact.
	if err := rt.ResetGeneral(ctxA); err != nil {
		t.Fatalf("reset A: %v", err)
	}
	if got := rt.General(ctxA).ProdNamespacePattern; got == "^prod-a-.*$" {
		t.Errorf("org A prodNs should be reset, got %q", got)
	}
	if got := rt.General(ctxA).CacheSyncTimeoutSeconds; got != 90 {
		t.Errorf("org A cacheSync after ORG reset should persist (platform): got %d", got)
	}
	if got := rt.General(ctxB).ProdNamespacePattern; got != "^prod-b-.*$" {
		t.Errorf("org B prodNs after A reset (bleed!): got %q", got)
	}
}

// TestNotifications_PerOrgIsolation proves each org's notifications config is
// independent: org A's Slack/severity never bleeds into org B, and an org with
// no override falls back to the env baseline.
func TestNotifications_PerOrgIsolation(t *testing.T) {
	rt := newTestRuntime(t)
	ctxA := auth.WithTenantID(context.Background(), "org-a")
	ctxB := auth.WithTenantID(context.Background(), "org-b")

	if err := rt.PutNotifications(ctxA, &StoredNotificationsSettings{
		Global: &StoredNotificationsGlobal{MinSeverity: strPtr("critical")},
	}, strPtr("https://hooks.slack.com/services/AAA/BBB/orgAtoken"), nil, nil); err != nil {
		t.Fatalf("put A: %v", err)
	}
	if err := rt.PutNotifications(ctxB, &StoredNotificationsSettings{
		Global: &StoredNotificationsGlobal{MinSeverity: strPtr("info")},
	}, strPtr("https://hooks.slack.com/services/CCC/DDD/orgBtoken"), nil, nil); err != nil {
		t.Fatalf("put B: %v", err)
	}

	a := rt.Notifications(ctxA)
	b := rt.Notifications(ctxB)
	if a.SlackWebhookURL == b.SlackWebhookURL {
		t.Fatalf("org A and B share a Slack URL: %q", a.SlackWebhookURL)
	}
	if a.MinSeverity != "critical" {
		t.Errorf("org A minSeverity: got %q want critical", a.MinSeverity)
	}
	if b.MinSeverity != "info" {
		t.Errorf("org B minSeverity: got %q want info", b.MinSeverity)
	}

	// Org C (no override) → env baseline (no slack URL configured).
	ctxC := auth.WithTenantID(context.Background(), "org-c")
	if got := rt.Notifications(ctxC).SlackWebhookURL; got == a.SlackWebhookURL || got == b.SlackWebhookURL {
		t.Errorf("org C (no override) leaked another org's Slack URL: %q", got)
	}

	// Reset A → its Slack override clears; B intact.
	if err := rt.ResetNotifications(ctxA); err != nil {
		t.Fatalf("reset A: %v", err)
	}
	if got := rt.Notifications(ctxA).SlackWebhookURL; got != "" {
		t.Errorf("org A Slack after reset: got %q want empty", got)
	}
	if got := rt.Notifications(ctxB).SlackWebhookURL; got != b.SlackWebhookURL {
		t.Errorf("org B Slack after A reset (bleed!): got %q", got)
	}
}

// ─── Validation ──────────────────────────────────────────────────────

func TestCopilot_Validation(t *testing.T) {
	cases := []struct {
		name      string
		patch     *StoredCopilotSettings
		wantField string
	}{
		{
			"unknown provider",
			&StoredCopilotSettings{Primary: &StoredProviderSettings{Provider: strPtr("not-a-provider")}},
			"primary.provider",
		},
		{
			"negative max tokens",
			&StoredCopilotSettings{MaxTokens: intPtr(-1)},
			"maxTokens",
		},
		{
			"threshold out of range high",
			&StoredCopilotSettings{AutoCompactThreshold: floatPtr(1.5)},
			"autoCompactThreshold",
		},
		{
			"threshold out of range low",
			&StoredCopilotSettings{AutoCompactThreshold: floatPtr(0)},
			"autoCompactThreshold",
		},
		{
			"negative budget",
			&StoredCopilotSettings{SessionBudgetTokens: intPtr(-100)},
			"sessionBudgetTokens",
		},
		{
			"zero action timeout",
			&StoredCopilotSettings{ActionProgressTimeoutMs: intPtr(0)},
			"actionProgressTimeoutMs",
		},
	}
	rt := newTestRuntime(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := rt.PutCopilot(context.Background(), c.patch, nil, nil)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !IsValidation(err) {
				t.Errorf("expected ValidationError, got %T: %v", err, err)
			}
			var ve *ValidationError
			_ = errorsAs(err, &ve)
			if ve == nil || ve.Field != c.wantField {
				t.Errorf("expected field %q, got %+v", c.wantField, ve)
			}
		})
	}
}

func TestCopilot_BoolFalse_OverridesEnvTrue(t *testing.T) {
	// Pointer-field semantics: false (not zero) IS a meaningful override.
	// Env says showToolCalls=true; UI explicitly disables it.
	rt := newTestRuntime(t)
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{ShowToolCalls: boolPtr(false)}, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if rt.Copilot(context.Background()).ShowToolCalls {
		t.Error("showToolCalls=false override ignored — env true bled through")
	}
}

// ─── Masked render ───────────────────────────────────────────────────

func TestRenderMaskedCopilot_SecretsNeverLeak(t *testing.T) {
	rt := newTestRuntime(t)
	plaintextKey := "sk-ant-api03-very-secret-key-here-abcd"
	if err := rt.PutCopilot(context.Background(), &StoredCopilotSettings{}, strPtr(plaintextKey), nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	masked, err := rt.RenderMaskedCopilot(context.Background())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Effective view masks the plaintext.
	if masked.Effective.APIKeyMasked == plaintextKey {
		t.Error("plaintext key leaked into masked response")
	}
	if !strings.Contains(masked.Effective.APIKeyMasked, "***") {
		t.Errorf("masked output should contain ***, got %q", masked.Effective.APIKeyMasked)
	}
	if !masked.SecretsReadable {
		t.Error("secrets should be readable with the original key")
	}
	if masked.Stored.Primary == nil || !masked.Stored.Primary.APIKeyConfigured {
		t.Error("stored.Primary.APIKeyConfigured should be true")
	}
}

// errorsAs wraps stdlib errors.As so the test stays self-contained
// without importing the stdlib package at the top (it's only used here).
func errorsAs(err error, target any) bool {
	type asable interface {
		As(any) bool
	}
	_ = err
	_ = target
	// Use stdlib via reflect-like trick: just call errors.As if it's in
	// the import graph. To keep this file minimal, redirect to a stub —
	// but that's overkill. Just use errors.As directly from stdlib.
	return errorsAsStd(err, target)
}

// errorsAsStd is a thin shim around stdlib errors.As so we can keep the
// test file's import list shorter at the top. Avoids adding a package-
// level `import "errors"` that would only be used here.
func errorsAsStd(err error, target any) bool {
	// stdlib import sits in copilot.go already (transitive); using it
	// directly here would require the import at the file level. Inline
	// the type-assertion manually for *ValidationError, the only target
	// this test file cares about.
	ve, ok := err.(*ValidationError)
	if !ok {
		return false
	}
	if t, ok := target.(**ValidationError); ok {
		*t = ve
		return true
	}
	return false
}

// min is a tiny inlined helper to avoid pulling stdlib math.Min for ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Sanity-check that the auth.Store interface we're depending on still
// has the methods we use (GetSetting + SetSetting). A test that fails
// to compile is a clearer signal than a runtime panic.
func TestStoreInterfaceCompiles(t *testing.T) {
	_ = filepath.Clean
	_ = os.Getenv
	// Compile-time only.
	var s *auth.Store
	if s != nil {
		_, _ = s.GetSetting("x")
		_ = s.SetSetting("x", nil)
	}
}
