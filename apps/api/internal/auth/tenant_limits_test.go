package auth

import (
	"errors"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestResolveLimits_NilCustom_UsesDefaults(t *testing.T) {
	defaults := EffectiveLimits{
		WriteSamplesPerSec: 10_000,
		WriteBurstSamples:  100_000,
		MaxActiveSeries:    1_000_000,
	}
	got := ResolveLimits(nil, defaults)
	if got != defaults {
		t.Fatalf("expected defaults when custom is nil, got %+v", got)
	}
}

func TestResolveLimits_PartialOverride(t *testing.T) {
	defaults := EffectiveLimits{
		WriteSamplesPerSec: 10_000,
		WriteBurstSamples:  100_000,
		MaxActiveSeries:    1_000_000,
	}
	custom := &TenantLimits{
		WriteBurstSamples: intPtr(200_000),
		// other fields nil → default fallback
	}
	got := ResolveLimits(custom, defaults)
	if got.WriteSamplesPerSec != defaults.WriteSamplesPerSec {
		t.Errorf("WriteSamplesPerSec: expected default %d, got %d", defaults.WriteSamplesPerSec, got.WriteSamplesPerSec)
	}
	if got.WriteBurstSamples != 200_000 {
		t.Errorf("WriteBurstSamples: expected custom 200000, got %d", got.WriteBurstSamples)
	}
	if got.MaxActiveSeries != defaults.MaxActiveSeries {
		t.Errorf("MaxActiveSeries: expected default %d, got %d", defaults.MaxActiveSeries, got.MaxActiveSeries)
	}
}

func TestResolveLimits_FullOverride(t *testing.T) {
	defaults := EffectiveLimits{
		WriteSamplesPerSec: 10_000,
		WriteBurstSamples:  100_000,
		MaxActiveSeries:    1_000_000,
	}
	custom := &TenantLimits{
		WriteSamplesPerSec: intPtr(50_000),
		WriteBurstSamples:  intPtr(500_000),
		MaxActiveSeries:    intPtr(10_000_000),
	}
	got := ResolveLimits(custom, defaults)
	want := EffectiveLimits{
		WriteSamplesPerSec: 50_000,
		WriteBurstSamples:  500_000,
		MaxActiveSeries:    10_000_000,
	}
	if got != want {
		t.Fatalf("expected full override %+v, got %+v", want, got)
	}
}

func TestValidateLimits_NilIsValid(t *testing.T) {
	_, err := ValidateLimits(nil)
	if err != nil {
		t.Fatalf("nil should validate, got %v", err)
	}
}

func TestValidateLimits_NegativeRejects(t *testing.T) {
	cases := []struct {
		name    string
		patch   TenantLimits
		wantSub string
	}{
		{"negative_samples_per_sec", TenantLimits{WriteSamplesPerSec: intPtr(-1)}, "writeSamplesPerSec"},
		{"negative_burst", TenantLimits{WriteBurstSamples: intPtr(-1)}, "writeBurstSamples"},
		{"negative_max_series", TenantLimits{MaxActiveSeries: intPtr(-1)}, "maxActiveSeries"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateLimits(&tc.patch)
			if err == nil {
				t.Fatalf("expected validation error for %s, got nil", tc.name)
			}
		})
	}
}

func TestValidateLimits_ZeroIsAllowed(t *testing.T) {
	// Zero is a legitimate "block all" posture — operator may want it
	// on a disabled tenant for belt-and-suspenders safety.
	patch := TenantLimits{
		WriteSamplesPerSec: intPtr(0),
		WriteBurstSamples:  intPtr(0),
		MaxActiveSeries:    intPtr(0),
	}
	v, err := ValidateLimits(&patch)
	if err != nil {
		t.Fatalf("zero should be accepted, got %v", err)
	}
	// Zero burst with zero rate is consistent — no warning expected.
	if len(v.Warnings) != 0 {
		t.Errorf("no warnings expected on all-zero, got %v", v.Warnings)
	}
}

func TestValidateLimits_BurstBelowRateWarns(t *testing.T) {
	patch := TenantLimits{
		WriteSamplesPerSec: intPtr(10_000),
		WriteBurstSamples:  intPtr(5_000), // < rate
	}
	v, err := ValidateLimits(&patch)
	if err != nil {
		t.Fatalf("burst < rate should be a warning, not error: %v", err)
	}
	if len(v.Warnings) == 0 {
		t.Fatalf("expected at least one warning for burst < rate")
	}
}

func TestMergeLimits_NilPatch_PreservesBase(t *testing.T) {
	base := &TenantLimits{WriteBurstSamples: intPtr(50_000)}
	out := MergeLimits(base, nil)
	if out != base {
		t.Fatalf("nil patch must preserve base pointer")
	}
}

func TestMergeLimits_PartialPatch_OverwritesOnly(t *testing.T) {
	base := &TenantLimits{
		WriteSamplesPerSec: intPtr(10_000),
		WriteBurstSamples:  intPtr(50_000),
		// MaxActiveSeries nil — inherits system default
	}
	patch := &TenantLimits{
		WriteBurstSamples: intPtr(75_000),
		MaxActiveSeries:   intPtr(500_000),
	}
	out := MergeLimits(base, patch)
	if out == nil {
		t.Fatalf("merge produced nil")
	}
	if *out.WriteSamplesPerSec != 10_000 {
		t.Errorf("samples_per_sec preserved: expected 10000, got %d", *out.WriteSamplesPerSec)
	}
	if *out.WriteBurstSamples != 75_000 {
		t.Errorf("burst overwritten: expected 75000, got %d", *out.WriteBurstSamples)
	}
	if *out.MaxActiveSeries != 500_000 {
		t.Errorf("max_series set fresh: expected 500000, got %d", *out.MaxActiveSeries)
	}
}

func TestMergeLimits_BaseNil_PatchOnly(t *testing.T) {
	patch := &TenantLimits{WriteBurstSamples: intPtr(75_000)}
	out := MergeLimits(nil, patch)
	if out == nil || out.WriteBurstSamples == nil || *out.WriteBurstSamples != 75_000 {
		t.Fatalf("base=nil + patch should yield patch values, got %+v", out)
	}
	if out.WriteSamplesPerSec != nil || out.MaxActiveSeries != nil {
		t.Errorf("unpatched fields should remain nil, got %+v", out)
	}
}

func TestErrLimitsValidation_Sentinel(t *testing.T) {
	// Documenting the sentinel exists + is errors.Is-comparable so
	// the handler can map it to HTTP 400 cleanly.
	if !errors.Is(ErrLimitsValidation, ErrLimitsValidation) {
		t.Fatalf("sentinel should self-identify via errors.Is")
	}
}
