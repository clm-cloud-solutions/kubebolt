// Package usage is the W1 metering seam: a durable, per-tenant record of
// billable usage that EE rolls up monthly for billing
// (internal/saas/kubebolt-implementation-roadmap.md — "Sprint 2.2 roll-up
// mensual y metering", Postgres `usage_records` table).
//
// This is NOT the same thing as internal/api/prom_write_metrics.go: those are
// ephemeral Prometheus counters for live observability (dashboards, rate-limit
// gauges) that reset on restart. A UsageRecord is the authoritative,
// persisted-for-billing signal — it must survive restarts and reconcile to a
// monthly invoice.
//
// OSS ships NoopUsageStore (everyone is on a free, unmetered single-tenant
// install, so there's nothing to bill). EE swaps a Postgres-backed impl behind
// the same seam. Call sites in OSS record usage unconditionally; the no-op
// makes that free.
package usage

import (
	"context"
	"time"
)

// Metric is a billable usage dimension. Kept as a string type (not an enum
// closed at compile time) so EE can introduce new dimensions — copilot token
// spend, retained-series-days, AI-gateway calls — without an OSS release. The
// constants below are the dimensions OSS call sites already produce.
type Metric string

const (
	// MetricSamplesIngested counts metric samples accepted into storage,
	// from both the Prom remote_write receiver and the agent gRPC channel.
	// The dominant billable signal for a metrics product.
	MetricSamplesIngested Metric = "samples_ingested"

	// MetricActiveSeries is the peak/active cardinality observed for a
	// tenant in the window — the other half of metrics-cost (storage
	// footprint, not just write throughput).
	MetricActiveSeries Metric = "active_series"

	// MetricAPIRequests counts authenticated REST API calls — the metering
	// dimension for programmatic (kbs_/kbk_ token) access.
	MetricAPIRequests Metric = "api_requests"

	// MetricCopilotTokens counts LLM tokens spent on a tenant's behalf —
	// the pass-through cost of the AI copilot.
	MetricCopilotTokens Metric = "copilot_tokens"
)

// UsageRecord is a single metered event: tenant X consumed Quantity of Metric
// at time At. ClusterID is optional (empty for tenant-wide events like API
// requests). Quantity is an absolute count for cumulative metrics
// (samples/requests/tokens) and a gauge snapshot for MetricActiveSeries — the
// EE roll-up sums the former and takes the max of the latter.
type UsageRecord struct {
	TenantID  string
	ClusterID string
	Metric    Metric
	Quantity  int64
	At        time.Time
}

// UsageStore is the metering seam. Record must be cheap and non-blocking on the
// hot path (it sits behind sample ingestion): EE implementations buffer and
// aggregate in memory, flushing to Postgres on a ticker, rather than writing
// once per call. Record never fails the caller's request — a metering error is
// logged and swallowed by the impl, never propagated to drop a customer's data.
type UsageStore interface {
	Record(ctx context.Context, rec UsageRecord) error
	// Summary returns the all-time per-metric totals for one org — the read
	// path behind GET /account/usage. OSS (NoopUsageStore) returns an empty
	// slice (nothing is metered). EE sums usage_records for the org, RLS-scoped
	// via app.current_org. Order is unspecified.
	Summary(ctx context.Context, org string) ([]UsagePoint, error)
	// UsedSince returns the SUM of one metric for an org since `since`
	// (inclusive) — the read behind a period roll-up (e.g. credits used this
	// calendar month). OSS (NoopUsageStore) returns 0 (nothing is metered). EE
	// sums usage_records RLS-scoped via app.current_org. An org with no rows in
	// the window returns 0, not an error.
	UsedSince(ctx context.Context, org string, m Metric, since time.Time) (int64, error)
}

// UsagePoint is one metric's rolled-up total for an org, the read shape behind
// GET /account/usage.
type UsagePoint struct {
	Metric Metric `json:"metric"`
	Total  int64  `json:"total"`
}

// NoopUsageStore is the OSS default: metering is a no-op because OSS is a free,
// unmetered single-tenant install. The zero value is usable, so call sites can
// hold a UsageStore that's never nil and record unconditionally.
type NoopUsageStore struct{}

// NewNoopUsageStore returns the OSS no-op metering store.
func NewNoopUsageStore() *NoopUsageStore { return &NoopUsageStore{} }

// Record discards the event and returns nil.
func (NoopUsageStore) Record(context.Context, UsageRecord) error { return nil }

// Summary returns an empty slice — OSS meters nothing.
func (NoopUsageStore) Summary(context.Context, string) ([]UsagePoint, error) {
	return []UsagePoint{}, nil
}

// UsedSince returns 0 — OSS meters nothing, so no org is ever over a cap.
func (NoopUsageStore) UsedSince(context.Context, string, Metric, time.Time) (int64, error) {
	return 0, nil
}

// Compile-time guarantees the no-op satisfies the seam — both as a value and a
// pointer, so callers can wire either form.
var (
	_ UsageStore = NoopUsageStore{}
	_ UsageStore = (*NoopUsageStore)(nil)
)
