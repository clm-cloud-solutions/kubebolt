package agent

import (
	"github.com/prometheus/client_golang/prometheus"
)

// GRPCIngestMetrics records the per-tenant Prometheus counters that
// power the `/admin/ingest-activity` panel (spec #09 V2 / Item 5b).
//
// Mirror of api.PromWriteMetrics's structure but for the gRPC side of
// the ingest plane: stream-level lifecycle events (connect / disconnect
// / auth reject) and the total samples crossing the wire. Together with
// the remote_write counters they answer the operator's "what is my
// ingest doing right now?" question across both paths.
//
// Why these two counters and not more:
//   - Per-source breakdown (cadvisor vs kubelet vs hubble vs self) was
//     considered and dropped. The protocol's StreamMetrics message
//     carries samples but doesn't tag them with a source — we'd have
//     to inspect each sample's name (`container_cpu_usage_seconds_total`
//     → cadvisor, `pod_flow_*` → hubble, etc.) which is hot-path-
//     expensive and operators rarely care about that level of slicing.
//     The spec's "agent (cAdvisor + kubelet + Hubble) vs remote_write"
//     framing is captured by the two-counter pair.
//   - Active series per tenant is NOT duplicated here — the existing
//     `kubebolt_prom_write_active_series{tenant_id}` gauge counts ALL
//     series VM holds for the tenant regardless of source, so it's
//     already the right answer for the ingest-activity panel's gauge.
type GRPCIngestMetrics struct {
	streamsTotal    *prometheus.CounterVec
	samplesReceived *prometheus.CounterVec
}

// GRPCIngestStream* are the canonical values for the `status` label of
// kubebolt_agent_grpc_streams_total. Centralized so callers (the agent
// server, the auth interceptor) can't drift on the spelling — drift
// would break the dashboard's status-class grouping silently.
const (
	GRPCIngestStreamConnected    = "connected"
	GRPCIngestStreamDisconnected = "disconnected"
	GRPCIngestStreamAuthRejected = "auth_rejected"
	// GRPCIngestStreamTenantMismatch is recorded when a metric batch asserts
	// a tenant_id label that differs from the agent's authenticated tenant —
	// an anti-spoofing reject mirroring the remote_write gate. The stream is
	// torn down on the spot, so this also implies a disconnect.
	GRPCIngestStreamTenantMismatch = "tenant_mismatch"
)

// AnonymousTenant is the synthetic tenant_id used when the auth
// interceptor lets a request through without a real Tenant (disabled
// mode or permissive-fallback). Same identity prom_write uses so the
// /admin/ingest-activity panel can join the two ingest paths cleanly.
const AnonymousTenant = "anonymous"

// NewGRPCIngestMetrics constructs the two counters and registers them
// on the given Prometheus registry. Passing prometheus.DefaultRegisterer
// is the common case; tests can pass a fresh registry to avoid
// global-state collision across `go test`.
//
// Returns nil when reg is nil so callers can centralise the "metrics
// disabled" gate (e.g., a future flag to skip telemetry entirely).
func NewGRPCIngestMetrics(reg prometheus.Registerer) *GRPCIngestMetrics {
	if reg == nil {
		return nil
	}
	m := &GRPCIngestMetrics{
		streamsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_agent_grpc_streams_total",
			Help: "Total agent gRPC stream lifecycle events labeled by tenant + status (connected/disconnected/auth_rejected).",
		}, []string{"tenant_id", "status"}),
		samplesReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_agent_grpc_samples_received_total",
			Help: "Total samples received from agents via the gRPC StreamMetrics channel, per tenant.",
		}, []string{"tenant_id"}),
	}
	reg.MustRegister(m.streamsTotal, m.samplesReceived)
	return m
}

// RecordStreamEvent increments the stream lifecycle counter. tenantID
// may be the synthetic AnonymousTenant when auth was permissive-
// fallback / disabled. Safe to call on a nil receiver — that's the
// "metrics disabled" no-op path so call sites don't need to nil-guard.
func (m *GRPCIngestMetrics) RecordStreamEvent(tenantID, status string) {
	if m == nil {
		return
	}
	if tenantID == "" {
		tenantID = AnonymousTenant
	}
	m.streamsTotal.WithLabelValues(tenantID, status).Inc()
}

// RecordSamplesReceived bumps the total-samples counter when an agent's
// StreamMetrics batch arrives. Counts the samples in the batch, not
// the bytes — the dashboard's sparkline is a rate(samples_total[5m])
// which gives operators the "samples per second" reading they think
// in. Bytes accounting would be a follow-on metric if SaaS bandwidth
// becomes a billable axis.
func (m *GRPCIngestMetrics) RecordSamplesReceived(tenantID string, samples int) {
	if m == nil || samples <= 0 {
		return
	}
	if tenantID == "" {
		tenantID = AnonymousTenant
	}
	m.samplesReceived.WithLabelValues(tenantID).Add(float64(samples))
}
