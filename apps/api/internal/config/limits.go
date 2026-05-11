package config

import (
	"log"
	"os"
	"strconv"
)

// PromWriteLimitsConfig holds the fleet-wide default Prom remote_write
// limits applied when a tenant has no per-tenant override on the
// corresponding field. Loaded from KUBEBOLT_PROM_WRITE_DEFAULT_* env
// vars at backend startup; the runtime resolver in auth.ResolveLimits
// reads these values once and caches.
//
// Why fleet-wide defaults + per-tenant overrides instead of per-tenant-
// only: in the SaaS edition the typical posture is that 95% of tenants
// stay on plan-derived defaults; the operator only customizes the few
// tenants that need exception capacity. Without fleet-wide defaults
// every new tenant would need explicit limit assignment, which
// fragments the configuration story across BoltDB writes.
//
// Defaults rationale:
//   - WriteSamplesPerSec 10000 — fits a typical 50-pod cluster scraping
//     KSM + node-exporter via Prom (~30 series per node × 50 nodes /
//     30s = ~50 samples/sec aggregate; 10000 leaves substantial
//     headroom for app exporters and burst).
//   - WriteBurstSamples 100000 — covers a scrape cycle where every
//     target hands over its samples within a 10s window (10× the
//     steady rate).
//   - MaxActiveSeries 1000000 — generous bound that keeps a single
//     tenant from balloon-ing VictoriaMetrics' index. The
//     KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES_GLOBAL Phase 2
//     defense remains intact; this is the per-tenant slice of that
//     global budget.
type PromWriteLimitsConfig struct {
	WriteSamplesPerSec int
	WriteBurstSamples  int
	MaxActiveSeries    int
}

// Default values used when the corresponding env var is unset or
// unparseable. Exposed as constants for tests + so handlers can render
// "defaults" in admin responses without re-reading env vars per request.
const (
	DefaultPromWriteSamplesPerSec = 10_000
	DefaultPromWriteBurstSamples  = 100_000
	DefaultPromWriteMaxActiveSeries = 1_000_000
)

// LoadPromWriteLimitsConfig reads the per-tenant Prom remote_write
// limit defaults from env vars. Unset / unparseable values fall back
// to the constants above; the load logs a WARN once per process when
// a value falls back so operators see the unmistakable signal.
//
// Validation: every value must be > 0. A 0 or negative env var falls
// back to the default with a WARN — operators can still set genuinely
// permissive limits (1_000_000_000+) but they can't trip the system
// into a deny-all posture via a typo.
func LoadPromWriteLimitsConfig() PromWriteLimitsConfig {
	cfg := PromWriteLimitsConfig{
		WriteSamplesPerSec: DefaultPromWriteSamplesPerSec,
		WriteBurstSamples:  DefaultPromWriteBurstSamples,
		MaxActiveSeries:    DefaultPromWriteMaxActiveSeries,
	}

	if v := readPositiveIntEnv("KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC", DefaultPromWriteSamplesPerSec); v != 0 {
		cfg.WriteSamplesPerSec = v
	}
	if v := readPositiveIntEnv("KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES", DefaultPromWriteBurstSamples); v != 0 {
		cfg.WriteBurstSamples = v
	}
	if v := readPositiveIntEnv("KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES", DefaultPromWriteMaxActiveSeries); v != 0 {
		cfg.MaxActiveSeries = v
	}

	return cfg
}

func readPositiveIntEnv(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("WARN config: %s=%q is not an integer — using default %d", key, raw, fallback)
		return fallback
	}
	if v <= 0 {
		log.Printf("WARN config: %s=%d must be > 0 — using default %d", key, v, fallback)
		return fallback
	}
	return v
}
