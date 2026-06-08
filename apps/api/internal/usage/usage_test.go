package usage

import (
	"context"
	"testing"
	"time"
)

// TestNoopUsageStore_RecordIsSafeAndNil pins the OSS no-op contract: Record
// never errors and never blocks, for both the constructed and zero-value forms.
func TestNoopUsageStore_RecordIsSafeAndNil(t *testing.T) {
	rec := UsageRecord{
		TenantID:  "t1",
		ClusterID: "c1",
		Metric:    MetricSamplesIngested,
		Quantity:  500,
		At:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	// Constructed pointer form.
	if err := NewNoopUsageStore().Record(context.Background(), rec); err != nil {
		t.Errorf("NewNoopUsageStore().Record returned %v, want nil", err)
	}

	// Zero-value form must be equally usable (the seam promises a never-nil
	// UsageStore that's safe to hold without construction).
	var zero NoopUsageStore
	if err := zero.Record(context.Background(), rec); err != nil {
		t.Errorf("zero-value NoopUsageStore.Record returned %v, want nil", err)
	}

	// Held behind the interface, the way a call site would.
	var store UsageStore = NewNoopUsageStore()
	if err := store.Record(context.Background(), UsageRecord{}); err != nil {
		t.Errorf("UsageStore.Record(empty) returned %v, want nil", err)
	}
}

// TestMetricConstants guards the dimension string values — EE roll-up SQL and
// the OSS call sites must agree on these exact strings, so a rename is a
// breaking change that should fail a test, not silently mis-bill.
func TestMetricConstants(t *testing.T) {
	cases := map[Metric]string{
		MetricSamplesIngested: "samples_ingested",
		MetricActiveSeries:    "active_series",
		MetricAPIRequests:     "api_requests",
		MetricCopilotTokens:   "copilot_tokens",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("metric constant = %q, want %q", string(got), want)
		}
	}
}
