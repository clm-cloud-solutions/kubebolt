// Phase 3 Day 8 — full-pipeline integration tests for the
// /api/v1/prom/write receiver.
//
// Existing tests in this package each isolate one gate (parser,
// rate limiter, cardinality tracker, metrics, injector, auth).
// These E2E tests wire ALL of them into a single handler and
// exercise the same path a real Prometheus / vmagent client
// traverses: snappy-compressed protobuf body → auth → rate limit
// → tenant_id validation → cardinality cap → optional injection
// → forward to upstream VictoriaMetrics → /metrics counter
// updates.
//
// Why a separate file: scope. Adding cases to prom_write_test.go
// would force every reader to mentally separate "unit test of one
// gate" from "integration test of the whole pipeline". A new file
// names the intent at the package level.
//
// Why not docker-compose: a true Prometheus binary in a container
// is slower, requires docker-in-docker for CI, and adds no
// coverage beyond what the protobuf+snappy round-trip already
// exercises. The plan's "E2E: Prom standalone → KubeBolt → VM"
// is satisfied at the wire-format boundary; future docker-compose
// validation lives in internal/cluster-validation, not in CI.
package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newE2EHandler wires the receiver with every Phase 3 gate active
// (auth, rate limit, cardinality, metrics) plus a fakeUpstream
// stand-in for VictoriaMetrics. Returns the handler, the token's
// plaintext, the tenant's ID, the fake upstream (to inspect the
// forwarded body), and the metrics registry (to inspect counters).
//
// defaults parameterize the rate / cardinality knobs so tests can
// pick a tight bucket to trip 429s without flake.
func newE2EHandler(t *testing.T, mode string, defaults auth.EffectiveLimits) (
	h *handlers,
	plaintext string,
	tenantID string,
	upstream *fakeUpstream,
	reg *prometheus.Registry,
) {
	t.Helper()
	upstream = &fakeUpstream{respStatus: http.StatusNoContent}
	ts := httptest.NewServer(upstream.handler())
	t.Cleanup(ts.Close)
	withPromWriteEnabled(t)
	pointStorageAt(t, ts)

	store, plaintext := newTenantsStoreWithToken(t)
	// LookupByToken plus a fresh-test cleanup so each E2E test
	// runs against an isolated tenant identity.
	tn, _, err := store.LookupByToken(plaintext)
	if err != nil {
		t.Fatalf("LookupByToken: %v", err)
	}
	tenantID = tn.ID

	reg = prometheus.NewRegistry()
	h = &handlers{
		tenantsStore:      store,
		promWriteAuthMode: mode,
		promRateLimiter:   NewPromRateLimiter(defaults),
		// CardinalityTracker without a refresh loop: hasFresh stays
		// false → Allow() returns true on every call (the
		// permissive-boot semantic). Exercises the cardinality
		// branch in the handler without needing a live VM.
		promCardinality:  NewCardinalityTracker("http://disabled", defaults, nil, 0),
		promWriteMetrics: NewPromWriteMetrics(reg),
	}
	return h, plaintext, tenantID, upstream, reg
}

// counterByLabels gathers reg, finds the counter named `name` whose
// labels match `want`, and returns its value. Returns 0 when no
// matching series exists — that's "the counter for this label set
// has never been touched", which is the correct semantic for
// observability assertions (a metric with no recorded events
// reads as zero in Prom queries).
func counterByLabels(t *testing.T, reg *prometheus.Registry, name string, want map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if !labelsContain(labels, want) {
				continue
			}
			switch {
			case m.Counter != nil:
				return m.Counter.GetValue()
			case m.Gauge != nil:
				return m.Gauge.GetValue()
			}
		}
	}
	return 0
}

func labelsContain(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// TestE2E_PromRemoteWrite_FullPipeline_HappyPath:
//
// Exercises every gate at once for a request that should succeed.
// Build a real snappy+protobuf WriteRequest carrying a tenant_id
// label that matches the bearer's tenant, post it with the bearer
// in Authorization, and verify:
//
//  1. Handler returns 204.
//  2. fakeUpstream received the (potentially re-encoded) body.
//  3. /metrics shows requests_total{status="accepted"}=1 +
//     samples_accepted_total>0 for this tenant.
//  4. /metrics shows zero rejections under any status for this
//     tenant (no gate fired).
//
// The lazy tenant-id label population is exercised by passing the
// label IN the body, which means the receiver's Day 4.1
// validation runs (verify it matches the bearer) and the Day 4.2
// "stamp if missing" path is NOT triggered.
func TestE2E_PromRemoteWrite_FullPipeline_HappyPath(t *testing.T) {
	roomy := auth.EffectiveLimits{
		WriteSamplesPerSec: 100_000,
		WriteBurstSamples:  1_000_000,
		MaxActiveSeries:    10_000_000,
	}
	h, plaintext, tenantID, upstream, reg := newE2EHandler(t, promWriteAuthEnforced, roomy)

	// Real WriteRequest: one TimeSeries with a tenant_id label that
	// matches our bearer's tenant + a metric name, then 3 samples.
	body := buildSnappyWriteRequest(t, [][2]string{
		{"__name__", "node_cpu_usage_seconds_total"},
		{"tenant_id", tenantID},
		{"node", "node-a"},
	})
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Upstream MUST have received the body. We don't pin exact
	// bytes (the snappy round-trip may re-encode) but the body
	// must be non-empty and the upstream path must match VM's
	// remote_write endpoint.
	upstream.mu.Lock()
	gotLen := len(upstream.lastBody)
	gotPath := upstream.lastPath
	upstream.mu.Unlock()
	if gotLen == 0 {
		t.Error("upstream got empty body — handler did not forward")
	}
	if gotPath != "/api/v1/write" {
		t.Errorf("upstream got path %q, want /api/v1/write", gotPath)
	}

	// Metrics: one accepted request, samples > 0, no rejections.
	wantAccept := map[string]string{"tenant_id": tenantID, "status": PromWriteStatusAccepted}
	if v := counterByLabels(t, reg, "kubebolt_prom_write_requests_total", wantAccept); v != 1 {
		t.Errorf("requests_total{status=accepted} = %v, want 1", v)
	}
	if v := counterByLabels(t, reg, "kubebolt_prom_write_samples_accepted_total", map[string]string{"tenant_id": tenantID}); v < 1 {
		t.Errorf("samples_accepted_total = %v, want >= 1", v)
	}
	// Cross-check: no rejection counter for this tenant should have
	// fired. Spot-check the four most common rejection statuses.
	for _, status := range []string{
		PromWriteStatusRejectedRateLimit,
		PromWriteStatusRejectedCardinality,
		PromWriteStatusRejectedAuth,
		PromWriteStatusRejectedTenantMissing,
	} {
		w := map[string]string{"tenant_id": tenantID, "status": status}
		if v := counterByLabels(t, reg, "kubebolt_prom_write_requests_total", w); v != 0 {
			t.Errorf("requests_total{status=%s} = %v, want 0 (no gate should have fired)", status, v)
		}
	}
}

// TestE2E_PromRemoteWrite_RateLimitTrips_429_WithRetryAfter:
//
// Operator contract: when the per-tenant rate limit trips, the
// response MUST be 429 with a Retry-After header so vmagent and
// Prometheus' remote_write queue back off correctly instead of
// retrying immediately. Validates:
//
//  1. Sending samples in excess of the token-bucket burst returns
//     429 (not 5xx — those would imply backend trouble, not
//     intentional throttling).
//  2. Retry-After parses as a non-zero positive integer (seconds).
//     A zero value would make clients retry-storm; we contract
//     for >= 1 even when the bucket refills in less than a second.
//  3. /metrics counter for status="rate_limit" advances by exactly
//     one for the throttled request, NOT for status="accepted".
//
// Uses a deliberately tight bucket (rate=10, burst=10) so a
// single request of 50 samples is guaranteed to exceed.
func TestE2E_PromRemoteWrite_RateLimitTrips_429_WithRetryAfter(t *testing.T) {
	tight := auth.EffectiveLimits{
		WriteSamplesPerSec: 10,
		WriteBurstSamples:  10,
		MaxActiveSeries:    10_000_000, // out of the way
	}
	h, plaintext, tenantID, upstream, reg := newE2EHandler(t, promWriteAuthEnforced, tight)

	// 50 samples in one batch → exceeds burst=10.
	body := buildOversizedSnappyWriteRequest(t, tenantID, 50)
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()

	h.handlePromWrite(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%q", rec.Code, rec.Body.String())
	}
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After header, got empty")
	}
	secs, err := strconv.Atoi(retryAfter)
	if err != nil {
		t.Fatalf("Retry-After %q is not an integer: %v", retryAfter, err)
	}
	if secs < 1 {
		t.Errorf("Retry-After = %d, want >= 1 (clients must back off at least a second)", secs)
	}

	// Upstream MUST NOT have been called — we throttled before
	// the forward path.
	upstream.mu.Lock()
	gotBody := upstream.lastBody
	upstream.mu.Unlock()
	if len(gotBody) > 0 {
		t.Errorf("upstream was called with %d bytes — throttled requests must not reach VM", len(gotBody))
	}

	// Metrics: exactly one rate_limit, zero accepted.
	wantThrottled := map[string]string{"tenant_id": tenantID, "status": PromWriteStatusRejectedRateLimit}
	if v := counterByLabels(t, reg, "kubebolt_prom_write_requests_total", wantThrottled); v != 1 {
		t.Errorf("requests_total{status=rate_limit} = %v, want 1", v)
	}
	wantAccepted := map[string]string{"tenant_id": tenantID, "status": PromWriteStatusAccepted}
	if v := counterByLabels(t, reg, "kubebolt_prom_write_requests_total", wantAccepted); v != 0 {
		t.Errorf("requests_total{status=accepted} = %v, want 0", v)
	}
}

// buildOversizedSnappyWriteRequest constructs a single TimeSeries
// with one label set (tenant_id + a metric name) and `nSamples`
// samples inside it. The wire-format sample count is exactly
// nSamples, which is what the rate-limit gate decodes and
// compares against the bucket capacity.
//
// Uses the existing buildWriteRequestRich helper, supplying a
// single series spec with Samples=nSamples.
func buildOversizedSnappyWriteRequest(t *testing.T, tenantID string, nSamples int) []byte {
	t.Helper()
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{
			Labels: [][2]string{
				{"__name__", "node_cpu_usage_seconds_total"},
				{"tenant_id", tenantID},
			},
			Samples: nSamples,
		},
	})
	return snappy.Encode(nil, body)
}

// _ keeps the io import alive when other helpers shed dependencies
// across refactors. Cheap insurance — Go is fine with unused
// package-level vars after build.
var _ = io.Discard
