package agent

import (
	"context"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/usage"
)

// captureUsage is a UsageStore that records every Record call. It embeds the
// no-op store so it satisfies the Summary half of the interface for free.
type captureUsage struct {
	usage.NoopUsageStore
	records []usage.UsageRecord
}

func (c *captureUsage) Record(_ context.Context, r usage.UsageRecord) error {
	c.records = append(c.records, r)
	return nil
}

// Agent-ingested samples must be metered through the usage seam, attributed to
// the AUTHENTICATED tenant (id.TenantID), mirroring the remote_write path.
func TestMeterSamples_RecordsForAuthenticatedTenant(t *testing.T) {
	cap := &captureUsage{}
	s := NewServer(&captureWriter{}, WithUsageStore(cap))

	s.meterSamples(context.Background(), "org-a", 42)

	if len(cap.records) != 1 {
		t.Fatalf("want 1 usage record, got %d", len(cap.records))
	}
	r := cap.records[0]
	if r.TenantID != "org-a" {
		t.Errorf("tenantID = %q, want org-a (must bill the authenticated tenant)", r.TenantID)
	}
	if r.Metric != usage.MetricSamplesIngested {
		t.Errorf("metric = %q, want %q", r.Metric, usage.MetricSamplesIngested)
	}
	if r.Quantity != 42 {
		t.Errorf("quantity = %d, want 42", r.Quantity)
	}
}

// No authenticated tenant (auth-disabled / single-tenant): ingest must NOT be
// metered — unattributed samples have no org to bill, and a "" tenant would
// orphan a row.
func TestMeterSamples_SkipsWhenNoTenant(t *testing.T) {
	cap := &captureUsage{}
	s := NewServer(&captureWriter{}, WithUsageStore(cap))

	s.meterSamples(context.Background(), "", 100)

	if len(cap.records) != 0 {
		t.Fatalf("want 0 usage records for empty tenant, got %d", len(cap.records))
	}
}

// Empty batch must not emit a zero-quantity record.
func TestMeterSamples_SkipsEmptyBatch(t *testing.T) {
	cap := &captureUsage{}
	s := NewServer(&captureWriter{}, WithUsageStore(cap))

	s.meterSamples(context.Background(), "org-a", 0)

	if len(cap.records) != 0 {
		t.Fatalf("want 0 usage records for empty batch, got %d", len(cap.records))
	}
}

// No seam wired (the option not passed, e.g. unit tests / unwired boot): must
// be a safe no-op, not a nil-deref panic.
func TestMeterSamples_NilStoreIsNoOp(t *testing.T) {
	s := NewServer(&captureWriter{}) // no WithUsageStore
	s.meterSamples(context.Background(), "org-a", 7)
}
