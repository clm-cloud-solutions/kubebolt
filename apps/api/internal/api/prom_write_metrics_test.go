package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue returns the current value of a labeled counter by
// scanning the registry's Collect output. Tests use this instead of
// poking the metric internals — keeps the test contract on the
// public Prometheus interface so a library bump won't silently
// break expectations.
func counterValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
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
			if labelsMatch(m.GetLabel(), labels) {
				switch {
				case m.Counter != nil:
					return m.Counter.GetValue()
				case m.Gauge != nil:
					return m.Gauge.GetValue()
				}
			}
		}
	}
	return 0
}

func labelsMatch(got []*dto.LabelPair, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, lp := range got {
		if want[lp.GetName()] != lp.GetValue() {
			return false
		}
	}
	return true
}

func TestPromWriteMetrics_RecordRequest_IncrementsByLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPromWriteMetrics(reg)

	m.RecordRequest("tenant-A", PromWriteStatusAccepted)
	m.RecordRequest("tenant-A", PromWriteStatusAccepted)
	m.RecordRequest("tenant-A", PromWriteStatusRejectedRateLimit)
	m.RecordRequest("tenant-B", PromWriteStatusAccepted)

	if v := counterValue(t, reg, "kubebolt_prom_write_requests_total",
		map[string]string{"tenant_id": "tenant-A", "status": PromWriteStatusAccepted}); v != 2 {
		t.Errorf("tenant-A accepted: expected 2, got %v", v)
	}
	if v := counterValue(t, reg, "kubebolt_prom_write_requests_total",
		map[string]string{"tenant_id": "tenant-A", "status": PromWriteStatusRejectedRateLimit}); v != 1 {
		t.Errorf("tenant-A rate_limit: expected 1, got %v", v)
	}
	if v := counterValue(t, reg, "kubebolt_prom_write_requests_total",
		map[string]string{"tenant_id": "tenant-B", "status": PromWriteStatusAccepted}); v != 1 {
		t.Errorf("tenant-B accepted: expected 1, got %v", v)
	}
}

func TestPromWriteMetrics_RecordAcceptedSamples_AccumulatesBytesAndSamples(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPromWriteMetrics(reg)

	m.RecordAcceptedSamples("tenant-A", 100, 1024)
	m.RecordAcceptedSamples("tenant-A", 50, 512)
	m.RecordAcceptedSamples("tenant-B", 200, 2048)

	if v := counterValue(t, reg, "kubebolt_prom_write_samples_accepted_total",
		map[string]string{"tenant_id": "tenant-A"}); v != 150 {
		t.Errorf("tenant-A samples: expected 150, got %v", v)
	}
	if v := counterValue(t, reg, "kubebolt_prom_write_bytes_accepted_total",
		map[string]string{"tenant_id": "tenant-A"}); v != 1536 {
		t.Errorf("tenant-A bytes: expected 1536, got %v", v)
	}
	if v := counterValue(t, reg, "kubebolt_prom_write_samples_accepted_total",
		map[string]string{"tenant_id": "tenant-B"}); v != 200 {
		t.Errorf("tenant-B samples: expected 200, got %v", v)
	}
}

func TestPromWriteMetrics_RecordAcceptedSamples_ZeroCountsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPromWriteMetrics(reg)
	m.RecordAcceptedSamples("tenant-A", 0, 0)
	// Zero increments should NOT instantiate the labeled metric
	// (gather should return empty for samples/bytes).
	if v := counterValue(t, reg, "kubebolt_prom_write_samples_accepted_total",
		map[string]string{"tenant_id": "tenant-A"}); v != 0 {
		t.Errorf("zero samples should be a no-op, got %v", v)
	}
}

func TestPromWriteMetrics_SetActiveSeries_ReplacesPriorSnapshot(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPromWriteMetrics(reg)

	// First snapshot: A=10, B=20.
	m.SetActiveSeries(map[string]int{"tenant-A": 10, "tenant-B": 20})
	if v := counterValue(t, reg, "kubebolt_prom_write_active_series",
		map[string]string{"tenant_id": "tenant-A"}); v != 10 {
		t.Errorf("A first snapshot: expected 10, got %v", v)
	}
	if v := counterValue(t, reg, "kubebolt_prom_write_active_series",
		map[string]string{"tenant_id": "tenant-B"}); v != 20 {
		t.Errorf("B first snapshot: expected 20, got %v", v)
	}

	// Second snapshot: A=15, C=5 — B disappears.
	m.SetActiveSeries(map[string]int{"tenant-A": 15, "tenant-C": 5})
	if v := counterValue(t, reg, "kubebolt_prom_write_active_series",
		map[string]string{"tenant_id": "tenant-A"}); v != 15 {
		t.Errorf("A second snapshot: expected 15, got %v", v)
	}
	if v := counterValue(t, reg, "kubebolt_prom_write_active_series",
		map[string]string{"tenant_id": "tenant-B"}); v != 0 {
		t.Errorf("B should be dropped after disappearance, got %v", v)
	}
	if v := counterValue(t, reg, "kubebolt_prom_write_active_series",
		map[string]string{"tenant_id": "tenant-C"}); v != 5 {
		t.Errorf("C second snapshot: expected 5, got %v", v)
	}
}

func TestPromWriteMetrics_ForgetTenant_RemovesAllSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPromWriteMetrics(reg)
	m.RecordRequest("tenant-A", PromWriteStatusAccepted)
	m.RecordRequest("tenant-A", PromWriteStatusRejectedRateLimit)
	m.RecordAcceptedSamples("tenant-A", 100, 1024)
	m.SetActiveSeries(map[string]int{"tenant-A": 50})

	m.ForgetTenant("tenant-A")

	// All four series for tenant-A should be dropped.
	for _, sel := range []map[string]string{
		{"tenant_id": "tenant-A", "status": PromWriteStatusAccepted},
		{"tenant_id": "tenant-A", "status": PromWriteStatusRejectedRateLimit},
		{"tenant_id": "tenant-A"},
	} {
		for _, mname := range []string{
			"kubebolt_prom_write_requests_total",
			"kubebolt_prom_write_samples_accepted_total",
			"kubebolt_prom_write_bytes_accepted_total",
			"kubebolt_prom_write_active_series",
		} {
			if v := counterValue(t, reg, mname, sel); v != 0 {
				t.Errorf("after Forget: %s%v should be 0, got %v", mname, sel, v)
			}
		}
	}
}

func TestPromWriteMetrics_NilMethodsAreNoop(t *testing.T) {
	// The nil-guard contract lets test fixtures and transitional
	// installs pass a nil PromWriteMetrics without crashing the
	// handler. This guards against accidental method-receiver
	// dereferences in future edits.
	var m *PromWriteMetrics
	m.RecordRequest("tenant-A", "ok")        // must not panic
	m.RecordAcceptedSamples("tenant-A", 1, 1) // must not panic
	m.SetActiveSeries(map[string]int{"t": 1}) // must not panic
	m.ForgetTenant("tenant-A")                // must not panic
}

func TestPromHTTPHandler_ServesTextExposition(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPromWriteMetrics(reg)
	m.RecordRequest("tenant-A", PromWriteStatusAccepted)
	m.RecordAcceptedSamples("tenant-A", 42, 1234)

	handler := PromHTTPHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Spot-check that the standard text format is present. We don't
	// pin exact bytes (libraries reformat) but the key fragments
	// must show up: metric name + label + value.
	for _, want := range []string{
		"kubebolt_prom_write_requests_total",
		`tenant_id="tenant-A"`,
		`status="accepted"`,
		"kubebolt_prom_write_samples_accepted_total",
		"42",
		"kubebolt_prom_write_bytes_accepted_total",
		"1234",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q\n---response---\n%s", want, body)
		}
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", got)
	}
}
