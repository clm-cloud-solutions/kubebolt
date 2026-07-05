// Package auth — tenant_limits.go defines the per-tenant resource limit
// model used by the Prom remote_write receiver (Phase 3 of the Universal
// Data Plane Plan). Limits are stored on the Tenant record in BoltDB so
// they persist across restarts and so the SaaS edition can map them to
// billing tiers cleanly.
//
// Resolution model: each limit field is a pointer. nil on the tenant
// means "fall back to the system default". The system default comes from
// config.LoadLimitsConfig() which reads `KUBEBOLT_PROM_WRITE_DEFAULT_*`
// env vars (see internal/config/limits.go). The Resolve / Effective
// helpers below produce concrete numbers for the enforcement layers.
//
// Why pointer + system default instead of zero-default:
//   - Operators can set "no limit" semantics later by extending the
//     fallback (e.g. a sentinel like 0 = unlimited) without breaking
//     existing tenants. Today the defaults are populated so 0 would be
//     unusual; the field can still be set to 0 explicitly to override.
//   - SaaS tier changes (free → pro) propagate by editing the per-tenant
//     overrides without touching the env-var defaults; the env-var
//     defaults remain the fleet-wide baseline.
//   - Per-tenant nil means "no opinion — inherit from the fleet" which
//     is exactly the right semantic when the operator hasn't customized
//     this tenant.
package auth

import (
	"errors"
	"fmt"
)

// TenantLimits is the per-tenant override of system-default Prom
// remote_write limits. Stored on Tenant.Limits; any field nil means
// "inherit from system default". Stored as a value type (no pointer
// transitions) so JSON marshaling is straightforward.
type TenantLimits struct {
	// WriteSamplesPerSec is the steady-state samples-per-second the
	// tenant may ingest via /api/v1/prom/write. The rate limiter is a
	// token bucket; sustained rate cannot exceed this value.
	WriteSamplesPerSec *int `json:"writeSamplesPerSec,omitempty"`

	// WriteBurstSamples is the maximum number of samples the tenant
	// may submit in a single burst (one request, or multiple back-to-
	// back requests within a tick). Token-bucket "burst" semantics —
	// the bucket is filled with this many tokens.
	WriteBurstSamples *int `json:"writeBurstSamples,omitempty"`

	// MaxActiveSeries is the upper bound on distinct active series the
	// tenant may have in VictoriaMetrics at any time. New series past
	// this cap are rejected at ingest with HTTP 413 + Retry-After.
	// Series count is authoritative via a periodic VM count query.
	MaxActiveSeries *int `json:"maxActiveSeries,omitempty"`

	// AllowCustomSeries controls whether NON-core metric families (series
	// whose __name__ isn't a KubeBolt-consumed family — the customer's own
	// app metrics arriving via remote_write) are accepted or dropped at
	// ingest. nil inherits the system default (core-only). false = drop
	// custom: the margin-protecting floor, KubeBolt only ingests what it
	// consumes. true = keep custom — they reach VM and count toward
	// MaxActiveSeries (the billable "custom telemetry" tier). This is the
	// enforcement point for the core-vs-custom split in the active-series
	// pricing proposal; the family classification is the metric registry.
	AllowCustomSeries *bool `json:"allowCustomSeries,omitempty"`
}

// EffectiveLimits is the resolved view a tenant's enforcement layer
// consumes: every field is a concrete number (custom override or
// system default), plus a parallel "source" map for the admin UI to
// render "default" / "custom" badges per field.
type EffectiveLimits struct {
	WriteSamplesPerSec int  `json:"writeSamplesPerSec"`
	WriteBurstSamples  int  `json:"writeBurstSamples"`
	MaxActiveSeries    int  `json:"maxActiveSeries"`
	AllowCustomSeries  bool `json:"allowCustomSeries"`
}

// LimitsResponse is the admin API DTO. It returns three views:
//   - effective: what enforcement uses right now
//   - custom:    the tenant's overrides (nil fields stripped via omitempty)
//   - defaults:  the system fallback (so the UI can show what would
//                apply if the operator clicks "Reset to default")
//
// The UI compares effective[field] against custom[field] to decide
// whether to render the "default" or "custom" badge per row.
type LimitsResponse struct {
	Effective EffectiveLimits `json:"effective"`
	Custom    *TenantLimits   `json:"custom,omitempty"`
	Defaults  EffectiveLimits `json:"defaults"`
}

// ResolveLimits collapses a tenant's overrides against the system
// defaults to produce concrete enforcement numbers. Called on every
// request the rate/cardinality layers process — kept allocation-free.
func ResolveLimits(custom *TenantLimits, defaults EffectiveLimits) EffectiveLimits {
	out := defaults
	if custom == nil {
		return out
	}
	if custom.WriteSamplesPerSec != nil {
		out.WriteSamplesPerSec = *custom.WriteSamplesPerSec
	}
	if custom.WriteBurstSamples != nil {
		out.WriteBurstSamples = *custom.WriteBurstSamples
	}
	if custom.MaxActiveSeries != nil {
		out.MaxActiveSeries = *custom.MaxActiveSeries
	}
	if custom.AllowCustomSeries != nil {
		out.AllowCustomSeries = *custom.AllowCustomSeries
	}
	return out
}

// ValidateLimits enforces the invariants the admin API requires for
// any field that's been explicitly set. Nil fields are skipped (they
// mean "no override" and the system default will apply).
//
// Rules:
//   - Numeric values cannot be negative.
//   - Zero is allowed: an explicit 0 reads as "block all" and is a
//     legitimate posture for a disabled tenant that we want to keep
//     in the registry. The Disabled flag is the canonical block, but
//     a 0 limit is a useful belt-and-suspenders fallback.
//   - Burst is expected to be ≥ rate. We do NOT reject burst < rate
//     (operators may want it intentionally — e.g. cap initial spike
//     below sustained rate) but the admin UI surfaces it as a
//     warning. The validator returns it as a soft warning string.
//
// Returns nil if the limits are accepted as-is, or an error describing
// the first hard-rejection. Soft warnings live on the result's Warnings
// field so the admin handler can surface them to the UI without
// blocking the write.
type LimitsValidation struct {
	Warnings []string
}

func ValidateLimits(l *TenantLimits) (LimitsValidation, error) {
	if l == nil {
		return LimitsValidation{}, nil
	}
	var v LimitsValidation
	if l.WriteSamplesPerSec != nil && *l.WriteSamplesPerSec < 0 {
		return v, fmt.Errorf("writeSamplesPerSec must be >= 0, got %d", *l.WriteSamplesPerSec)
	}
	if l.WriteBurstSamples != nil && *l.WriteBurstSamples < 0 {
		return v, fmt.Errorf("writeBurstSamples must be >= 0, got %d", *l.WriteBurstSamples)
	}
	if l.MaxActiveSeries != nil && *l.MaxActiveSeries < 0 {
		return v, fmt.Errorf("maxActiveSeries must be >= 0, got %d", *l.MaxActiveSeries)
	}
	// Soft check: burst < rate is unusual. If both are explicitly set
	// AND burst < rate, flag a warning. Pure rate without burst, or
	// vice-versa, falls back to defaults on the missing field — the
	// effective values are checked at enforcement time.
	if l.WriteBurstSamples != nil && l.WriteSamplesPerSec != nil &&
		*l.WriteBurstSamples < *l.WriteSamplesPerSec {
		v.Warnings = append(v.Warnings,
			fmt.Sprintf("writeBurstSamples (%d) is lower than writeSamplesPerSec (%d) — sustained rate may exceed burst headroom",
				*l.WriteBurstSamples, *l.WriteSamplesPerSec))
	}
	return v, nil
}

// ErrLimitsValidation is returned by ValidateLimits when a hard rule
// is violated. The handler maps this to HTTP 400.
var ErrLimitsValidation = errors.New("invalid tenant limits")

// MergeLimits applies a partial update onto an existing tenant's
// limits. Fields set on patch override the base; fields nil on patch
// preserve the base. Returns a fresh struct — does NOT mutate either
// argument. The admin PUT handler uses this so partial bodies behave
// intuitively (sending only writeBurstSamples doesn't wipe the other
// two overrides).
//
// To CLEAR a specific field (revert to system default), the operator
// uses the DELETE endpoint or sends a full PUT with the field omitted
// AND a query param `?reset=<field>`. MVP: clear-individual-field is
// out of scope; the DELETE endpoint resets all overrides at once.
func MergeLimits(base, patch *TenantLimits) *TenantLimits {
	if patch == nil {
		// Nil patch means no change. Return base as-is (or nil).
		return base
	}
	out := TenantLimits{}
	if base != nil {
		out = *base
	}
	if patch.WriteSamplesPerSec != nil {
		v := *patch.WriteSamplesPerSec
		out.WriteSamplesPerSec = &v
	}
	if patch.WriteBurstSamples != nil {
		v := *patch.WriteBurstSamples
		out.WriteBurstSamples = &v
	}
	if patch.MaxActiveSeries != nil {
		v := *patch.MaxActiveSeries
		out.MaxActiveSeries = &v
	}
	if patch.AllowCustomSeries != nil {
		v := *patch.AllowCustomSeries
		out.AllowCustomSeries = &v
	}
	return &out
}
