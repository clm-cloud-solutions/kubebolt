package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		MaxTokens:            4096,
		AutoCompact:          true,
		AutoCompactThreshold: 0.8,
		CompactPreserveTurns: 3,
		ShowToolCalls:        true,
	}
	envBase.Enabled = true

	// JWT secret must be at least 16 bytes — same constraint as
	// production. Use a fixed value so encryption is deterministic
	// across the test run.
	jwt := []byte("test-jwt-secret-32-bytes-padding-")

	rt, err := NewRuntime(store, envBase, config.NotificationsConfig{}, config.AuthConfig{}, jwt)
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
	cfg := rt.Copilot()
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
	if err := rt.PutCopilot(patch, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	cfg := rt.Copilot()
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
	if err := rt.PutCopilot(&StoredCopilotSettings{}, strPtr(plaintextKey), nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Effective config sees the new key.
	cfg := rt.Copilot()
	if cfg.Primary.APIKey != plaintextKey {
		t.Errorf("effective key: got %q want %q", cfg.Primary.APIKey, plaintextKey)
	}
	// Raw stored record has the encrypted envelope, not the plaintext.
	raw, _ := rt.store.GetSetting(copilotSettingsKey)
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
	if err := rt.PutCopilot(patch, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := rt.Copilot().Primary.Model; got != "claude-opus-4-7" {
		t.Fatalf("setup: expected override applied, got %q", got)
	}
	if err := rt.ResetCopilot(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := rt.Copilot().Primary.Model; got != "claude-sonnet-4-6" {
		t.Errorf("after reset, model should be env baseline %q, got %q", "claude-sonnet-4-6", got)
	}
}

func TestCopilot_InvalidationOnPut(t *testing.T) {
	rt := newTestRuntime(t)
	// Prime the cache with the env baseline read.
	_ = rt.Copilot()
	if !rt.copilotValid {
		t.Fatal("cache should be valid after first read")
	}
	// PUT must invalidate.
	patch := &StoredCopilotSettings{
		Primary: &StoredProviderSettings{Model: strPtr("claude-opus-4-7")},
	}
	if err := rt.PutCopilot(patch, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if rt.copilotValid {
		t.Error("cache should be invalidated after PUT")
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
	}
	rt := newTestRuntime(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := rt.PutCopilot(c.patch, nil, nil)
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
	if err := rt.PutCopilot(&StoredCopilotSettings{ShowToolCalls: boolPtr(false)}, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if rt.Copilot().ShowToolCalls {
		t.Error("showToolCalls=false override ignored — env true bled through")
	}
}

// ─── Masked render ───────────────────────────────────────────────────

func TestRenderMaskedCopilot_SecretsNeverLeak(t *testing.T) {
	rt := newTestRuntime(t)
	plaintextKey := "sk-ant-api03-very-secret-key-here-abcd"
	if err := rt.PutCopilot(&StoredCopilotSettings{}, strPtr(plaintextKey), nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	masked, err := rt.RenderMaskedCopilot()
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
