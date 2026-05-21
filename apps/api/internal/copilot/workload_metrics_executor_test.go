package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tests for the executor-side helpers in workload_metrics_executor.go.
// The full Execute() flow requires a Connector mock that doesn't exist in
// this package (see executor_propose_test.go's preamble); these tests
// cover the pure helpers + the HTTP layer via httptest so we can pin the
// query → response decoding contract without a live VM. End-to-end
// verification happens in-vivo per spec #07 §"In-vivo on yagan".

// ─── parseMetricsArg ─────────────────────────────────────────────────

func TestParseMetricsArg_HappyPath(t *testing.T) {
	got, err := parseMetricsArg([]interface{}{"cpu", "memory", "network_rx"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []MetricKind{MetricCPU, MetricMemory, MetricNetworkRX}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d", len(got), len(want))
	}
	for i, m := range want {
		if got[i] != m {
			t.Errorf("idx %d: got %s, want %s", i, got[i], m)
		}
	}
}

func TestParseMetricsArg_Dedups(t *testing.T) {
	got, err := parseMetricsArg([]interface{}{"cpu", "cpu", "memory"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected dedup to 2, got %d (%v)", len(got), got)
	}
}

func TestParseMetricsArg_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"nil", nil, "must be a non-empty array"},
		{"not an array", "cpu", "must be a non-empty array"},
		{"empty array", []interface{}{}, "must be a non-empty array"},
		{"non-string entry", []interface{}{1}, "must be strings"},
		{"unknown metric", []interface{}{"disk_io"}, `invalid metric "disk_io"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseMetricsArg(c.in)
			if err == nil {
				t.Fatalf("expected error, got none")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should contain %q", err.Error(), c.want)
			}
		})
	}
}

// ─── containsAny ─────────────────────────────────────────────────────

func TestContainsAny(t *testing.T) {
	set := []MetricKind{MetricCPU, MetricMemory}
	if !containsAny(set, MetricCPU) {
		t.Error("CPU should be in set")
	}
	if containsAny(set, MetricNetworkRX) {
		t.Error("network_rx should NOT be in set")
	}
	if !containsAny(set, MetricNetworkRX, MetricMemory) {
		t.Error("at least one of net_rx/mem should match")
	}
	if containsAny(nil, MetricCPU) {
		t.Error("empty set: nothing should match")
	}
}

// ─── extractPodNames / dedupStrings ──────────────────────────────────

func TestExtractPodNames(t *testing.T) {
	in := []map[string]interface{}{
		{"name": "api-7c5d-abcd1", "namespace": "default"},
		{"name": "api-7c5d-efgh2", "namespace": "default"},
		{"namespace": "default"}, // missing name — should be dropped
		{"name": ""},             // empty — dropped
		{"name": "api-7c5d-aaaa1"},
	}
	got := extractPodNames(in)
	want := []string{"api-7c5d-aaaa1", "api-7c5d-abcd1", "api-7c5d-efgh2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, n := range want {
		if got[i] != n {
			t.Errorf("idx %d: got %q, want %q", i, got[i], n)
		}
	}
}

func TestDedupStrings(t *testing.T) {
	got := dedupStrings([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("dedup result: %v", got)
	}
	if dedupStrings(nil) != nil {
		t.Error("nil input should pass through")
	}
}

// ─── unitFor ─────────────────────────────────────────────────────────

func TestUnitFor(t *testing.T) {
	cases := []struct {
		in   MetricKind
		want string
	}{
		{MetricCPU, "cores"},
		{MetricMemory, "bytes"},
		{MetricNetworkRX, "bytes/sec"},
		{MetricNetworkTX, "bytes/sec"},
		{MetricKind("garbage"), ""},
	}
	for _, c := range cases {
		if got := unitFor(c.in); got != c.want {
			t.Errorf("unitFor(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── queryRange / queryInstant against a fake VM ─────────────────────

// withFakeVM points the package-level VM URL at the given httptest server
// for the duration of fn and restores it after.
func withFakeVM(t *testing.T, srv *httptest.Server, fn func()) {
	t.Helper()
	prev := vmURLTestOverride
	vmURLTestOverride = srv.URL
	t.Cleanup(func() {
		vmURLTestOverride = prev
	})
	fn()
}

func TestQueryRange_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/v1/query_range") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("query") == "" {
			t.Error("missing query")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "matrix",
				"result": [{
					"metric": {"workload_name": "api"},
					"values": [
						[1700000000, "0.12"],
						[1700000060, "0.18"],
						[1700000120, "0.25"]
					]
				}]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	withFakeVM(t, srv, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		now := time.Unix(1700000120, 0)
		points, err := queryRange(ctx, `sum(rate(foo[1m]))`, now.Add(-2*time.Minute), now, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(points) != 3 {
			t.Fatalf("got %d points, want 3", len(points))
		}
		if points[0].V != 0.12 || points[2].V != 0.25 {
			t.Errorf("values mismatch: %+v", points)
		}
	})
}

func TestQueryRange_EmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	t.Cleanup(srv.Close)

	withFakeVM(t, srv, func() {
		ctx := context.Background()
		points, err := queryRange(ctx, `sum(foo)`, time.Unix(0, 0), time.Unix(60, 0), time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if points != nil {
			t.Errorf("expected nil points for empty result, got %v", points)
		}
	})
}

func TestQueryRange_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","error":"invalid PromQL"}`))
	}))
	t.Cleanup(srv.Close)

	withFakeVM(t, srv, func() {
		ctx := context.Background()
		_, err := queryRange(ctx, `garbage`, time.Unix(0, 0), time.Unix(60, 0), time.Minute)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "400") {
			t.Errorf("expected 400 in error, got %v", err)
		}
	})
}

func TestQueryInstant_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/v1/query") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "vector",
				"result": [{
					"metric": {},
					"value": [1700000000, "0.5"]
				}]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	withFakeVM(t, srv, func() {
		v, ok, err := queryInstant(context.Background(), `sum(foo)`, time.Unix(1700000000, 0))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected ok=true")
		}
		if v != 0.5 {
			t.Errorf("value: got %v, want 0.5", v)
		}
	})
}

func TestQueryInstant_NoSeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)

	withFakeVM(t, srv, func() {
		_, ok, err := queryInstant(context.Background(), `sum(missing)`, time.Now())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Error("expected ok=false for empty result")
		}
	})
}

func TestParseFloatFromVM(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want float64
		ok   bool
	}{
		{"string", "1.5", 1.5, true},
		{"scientific", "1.5e3", 1500, true},
		{"float64", float64(2.5), 2.5, true},
		{"NaN string", "NaN", 0, false},
		{"empty string", "", 0, false},
		{"unsupported type", []int{1}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := parseFloatFromVM(c.in)
			if ok != c.ok {
				t.Errorf("ok: got %v, want %v", ok, c.ok)
			}
			if ok && v != c.want {
				t.Errorf("val: got %v, want %v", v, c.want)
			}
		})
	}
}
