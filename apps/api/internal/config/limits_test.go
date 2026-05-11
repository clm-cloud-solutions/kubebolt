package config

import (
	"testing"
)

func TestLoadPromWriteLimitsConfig_UnsetEnvUsesDefaults(t *testing.T) {
	// Clear every env var the loader reads.
	t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC", "")
	t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES", "")
	t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES", "")

	got := LoadPromWriteLimitsConfig()
	if got.WriteSamplesPerSec != DefaultPromWriteSamplesPerSec {
		t.Errorf("WriteSamplesPerSec: expected %d, got %d", DefaultPromWriteSamplesPerSec, got.WriteSamplesPerSec)
	}
	if got.WriteBurstSamples != DefaultPromWriteBurstSamples {
		t.Errorf("WriteBurstSamples: expected %d, got %d", DefaultPromWriteBurstSamples, got.WriteBurstSamples)
	}
	if got.MaxActiveSeries != DefaultPromWriteMaxActiveSeries {
		t.Errorf("MaxActiveSeries: expected %d, got %d", DefaultPromWriteMaxActiveSeries, got.MaxActiveSeries)
	}
}

func TestLoadPromWriteLimitsConfig_ValidOverrides(t *testing.T) {
	t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC", "5000")
	t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES", "50000")
	t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES", "500000")

	got := LoadPromWriteLimitsConfig()
	if got.WriteSamplesPerSec != 5000 {
		t.Errorf("WriteSamplesPerSec: expected 5000, got %d", got.WriteSamplesPerSec)
	}
	if got.WriteBurstSamples != 50000 {
		t.Errorf("WriteBurstSamples: expected 50000, got %d", got.WriteBurstSamples)
	}
	if got.MaxActiveSeries != 500000 {
		t.Errorf("MaxActiveSeries: expected 500000, got %d", got.MaxActiveSeries)
	}
}

func TestLoadPromWriteLimitsConfig_BadValuesFallBackToDefault(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"non_numeric", "KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC", "not-a-number"},
		{"zero", "KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES", "0"},
		{"negative", "KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES", "-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Clear all keys, set just this one.
			t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC", "")
			t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES", "")
			t.Setenv("KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES", "")
			t.Setenv(tc.key, tc.val)

			got := LoadPromWriteLimitsConfig()
			// All three fields should land on their respective defaults
			// since the only override was bad.
			if got.WriteSamplesPerSec != DefaultPromWriteSamplesPerSec {
				t.Errorf("WriteSamplesPerSec: expected default %d, got %d", DefaultPromWriteSamplesPerSec, got.WriteSamplesPerSec)
			}
			if got.WriteBurstSamples != DefaultPromWriteBurstSamples {
				t.Errorf("WriteBurstSamples: expected default %d, got %d", DefaultPromWriteBurstSamples, got.WriteBurstSamples)
			}
			if got.MaxActiveSeries != DefaultPromWriteMaxActiveSeries {
				t.Errorf("MaxActiveSeries: expected default %d, got %d", DefaultPromWriteMaxActiveSeries, got.MaxActiveSeries)
			}
		})
	}
}

func TestReadPositiveIntEnv_AllPaths(t *testing.T) {
	t.Setenv("KB_TEST_INT", "")
	if got := readPositiveIntEnv("KB_TEST_INT", 42); got != 42 {
		t.Errorf("unset: expected fallback 42, got %d", got)
	}
	t.Setenv("KB_TEST_INT", "100")
	if got := readPositiveIntEnv("KB_TEST_INT", 42); got != 100 {
		t.Errorf("valid: expected 100, got %d", got)
	}
	t.Setenv("KB_TEST_INT", "bogus")
	if got := readPositiveIntEnv("KB_TEST_INT", 42); got != 42 {
		t.Errorf("non-numeric: expected fallback 42, got %d", got)
	}
	t.Setenv("KB_TEST_INT", "0")
	if got := readPositiveIntEnv("KB_TEST_INT", 42); got != 42 {
		t.Errorf("zero: expected fallback 42, got %d", got)
	}
	t.Setenv("KB_TEST_INT", "-1")
	if got := readPositiveIntEnv("KB_TEST_INT", 42); got != 42 {
		t.Errorf("negative: expected fallback 42, got %d", got)
	}
}
