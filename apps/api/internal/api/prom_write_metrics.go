package api

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// PromWriteMetrics exposes per-tenant observability for the
// /api/v1/prom/write receiver (Phase 3 Day 5). Counters are labeled
// by tenant_id so operators can answer "is this tenant being
// throttled?" with one PromQL query. The active_series gauge is
// driven by the CardinalityTracker's refresh loop so it tracks
// VM's authoritative count rather than receiver-local guesses.
//
// Endpoint surface: registered at /metrics on the backend's chi
// router (Prometheus scrape convention). No auth — operators
// firewall this port at the LB / NetworkPolicy layer when running
// SaaS or multi-tenant deployments. Self-hosted single-cluster
// installs typically expose this freely; the bundled chart can
// add a ServiceMonitor or simple Prom scrape config to pull from
// the API service.
//
// Cardinality budget: tenant_id can fan out unbounded in SaaS.
// We accept this for now — operators that hit memory pressure on
// the metrics registry can downsample tenant_id to a hash bucket
// at the alerting / dashboard layer. Future iteration may add a
// "high-cardinality tenants" cap that aggregates the long tail.
type PromWriteMetrics struct {
	// requestsTotal counts every request that reached the handler,
	// labeled by the outcome. Common queries:
	//   rate(kubebolt_prom_write_requests_total{status="accepted"}[5m])
	//   rate(kubebolt_prom_write_requests_total{status!="accepted"}[5m])
	requestsTotal *prometheus.CounterVec
	// samplesAccepted is the per-tenant total of samples that made
	// it past every gate and got forwarded to VM. Drives billing
	// counters and capacity dashboards.
	samplesAccepted *prometheus.CounterVec
	// bytesAccepted tracks the on-wire payload size that was
	// forwarded. Useful for cost-allocation in SaaS where
	// bandwidth is non-trivial.
	bytesAccepted *prometheus.CounterVec
	// droppedSeries counts non-core ("custom") series dropped at ingest by
	// the core-only name filter, labeled by tenant + drop reason. Pairs
	// with samplesAccepted for a kept-vs-dropped cardinality dashboard.
	droppedSeries *prometheus.CounterVec
	// activeSeries mirrors the CardinalityTracker's cache. Updated
	// by the tracker's refresh loop, not the request hot path.
	activeSeries *prometheus.GaugeVec
	// activeSeriesMu + activeSeriesTenants tracks which tenant_id
	// labels currently have a gauge value, so SetActiveSeries can
	// drop entries whose tenant disappeared in the latest snapshot.
	// The prometheus library doesn't expose label-iteration
	// directly; an external set is the cleanest way to compute the
	// "tenants present last refresh, absent this refresh" delta.
	activeSeriesMu      sync.Mutex
	activeSeriesTenants map[string]struct{}
}

// Status label values. Each request flows through these
// states; only "accepted" ends with a forward to VM. Kept as
// snake_case strings (Prometheus convention) so dashboards can
// filter by exact value.
const (
	PromWriteStatusAccepted               = "accepted"
	PromWriteStatusRejectedRateLimit      = "rate_limit"
	PromWriteStatusRejectedCardinality    = "cardinality"
	PromWriteStatusRejectedAuth           = "auth"
	PromWriteStatusRejectedBodySize       = "body_size"
	PromWriteStatusRejectedMalformed      = "malformed"
	PromWriteStatusRejectedTenantMismatch = "tenant_id_mismatch"
	PromWriteStatusRejectedTenantMissing  = "tenant_id_missing"
	PromWriteStatusInjectionFailed       = "injection_failed"
	PromWriteStatusUpstreamError         = "upstream_error"
)

// PromWriteAnonymousTenant is the synthetic tenant_id used when
// requests don't authenticate against a real Tenant (disabled mode
// or permissive-fallback). Same identity the rate limiter uses, so
// /metrics aggregations join cleanly with rate-limit dashboards.
const PromWriteAnonymousTenant = "anonymous"

// NewPromWriteMetrics constructs the metrics + registers them on
// the given Prometheus registry. Passing prometheus.DefaultRegisterer
// is the common case; tests can pass a fresh registry to avoid
// global-state collision across `go test`.
func NewPromWriteMetrics(reg prometheus.Registerer) *PromWriteMetrics {
	m := &PromWriteMetrics{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_prom_write_requests_total",
			Help: "Total /api/v1/prom/write requests, labeled by outcome status.",
		}, []string{"tenant_id", "status"}),
		samplesAccepted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_prom_write_samples_accepted_total",
			Help: "Total samples that passed every gate and were forwarded to VM, per tenant.",
		}, []string{"tenant_id"}),
		bytesAccepted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_prom_write_bytes_accepted_total",
			Help: "Total request body bytes (snappy-compressed) that were forwarded, per tenant.",
		}, []string{"tenant_id"}),
		activeSeries: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kubebolt_prom_write_active_series",
			Help: "Cached active series count per tenant, refreshed every 30s from VM.",
		}, []string{"tenant_id"}),
		droppedSeries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_prom_write_dropped_series_total",
			Help: "Total series dropped at ingest by the core-only name filter, per tenant and reason.",
		}, []string{"tenant_id", "reason"}),
		activeSeriesTenants: make(map[string]struct{}),
	}
	// MustRegister panics on duplicate registration — that's the
	// right semantic for a process-lifetime metric; a double-wire
	// in main.go would silently corrupt counts without it.
	reg.MustRegister(m.requestsTotal, m.samplesAccepted, m.bytesAccepted, m.activeSeries, m.droppedSeries)
	return m
}

// RecordRequest increments the request counter for the given
// outcome. tenantID may be the synthetic "anonymous" identity when
// auth was permissive-fallback / disabled (the rate limiter does
// the same — see prom_write.go).
func (m *PromWriteMetrics) RecordRequest(tenantID, status string) {
	if m == nil {
		return
	}
	m.requestsTotal.WithLabelValues(tenantID, status).Inc()
}

// RecordAcceptedSamples bumps the samples + bytes counters when
// the request was forwarded to VM. Bytes is the snappy-compressed
// on-wire size — the metric the operator pays for in SaaS bandwidth
// accounting.
func (m *PromWriteMetrics) RecordAcceptedSamples(tenantID string, samples int, bytes int) {
	if m == nil {
		return
	}
	if samples > 0 {
		m.samplesAccepted.WithLabelValues(tenantID).Add(float64(samples))
	}
	if bytes > 0 {
		m.bytesAccepted.WithLabelValues(tenantID).Add(float64(bytes))
	}
}

// RecordDroppedByName bumps the dropped-series counter for series the
// core-only name filter rejected (reason="custom" — the __name__ wasn't a
// KubeBolt family). Series-level count, not request-level: a request can
// forward its core series and still record customs dropped here.
func (m *PromWriteMetrics) RecordDroppedByName(tenantID string, count int) {
	if m == nil || count <= 0 {
		return
	}
	m.droppedSeries.WithLabelValues(tenantID, "custom").Add(float64(count))
}

// SetActiveSeries replaces the active_series snapshot. Called by
// CardinalityTracker.refresh() at the end of each successful poll.
// Tenants absent from the snapshot have their gauge values dropped
// so stale tenants don't accumulate indefinitely.
//
// We maintain an internal set of tenants we've seen so we can
// compute the drop diff without iterating the prometheus library's
// internal metric registry (which doesn't expose label-iteration
// publicly). The set is mutated under a small Mutex — this method
// runs every 30s, not on the hot path, so contention is negligible.
func (m *PromWriteMetrics) SetActiveSeries(snapshot map[string]int) {
	if m == nil {
		return
	}
	m.activeSeriesMu.Lock()
	defer m.activeSeriesMu.Unlock()

	// Drop tenants present in the previous snapshot but absent
	// from this one.
	for tid := range m.activeSeriesTenants {
		if _, ok := snapshot[tid]; !ok {
			m.activeSeries.DeleteLabelValues(tid)
			delete(m.activeSeriesTenants, tid)
		}
	}
	// Set values for tenants in the current snapshot.
	for tid, count := range snapshot {
		m.activeSeries.WithLabelValues(tid).Set(float64(count))
		m.activeSeriesTenants[tid] = struct{}{}
	}
}

// ForgetTenant drops every metric series for the given tenant.
// Called when an operator deletes a tenant via the admin API so
// stale label values don't accumulate in /metrics.
func (m *PromWriteMetrics) ForgetTenant(tenantID string) {
	if m == nil {
		return
	}
	m.requestsTotal.DeletePartialMatch(prometheus.Labels{"tenant_id": tenantID})
	m.samplesAccepted.DeleteLabelValues(tenantID)
	m.bytesAccepted.DeleteLabelValues(tenantID)
	m.activeSeries.DeleteLabelValues(tenantID)
	m.droppedSeries.DeletePartialMatch(prometheus.Labels{"tenant_id": tenantID})
}

// PromHTTPHandler returns the http.Handler that serves /metrics
// for a given registry. The handler emits the standard Prometheus
// text-exposition format. Use prometheus.DefaultGatherer for the
// process-global registry; tests pass a fresh gatherer to isolate.
func PromHTTPHandler(gatherer prometheus.Gatherer) http.Handler {
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		// Plain-text only — VM scrapes via the standard Prom protocol,
		// and the no-OpenMetrics constraint keeps the response shape
		// stable across client versions.
		EnableOpenMetrics: false,
	})
}
