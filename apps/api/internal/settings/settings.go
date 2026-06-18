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
	"context"
	"errors"
	"sync"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Runtime is the per-process settings resolver. One instance for the
// lifetime of the server, shared across handlers.
type Runtime struct {
	store auth.SettingStore
	// orgStore backs PER-ORG settings (Copilot today). Unlike store (global
	// install settings), reads/writes are scoped to the org carried in the
	// request ctx. OSS resolves every request to the single default tenant, so
	// the per-org map below holds exactly one entry — identical to the old
	// global behavior.
	orgStore auth.OrgSettingStore

	// envBase / envNotifications / envAuth / envGeneral / envIngestChannel
	// are the env-driven baselines computed once at boot. Override values
	// from BoltDB merge onto these at resolve time, field by field.
	envBase           config.CopilotConfig
	envNotifications  config.NotificationsConfig
	envAuth           config.AuthConfig
	envGeneral        config.GeneralConfig
	envIngestChannel  config.IngestChannelConfig

	// crypto handles encryption of sensitive fields at rest (API keys,
	// webhook secrets). Derived from the JWT secret at boot so the same
	// install can decrypt what it wrote; rotating the JWT secret renders
	// existing encrypted blobs unreadable (documented as expected).
	crypto *secretCrypto

	// Cache. Per-domain `<Domain>` field holds the last resolved struct;
	// the matching `<Domain>Valid` flag toggles to false on PUT so the
	// next read re-resolves from BoltDB. Plain mutex over a sync.Map is
	// fine — read concurrency is high but cache invalidation is rare.
	mu sync.RWMutex
	// Copilot is PER-ORG: the cache keys by org id (resolved from the request
	// ctx). copilotByOrg holds the last resolved struct per org; copilotValid
	// tracks which orgs are fresh. A PUT invalidates only the mutating org, so
	// one org's change never re-resolves another's. OSS keys everything under
	// the single default tenant → one entry.
	copilotByOrg       map[string]config.CopilotConfig
	copilotValid       map[string]bool
	// Notifications is PER-ORG (Track D): each org configures its own channels
	// and thresholds; the dispatcher routes each insight to its owning org's
	// notifiers (keyed by insight.TenantID).
	notificationsByOrg map[string]config.NotificationsConfig
	notificationsValid map[string]bool
	auth               config.AuthConfig
	authValid          bool
	// General is MIXED: per-org ORG fields + install-global PLATFORM fields.
	// The cache keys by org (the resolved config folds in the global platform
	// fields too); a platform-field PUT invalidates every org.
	generalByOrg map[string]config.GeneralConfig
	generalValid map[string]bool
	// authBootSnapshot is the resolved Auth config the running process
	// was actually built from. Set once at boot via
	// CaptureAuthBootSnapshot(); compared against Auth() to compute
	// pendingRestart.
	authBootSnapshot config.AuthConfig
	authBootCaptured bool

	// IngestChannel cache + boot snapshot. Mirrors the Auth pattern:
	// the resolved struct is always available via IngestChannel(), and
	// the boot snapshot lets the UI compute pendingRestart for the
	// restart-required subset (agent auth mode, token audience, mTLS).
	ingestChannel             config.IngestChannelConfig
	ingestChannelValid        bool
	ingestChannelBootSnapshot config.IngestChannelConfig
	ingestChannelBootCaptured bool
}

// NewRuntime wires the Runtime against an auth.Store and the env-driven
// baselines for each domain. The jwtSecret feeds the at-rest secret
// encryption — it must be the same value used to sign tokens, so
// rotation rules are understood as a single concern by operators.
//
// Returns an error if the JWT secret is too short to derive a key from
// (must be at least 16 bytes — anything shorter is a misconfiguration we
// fail loud about rather than silently accept).
func NewRuntime(store auth.SettingStore, orgStore auth.OrgSettingStore, envCopilot config.CopilotConfig, envNotifications config.NotificationsConfig, envAuth config.AuthConfig, envGeneral config.GeneralConfig, envIngestChannel config.IngestChannelConfig, jwtSecret []byte) (*Runtime, error) {
	if store == nil {
		return nil, errors.New("settings: store is required")
	}
	if orgStore == nil {
		return nil, errors.New("settings: org store is required")
	}
	crypto, err := newSecretCrypto(jwtSecret)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		store:            store,
		orgStore:         orgStore,
		envBase:          envCopilot,
		envNotifications: envNotifications,
		envAuth:          envAuth,
		envGeneral:       envGeneral,
		envIngestChannel: envIngestChannel,
		crypto:           crypto,
		copilotByOrg:       map[string]config.CopilotConfig{},
		copilotValid:       map[string]bool{},
		generalByOrg:       map[string]config.GeneralConfig{},
		generalValid:       map[string]bool{},
		notificationsByOrg: map[string]config.NotificationsConfig{},
		notificationsValid: map[string]bool{},
	}, nil
}

// EnvBaselineCopilot returns the env-driven config the process booted
// with. Used by GET /settings/booted-with to show operators what would be
// in effect if all UI overrides were cleared. Read-only — do not mutate.
func (r *Runtime) EnvBaselineCopilot() config.CopilotConfig {
	return r.envBase
}

// InvalidateCopilot marks one org's cached Copilot config as stale. Called by
// PUT and reset handlers with the request ctx so only the mutating org is
// invalidated. Safe to call from any goroutine.
func (r *Runtime) InvalidateCopilot(ctx context.Context) {
	org := orgKey(ctx)
	r.mu.Lock()
	delete(r.copilotValid, org)
	r.mu.Unlock()
}

// orgKey resolves the org id used to key the per-org settings cache. Mirrors
// auth.TenantIDFromContext but normalizes the "no request org" case to the
// default tenant so OSS (and any unscoped path) keys consistently.
func orgKey(ctx context.Context) string {
	if org := auth.TenantIDFromContext(ctx); org != "" {
		return org
	}
	return auth.DefaultTenantName
}
