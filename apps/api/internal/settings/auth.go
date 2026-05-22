package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Spec #09 — Auth is the ONE domain in the Settings runtime that
// REQUIRES a restart to apply. The JWT service, token TTLs, and the
// "is auth enabled at all" decision are wired into the request pipeline
// at boot; swapping them mid-process would invalidate in-flight
// sessions, leak goroutines, and risk subtle races with the auth
// middleware that's already mounted on every route. So the model is:
//
//   1. PUT writes to BoltDB and returns 200 with pendingRestart=true.
//   2. UI surfaces a "Pending restart" banner and a "Restart now" button.
//   3. On next process boot, LoadAuthConfig-derived env merges with the
//      stored override; the resolved values seed the JWT service.
//
// JWT secret is intentionally NOT exposed for UI editing. Rotating it
// instantly invalidates every refresh token AND every secret encrypted
// by the settings.crypto layer (Copilot API keys, webhook URLs).
// Operators rotate via KUBEBOLT_JWT_SECRET + redeploy — same as today.

const authSettingsKey = "auth"

// StoredAuthSettings is the on-disk shape for the Auth domain. Every
// field is a pointer so nil means "fall back to env baseline". Only
// the safe-to-change subset of AuthConfig is exposed:
//   - Enabled: master "auth on/off" switch (one-way useful only when
//     env said enabled=true at boot; flipping to false stays in effect
//     until next reboot).
//   - AccessTokenExpirySeconds / RefreshTokenExpirySeconds: token TTLs.
//
// JWT secret, DataDir, initial admin password — NOT here. Each has a
// reason (security, filesystem path, boot-only seed) that makes them
// inappropriate for UI editing.
type StoredAuthSettings struct {
	Enabled                    *bool `json:"enabled,omitempty"`
	AccessTokenExpirySeconds   *int  `json:"accessTokenExpirySeconds,omitempty"`
	RefreshTokenExpirySeconds  *int  `json:"refreshTokenExpirySeconds,omitempty"`
}

// Auth returns the live resolved AuthConfig (env baseline + BoltDB
// override merged). Cached until InvalidateAuth is called on PUT.
//
// Important: this is the "what would auth look like if we restarted
// right now" config — NOT what the running JWT service is using. The
// JWT service was built from r.authBootSnapshot. Compare via the
// pendingRestart flag in MaskedAuth.
func (r *Runtime) Auth() config.AuthConfig {
	r.mu.RLock()
	if r.authValid {
		cfg := r.auth
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.authValid {
		return r.auth
	}
	r.auth = r.resolveAuthLocked()
	r.authValid = true
	return r.auth
}

func (r *Runtime) InvalidateAuth() {
	r.mu.Lock()
	r.authValid = false
	r.mu.Unlock()
}

// CaptureAuthBootSnapshot records the resolved Auth config as the
// "this is what the running process was built from" baseline. Called
// once at boot AFTER all subsystems have been wired with the resolved
// auth values. The pendingRestart computation diffs the live resolved
// auth (Auth()) against this snapshot.
//
// Must be called before any PUT /admin/settings/auth could fire, so the
// pendingRestart flag has a stable baseline to compare against.
func (r *Runtime) CaptureAuthBootSnapshot() {
	resolved := r.Auth()
	r.mu.Lock()
	r.authBootSnapshot = resolved
	r.authBootCaptured = true
	r.mu.Unlock()
}

// AuthBootSnapshot returns the boot-time resolved auth config used by
// the live JWT service.
func (r *Runtime) AuthBootSnapshot() (config.AuthConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authBootSnapshot, r.authBootCaptured
}

func (r *Runtime) resolveAuthLocked() config.AuthConfig {
	cfg := r.envAuth

	raw, err := r.store.GetSetting(authSettingsKey)
	if err != nil {
		return cfg
	}
	var stored StoredAuthSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		return cfg
	}
	applyStoredAuth(&cfg, &stored)
	return cfg
}

func applyStoredAuth(cfg *config.AuthConfig, stored *StoredAuthSettings) {
	if stored.Enabled != nil {
		cfg.Enabled = *stored.Enabled
	}
	if stored.AccessTokenExpirySeconds != nil && *stored.AccessTokenExpirySeconds > 0 {
		cfg.AccessTokenExpiry = time.Duration(*stored.AccessTokenExpirySeconds) * time.Second
	}
	if stored.RefreshTokenExpirySeconds != nil && *stored.RefreshTokenExpirySeconds > 0 {
		cfg.RefreshTokenExpiry = time.Duration(*stored.RefreshTokenExpirySeconds) * time.Second
	}
}

// PutAuth validates and persists a partial Auth settings patch. No
// hot reload — the JWT service in memory keeps its boot-time config
// until the process restarts. PUT just updates BoltDB; the next
// resolved Auth() differs from the boot snapshot → pendingRestart=true.
func (r *Runtime) PutAuth(patch *StoredAuthSettings) error {
	if err := validateAuthPatch(patch); err != nil {
		return err
	}
	existing, _ := r.loadAuth()
	merged := mergeAuth(existing, *patch)

	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := r.store.SetSetting(authSettingsKey, encoded); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	r.InvalidateAuth()
	return nil
}

func (r *Runtime) ResetAuth() error {
	if err := r.store.SetSetting(authSettingsKey, []byte("null")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	r.InvalidateAuth()
	return nil
}

func (r *Runtime) loadAuth() (StoredAuthSettings, error) {
	raw, err := r.store.GetSetting(authSettingsKey)
	if err != nil {
		return StoredAuthSettings{}, nil
	}
	var out StoredAuthSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return StoredAuthSettings{}, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// ─── Masked render ────────────────────────────────────────────────────

// MaskedAuth is the GET response shape. Includes the boot snapshot so
// the UI can render a side-by-side "running vs pending" comparison
// when a change is staged.
type MaskedAuth struct {
	Effective       MaskedEffectiveAuth `json:"effective"`
	BootSnapshot    MaskedEffectiveAuth `json:"bootSnapshot"`
	Stored          MaskedStoredAuth    `json:"stored"`
	PendingRestart  bool                `json:"pendingRestart"`
	// JWTSecretFromEnv tells the UI whether KUBEBOLT_JWT_SECRET was
	// set via env at boot. When false, the secret was auto-generated +
	// persisted in BoltDB; the operator might want to rotate it via env.
	JWTSecretFromEnv bool `json:"jwtSecretFromEnv"`
	// JWTSecretMasked is a UI-safe preview (e.g. "f3a8b1***c204") via
	// the same maskSecret helper that masks Copilot API keys and
	// webhook URLs. Surfaces the secret's "identity" so operators can
	// verify which one is in effect without leaking enough to brute
	// force. Empty when no secret is set (shouldn't happen in practice
	// — main.go either reads it from env, loads from DB, or generates).
	JWTSecretMasked string `json:"jwtSecretMasked,omitempty"`
}

type MaskedEffectiveAuth struct {
	Enabled                   bool `json:"enabled"`
	AccessTokenExpirySeconds  int  `json:"accessTokenExpirySeconds"`
	RefreshTokenExpirySeconds int  `json:"refreshTokenExpirySeconds"`
}

type MaskedStoredAuth struct {
	HasOverride               bool  `json:"hasOverride"`
	Enabled                   *bool `json:"enabled,omitempty"`
	AccessTokenExpirySeconds  *int  `json:"accessTokenExpirySeconds,omitempty"`
	RefreshTokenExpirySeconds *int  `json:"refreshTokenExpirySeconds,omitempty"`
}

func (r *Runtime) RenderMaskedAuth() (MaskedAuth, error) {
	stored, err := r.loadAuth()
	if err != nil {
		return MaskedAuth{}, err
	}
	resolved := r.Auth()
	boot, captured := r.AuthBootSnapshot()
	if !captured {
		// Boot snapshot not yet captured (called before main.go finished
		// wiring) — fall back to the env baseline so pendingRestart can
		// still be computed sensibly. In practice CaptureAuthBootSnapshot
		// runs before the HTTP server binds, so this branch is defensive.
		boot = r.envAuth
	}
	pending := !sameAuth(resolved, boot)

	out := MaskedAuth{
		Effective: MaskedEffectiveAuth{
			Enabled:                   resolved.Enabled,
			AccessTokenExpirySeconds:  int(resolved.AccessTokenExpiry / time.Second),
			RefreshTokenExpirySeconds: int(resolved.RefreshTokenExpiry / time.Second),
		},
		BootSnapshot: MaskedEffectiveAuth{
			Enabled:                   boot.Enabled,
			AccessTokenExpirySeconds:  int(boot.AccessTokenExpiry / time.Second),
			RefreshTokenExpirySeconds: int(boot.RefreshTokenExpiry / time.Second),
		},
		Stored: renderStoredAuthMask(stored),
		PendingRestart:   pending,
		JWTSecretFromEnv: r.envAuth.JWTSecretFromEnv,
		JWTSecretMasked:  maskRandomSecret(string(r.envAuth.JWTSecret)),
	}
	return out, nil
}

func renderStoredAuthMask(s StoredAuthSettings) MaskedStoredAuth {
	out := MaskedStoredAuth{
		Enabled:                   s.Enabled,
		AccessTokenExpirySeconds:  s.AccessTokenExpirySeconds,
		RefreshTokenExpirySeconds: s.RefreshTokenExpirySeconds,
	}
	out.HasOverride = s.Enabled != nil || s.AccessTokenExpirySeconds != nil || s.RefreshTokenExpirySeconds != nil
	return out
}

// sameAuth compares the two AuthConfig values across the UI-editable
// subset only. JWT secret, DataDir, admin password — not part of this
// equality because they're not editable from UI, so they can never
// trigger a pendingRestart through the settings path.
func sameAuth(a, b config.AuthConfig) bool {
	return a.Enabled == b.Enabled &&
		a.AccessTokenExpiry == b.AccessTokenExpiry &&
		a.RefreshTokenExpiry == b.RefreshTokenExpiry
}

// ─── Validation + merge ──────────────────────────────────────────────

func validateAuthPatch(p *StoredAuthSettings) error {
	if p == nil {
		return &ValidationError{Field: "patch", Message: "patch is required"}
	}
	// TTL sanity: access tokens must be short-lived but not zero;
	// refresh tokens must be longer than access tokens or session
	// renewal logic gets weird. Caps prevent extreme typo-tier values
	// (a 100-year access TTL would silently disable rotation).
	if p.AccessTokenExpirySeconds != nil {
		s := *p.AccessTokenExpirySeconds
		if s < 60 || s > 24*60*60 {
			return &ValidationError{Field: "accessTokenExpirySeconds", Message: "must be between 60 (1m) and 86400 (24h)"}
		}
	}
	if p.RefreshTokenExpirySeconds != nil {
		s := *p.RefreshTokenExpirySeconds
		if s < 5*60 || s > 365*24*60*60 {
			return &ValidationError{Field: "refreshTokenExpirySeconds", Message: "must be between 300 (5m) and 31536000 (365d)"}
		}
	}
	if p.AccessTokenExpirySeconds != nil && p.RefreshTokenExpirySeconds != nil {
		if *p.RefreshTokenExpirySeconds <= *p.AccessTokenExpirySeconds {
			return &ValidationError{Field: "refreshTokenExpirySeconds", Message: "must be greater than accessTokenExpirySeconds"}
		}
	}
	return nil
}

func mergeAuth(base, patch StoredAuthSettings) StoredAuthSettings {
	out := base
	if patch.Enabled != nil {
		out.Enabled = patch.Enabled
	}
	if patch.AccessTokenExpirySeconds != nil {
		out.AccessTokenExpirySeconds = patch.AccessTokenExpirySeconds
	}
	if patch.RefreshTokenExpirySeconds != nil {
		out.RefreshTokenExpirySeconds = patch.RefreshTokenExpirySeconds
	}
	return out
}

// errAuthRequiredEnabled is returned when a UI override would leave the
// system in an unbootable state (e.g. mismatched expiries that fail
// re-validation on next boot). Not currently used; reserved for future
// edge cases the validator catches but the merge logic doesn't.
var errAuthRequiredEnabled = errors.New("auth must be enabled to read this setting")
