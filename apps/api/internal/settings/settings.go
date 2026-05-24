// Package settings is the runtime-configuration layer that sits on top of
// the env-only config package. It backs UI-edited settings with a BoltDB
// record per domain, falls back to the env-driven baseline when no
// override exists, and exposes hot-reloadable accessors so subsystems
// (Copilot, Notifications, etc.) read live values per request.
//
// Layering, in resolution order:
//
//  1. BoltDB-persisted override (via auth.Store's settings bucket)
//  2. Env-driven baseline (constructed once at boot by config.LoadCopilotConfig)
//  3. Built-in defaults inside the config types themselves
//
// This means env continues to work for operators with config-as-code
// workflows — env is the boot fallback. UI is the runtime authority: the
// moment a domain is configured via UI, its persisted record wins on
// subsequent reads.
//
// Hot reload: every read goes through Runtime.<Domain>() which checks a
// cached resolved value, falling back to a fresh BoltDB read + merge when
// the cache is invalid. PUT handlers invalidate the cache for their
// domain. Subscribers don't need to register — they just call the
// accessor at the start of each operation.
package settings

import (
	"errors"
	"sync"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Runtime is the per-process settings resolver. One instance for the
// lifetime of the server, shared across handlers.
type Runtime struct {
	store *auth.Store

	// envBase / envNotifications / envAuth / envGeneral are the
	// env-driven baselines computed once at boot. Override values from
	// BoltDB merge onto these at resolve time, field by field.
	envBase          config.CopilotConfig
	envNotifications config.NotificationsConfig
	envAuth          config.AuthConfig
	envGeneral       config.GeneralConfig

	// crypto handles encryption of sensitive fields at rest (API keys,
	// webhook secrets). Derived from the JWT secret at boot so the same
	// install can decrypt what it wrote; rotating the JWT secret renders
	// existing encrypted blobs unreadable (documented as expected).
	crypto *secretCrypto

	// Cache. Per-domain `<Domain>` field holds the last resolved struct;
	// the matching `<Domain>Valid` flag toggles to false on PUT so the
	// next read re-resolves from BoltDB. Plain mutex over a sync.Map is
	// fine — read concurrency is high but cache invalidation is rare.
	mu                 sync.RWMutex
	copilot            config.CopilotConfig
	copilotValid       bool
	notifications      config.NotificationsConfig
	notificationsValid bool
	auth               config.AuthConfig
	authValid          bool
	general            config.GeneralConfig
	generalValid       bool
	// authBootSnapshot is the resolved Auth config the running process
	// was actually built from. Set once at boot via
	// CaptureAuthBootSnapshot(); compared against Auth() to compute
	// pendingRestart.
	authBootSnapshot config.AuthConfig
	authBootCaptured bool
}

// NewRuntime wires the Runtime against an auth.Store and the env-driven
// baselines for each domain. The jwtSecret feeds the at-rest secret
// encryption — it must be the same value used to sign tokens, so
// rotation rules are understood as a single concern by operators.
//
// Returns an error if the JWT secret is too short to derive a key from
// (must be at least 16 bytes — anything shorter is a misconfiguration we
// fail loud about rather than silently accept).
func NewRuntime(store *auth.Store, envCopilot config.CopilotConfig, envNotifications config.NotificationsConfig, envAuth config.AuthConfig, envGeneral config.GeneralConfig, jwtSecret []byte) (*Runtime, error) {
	if store == nil {
		return nil, errors.New("settings: store is required")
	}
	crypto, err := newSecretCrypto(jwtSecret)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		store:            store,
		envBase:          envCopilot,
		envNotifications: envNotifications,
		envAuth:          envAuth,
		envGeneral:       envGeneral,
		crypto:           crypto,
	}, nil
}

// EnvBaselineCopilot returns the env-driven config the process booted
// with. Used by GET /settings/booted-with to show operators what would be
// in effect if all UI overrides were cleared. Read-only — do not mutate.
func (r *Runtime) EnvBaselineCopilot() config.CopilotConfig {
	return r.envBase
}

// InvalidateCopilot marks the cached Copilot config as stale. Called by
// PUT and reset handlers. Safe to call from any goroutine.
func (r *Runtime) InvalidateCopilot() {
	r.mu.Lock()
	r.copilotValid = false
	r.mu.Unlock()
}
