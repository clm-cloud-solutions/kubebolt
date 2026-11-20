package config

import (
	"os"
	"strconv"
)

// GeneralConfig holds "branding + UX defaults" settings — values that
// don't fit any one functional domain (auth, copilot, notifications,
// ingest) but are still cluster-wide config the operator wants to set.
//
// All fields read from KUBEBOLT_* env vars at boot. UI overrides via
// /admin/settings/general merge onto this baseline (spec #09 pattern).
type GeneralConfig struct {
	// DisplayName is the human label shown in the topbar / browser
	// title to identify which KubeBolt install the user is looking at.
	// Empty string = "KubeBolt" (the product default).
	DisplayName string

	// DefaultRefreshIntervalSeconds is the fallback the frontend uses
	// when a user has no localStorage preference yet. Picked so teams
	// can ship a sensible default ("our cluster needs 15s polling")
	// without every user re-picking. Per-user overrides via the
	// DataFreshnessIndicator dropdown still win.
	//
	// Allowed values: 5, 10, 15, 30, 60, 120. Anything else falls
	// back to 30 — same set the RefreshContext on the frontend
	// considers valid.
	DefaultRefreshIntervalSeconds int

	// ProdNamespacePattern is the regex used to classify a namespace
	// as "production" for the Secret Reveal role-escalation rule
	// (admin required in prod, editor elsewhere). Empty string falls
	// back to the actions_secret.go default
	// `^(prod|production|prd)([-_].+)?$`. UI-editable via
	// Settings → General; regex compile is validated server-side.
	ProdNamespacePattern string

	// UpdateCheckEnabled controls whether the backend periodically
	// queries the GitHub releases API to surface a "new version
	// available" chip in the UI. Defaults true. Air-gapped operators
	// disable via KUBEBOLT_UPDATE_CHECK_ENABLED=false; admins can also
	// toggle at runtime via Settings → General.
	UpdateCheckEnabled bool

	// CacheSyncTimeoutSeconds is how long a cold cluster connect's informer
	// sync may go without receiving anything before failing ("cluster may be
	// unreachable"). It is a budget of silence, not of duration: a sync that
	// keeps receiving pages continues, up to cluster.maxConnectDuration
	// (10 min), so a large cluster behind a slow agent tunnel connects
	//. Default 45s. Set via
	// KUBEBOLT_CACHE_SYNC_TIMEOUT_SECONDS; admins override at runtime (no
	// restart) from the platform settings. Floored at 5s.
	CacheSyncTimeoutSeconds int

	// RightSizingMinHistoryHours is how much usage history right-sizing needs
	// before it recommends anything. 0 = rightsizing.DefaultMinHistoryHours
	// (48h: two daily peaks). Install-global: set in Settings › General, no
	// restart; lowering it lets a trial cluster show recommendations on its
	// first day, marked low-confidence below 48h.
	RightSizingMinHistoryHours int
}

// DefaultCacheSyncTimeoutSeconds is the baseline informer cache-sync deadline.
const DefaultCacheSyncTimeoutSeconds = 45

// MinCacheSyncTimeoutSeconds floors the override so a fat-fingered tiny value
// can't make every connect fail instantly.
const MinCacheSyncTimeoutSeconds = 5

// LoadGeneralConfig reads general settings from env vars. All optional.
func LoadGeneralConfig() GeneralConfig {
	cfg := GeneralConfig{
		DisplayName:                   os.Getenv("KUBEBOLT_DISPLAY_NAME"),
		DefaultRefreshIntervalSeconds: 30, // matches RefreshContext fallback
		ProdNamespacePattern:          os.Getenv("KUBEBOLT_PROD_NAMESPACE_PATTERN"),
		UpdateCheckEnabled:            true,
		CacheSyncTimeoutSeconds:       DefaultCacheSyncTimeoutSeconds,
	}
	if v := os.Getenv("KUBEBOLT_DEFAULT_REFRESH_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && isValidRefreshInterval(n) {
			cfg.DefaultRefreshIntervalSeconds = n
		}
	}
	if v := os.Getenv("KUBEBOLT_UPDATE_CHECK_ENABLED"); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			cfg.UpdateCheckEnabled = parsed
		}
	}
	if v := os.Getenv("KUBEBOLT_CACHE_SYNC_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= MinCacheSyncTimeoutSeconds {
			cfg.CacheSyncTimeoutSeconds = n
		}
	}
	return cfg
}

// isValidRefreshInterval mirrors the VALID_INTERVALS set the frontend
// RefreshContext enforces. Keeping the two in lockstep prevents the
// server from returning a value the dropdown can't display.
func isValidRefreshInterval(n int) bool {
	switch n {
	case 5, 10, 15, 30, 60, 120:
		return true
	}
	return false
}

// ValidRefreshIntervalSeconds is the canonical list — exported so the
// settings layer's validator can reuse it instead of duplicating the
// magic numbers.
var ValidRefreshIntervalSeconds = []int{5, 10, 15, 30, 60, 120}
