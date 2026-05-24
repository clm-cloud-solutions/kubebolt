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
}

// LoadGeneralConfig reads general settings from env vars. All optional.
func LoadGeneralConfig() GeneralConfig {
	cfg := GeneralConfig{
		DisplayName:                   os.Getenv("KUBEBOLT_DISPLAY_NAME"),
		DefaultRefreshIntervalSeconds: 30, // matches RefreshContext fallback
	}
	if v := os.Getenv("KUBEBOLT_DEFAULT_REFRESH_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && isValidRefreshInterval(n) {
			cfg.DefaultRefreshIntervalSeconds = n
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
