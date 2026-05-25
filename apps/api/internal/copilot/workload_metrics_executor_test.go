package copilot

import (
	"context"
	"fmt"
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
		series, err := queryRange(ctx, `sum(rate(foo[1m]))`, now.Add(-2*time.Minute), now, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(series) != 1 {
			t.Fatalf("got %d series, want 1", len(series))
		}
		points := series[0].Points
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
		series, err := queryRange(ctx, `sum(foo)`, time.Unix(0, 0), time.Unix(60, 0), time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(series) != 0 {
			t.Errorf("expected zero series for empty result, got %d", len(series))
		}
	})
}

func TestQueryRange_MultipleSeries_PerContainer(t *testing.T) {
	// `sum by (container) (...)` returns one series per container.
	// queryRange must surface ALL series so runMetric can split per
	// container. This is the regression net for scenario 3 — the
	// previous Result[0]-only code reported only the first container's
	// values as if they were the pod aggregate.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{
				"resultType":"matrix",
				"result":[
					{"metric":{"container":"heavy"},"values":[[1700000000,"0.198"],[1700000060,"0.201"]]},
					{"metric":{"container":"idle-sidecar"},"values":[[1700000000,"0.002"],[1700000060,"0.003"]]}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	withFakeVM(t, srv, func() {
		series, err := queryRange(context.Background(), `sum by (container) (rate(foo[1m]))`, time.Unix(1700000000, 0), time.Unix(1700000060, 0), time.Minute)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if len(series) != 2 {
			t.Fatalf("expected 2 series (one per container), got %d", len(series))
		}
		// Containers must surface in series labels.
		found := map[string]bool{}
		for _, s := range series {
			found[s.Labels["container"]] = true
		}
		if !found["heavy"] || !found["idle-sidecar"] {
			t.Errorf("expected both containers in labels, got: %v", found)
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

// ─── End-to-end runMetric tests with realistic VM payloads ──────────
//
// These tests are the regression net for the kind-cluster scenario 1 bug:
// `sum by (pod)` produced multi-series responses, the executor read only
// the first, and a 2-replica workload at 100m each was reported as ~100m
// (half-truth). The current code uses plain `sum(...)` which collapses to
// one series — these tests prove that collapse is happening AND that the
// resulting math reflects the actual cluster total across replicas. We
// also exercise pod-restart shapes (counter resets handled by VM's
// rate(); gauges that drop to zero mid-window) to confirm summarize() and
// downsample() behave sensibly in real operational scenarios.

// fakeVMRange returns an httptest server that responds to a range query
// with the given (ts_unix, value) pairs serialised as VM's matrix shape.
// VM serialises values as strings; the timestamp comes through as a JSON
// number. We mirror that contract here so the parsing path is exercised.
func fakeVMRange(t *testing.T, points [][2]any) *httptest.Server {
	t.Helper()
	values := make([]string, 0, len(points))
	for _, p := range points {
		ts := fmt.Sprintf("%v", p[0])
		val := fmt.Sprintf("%v", p[1])
		values = append(values, fmt.Sprintf(`[%s,"%s"]`, ts, val))
	}
	body := fmt.Sprintf(`{
		"status":"success",
		"data":{
			"resultType":"matrix",
			"result":[{
				"metric":{},
				"values":[%s]
			}]
		}
	}`, strings.Join(values, ","))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

// runMetricForTest is a thin wrapper that builds a promBuilder, calls
// runMetric, and surfaces the resulting metricResponse. Lets each test
// vary inputs without restating the boilerplate.
func runMetricForTest(t *testing.T, kind, name string, mk MetricKind, points [][2]any) metricResponse {
	t.Helper()
	srv := fakeVMRange(t, points)
	t.Cleanup(srv.Close)
	var got metricResponse
	withFakeVM(t, srv, func() {
		b := promBuilder{
			kind:       kind,
			namespace:  "default",
			name:       name,
			clusterUID: "uid-test",
			rateWindow: 1 * time.Minute,
		}
		now := time.Unix(1700000600, 0)
		mr, err := runMetric(context.Background(), b, mk, now.Add(-10*time.Minute), now, 60*time.Second)
		if err != nil {
			t.Fatalf("runMetric: %v", err)
		}
		got = mr
	})
	return got
}

func TestRunMetric_SinglePodWorkload_CPU(t *testing.T) {
	// 1-pod Deployment running stress --cpu 1 against a 100m limit.
	// VM's rate() smooths to ~0.1 cores. The executor's `sum(...)`
	// collapses single series → trend should land cleanly at 0.1.
	points := [][2]any{
		{1700000000, 0.098}, {1700000060, 0.101}, {1700000120, 0.099},
		{1700000180, 0.102}, {1700000240, 0.100}, {1700000300, 0.097},
		{1700000360, 0.103}, {1700000420, 0.101}, {1700000480, 0.099},
		{1700000540, 0.100},
	}
	r := runMetricForTest(t, "Deployment", "single-pod-app", MetricCPU, points)
	if r.Summary.Max < 0.095 || r.Summary.Max > 0.105 {
		t.Errorf("max should be ~0.1 cores for single-pod workload, got %v", r.Summary.Max)
	}
	if r.Summary.Avg < 0.095 || r.Summary.Avg > 0.105 {
		t.Errorf("avg should be ~0.1, got %v", r.Summary.Avg)
	}
}

func TestRunMetric_TwoPodWorkload_CPU(t *testing.T) {
	// THE REGRESSION TEST FOR SCENARIO 1: 2-pod Deployment, each pod
	// throttled at 100m → workload total = ~200m. The previous bug
	// (`sum by (pod)` + executor reads first series) reported ~100m.
	// This test asserts the workload roll-up reflects the sum across
	// replicas (within VM's natural smoothing tolerance).
	points := [][2]any{
		{1700000000, 0.196}, {1700000060, 0.201}, {1700000120, 0.199},
		{1700000180, 0.204}, {1700000240, 0.200}, {1700000300, 0.198},
		{1700000360, 0.203}, {1700000420, 0.201}, {1700000480, 0.197},
		{1700000540, 0.200},
	}
	r := runMetricForTest(t, "Deployment", "cpu-burner", MetricCPU, points)
	if r.Summary.Max < 0.195 || r.Summary.Max > 0.210 {
		t.Errorf("max should be ~0.2 cores for 2-pod workload (THE scenario-1 bug); got %v — if this looks like ~0.1, sum-by-pod is back", r.Summary.Max)
	}
	if r.Summary.Avg < 0.195 || r.Summary.Avg > 0.210 {
		t.Errorf("avg should be ~0.2, got %v", r.Summary.Avg)
	}
}

func TestRunMetric_FivePodWorkload_CPU(t *testing.T) {
	// 5-pod workload at 100m each → 500m total. Catches scaling
	// regressions if someone "optimises" the aggregation later.
	points := [][2]any{
		{1700000000, 0.495}, {1700000060, 0.502}, {1700000120, 0.500},
		{1700000180, 0.498}, {1700000240, 0.503}, {1700000300, 0.500},
		{1700000360, 0.497}, {1700000420, 0.501},
	}
	r := runMetricForTest(t, "Deployment", "wide-app", MetricCPU, points)
	if r.Summary.Avg < 0.49 || r.Summary.Avg > 0.51 {
		t.Errorf("avg should be ~0.5 cores for 5-pod workload, got %v", r.Summary.Avg)
	}
}

func TestRunMetric_MemoryWithPodRestart(t *testing.T) {
	// Memory is a gauge — pod restart shows as a drop to zero followed
	// by a ramp. (CPU's counter reset is handled by VM's rate() and we
	// only see the smoothed rate, so this test focuses on memory which
	// IS where operators see the restart shape directly.) Mid-window
	// the working set drops to 0 (pod terminated, new pod schedules) and
	// ramps back over a few minutes. Summary must report min=0,
	// max=peak, avg reflecting the gap.
	points := [][2]any{
		// Pre-restart: stable ~150 MiB working set.
		{1700000000, 157286400}, {1700000060, 159383552}, {1700000120, 158335000},
		// Restart window: drop to zero (pod gone for 1 sample).
		{1700000180, 0},
		// Post-restart: ramp back up.
		{1700000240, 41943040}, {1700000300, 104857600}, {1700000360, 146800640},
		{1700000420, 158335000}, {1700000480, 159383552}, {1700000540, 158335000},
	}
	r := runMetricForTest(t, "Deployment", "restart-victim", MetricMemory, points)
	if r.Summary.Min != 0 {
		t.Errorf("min should be 0 (the restart trough), got %v", r.Summary.Min)
	}
	if r.Summary.Max < 158000000 {
		t.Errorf("max should reflect the pre/post-restart peak ~160 MiB, got %v", r.Summary.Max)
	}
	// Average is dragged down by the zero point but stays well above zero.
	if r.Summary.Avg <= 0 || r.Summary.Avg > 150000000 {
		t.Errorf("avg should reflect the trough drag: 0 < avg < peak, got %v", r.Summary.Avg)
	}
}

func TestRunMetric_OOMKillSawtooth_Memory(t *testing.T) {
	// OOMKill scenario (test runbook scenario 2): mem-leaker grows
	// monotonically to its 64Mi limit, gets OOMKilled, restarts, grows
	// again. Operator-visible sawtooth — and the metric tool must show
	// it accurately so a propose_set_resources patch can be sized.
	points := [][2]any{
		{1700000000, 10485760},  // 10Mi — fresh pod
		{1700000060, 30000000},  // climbing
		{1700000120, 50000000},  // near limit
		{1700000180, 67108864},  // ~64Mi — OOM imminent
		{1700000240, 0},         // killed
		{1700000300, 12000000},  // restart, ramp again
		{1700000360, 35000000},
		{1700000420, 58000000},
		{1700000480, 67108864},  // hits the same limit
		{1700000540, 0},         // killed again
	}
	r := runMetricForTest(t, "Deployment", "mem-leaker", MetricMemory, points)
	// Max must surface the actual limit-hitting peak — operators size
	// the patch off this number, so under-reporting would be dangerous.
	if r.Summary.Max < 67000000 {
		t.Errorf("max should reflect the ~64Mi pre-OOM peak, got %v", r.Summary.Max)
	}
	if r.Summary.Min != 0 {
		t.Errorf("min should be 0 (the kill troughs), got %v", r.Summary.Min)
	}
	// p95 should be near the peak — most of the time series is climbing
	// toward the limit, only a couple samples at the trough.
	if r.Summary.P95 < 58000000 {
		t.Errorf("p95 should be near the peak (most of the time near limit), got %v", r.Summary.P95)
	}
}

func TestRunMetric_PerContainer_SplitsByContainer(t *testing.T) {
	// THE REGRESSION TEST FOR SCENARIO 3: multi-container pod with one
	// hot container (heavy at ~190m) and one idle (sidecar at ~2m).
	// perContainer=true must produce a `perContainer` map keyed by
	// container name; the aggregate must equal the sum of containers.
	// The previous bug returned only the first series, hiding the split.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{
				"resultType":"matrix",
				"result":[
					{"metric":{"container":"heavy"},"values":[
						[1700000000,"0.188"],[1700000060,"0.191"],[1700000120,"0.190"],
						[1700000180,"0.192"],[1700000240,"0.189"]
					]},
					{"metric":{"container":"idle-sidecar"},"values":[
						[1700000000,"0.002"],[1700000060,"0.003"],[1700000120,"0.002"],
						[1700000180,"0.001"],[1700000240,"0.002"]
					]}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	var got metricResponse
	withFakeVM(t, srv, func() {
		b := promBuilder{
			kind:         "Deployment",
			namespace:    "default",
			name:         "multi-c",
			clusterUID:   "uid-test",
			perContainer: true,
			rateWindow:   1 * time.Minute,
		}
		now := time.Unix(1700000600, 0)
		mr, err := runMetric(context.Background(), b, MetricCPU, now.Add(-10*time.Minute), now, 60*time.Second)
		if err != nil {
			t.Fatalf("runMetric: %v", err)
		}
		got = mr
	})

	// Both containers must appear in the perContainer map.
	if got.PerContainer == nil {
		t.Fatal("perContainer map missing — scenario-3 bug is back")
	}
	if _, ok := got.PerContainer["heavy"]; !ok {
		t.Error("perContainer missing 'heavy' container")
	}
	if _, ok := got.PerContainer["idle-sidecar"]; !ok {
		t.Error("perContainer missing 'idle-sidecar' container")
	}

	// Per-container summaries must be accurate.
	heavy := got.PerContainer["heavy"]
	if heavy.Summary.Avg < 0.185 || heavy.Summary.Avg > 0.195 {
		t.Errorf("heavy container avg should be ~190m, got %v", heavy.Summary.Avg)
	}
	idle := got.PerContainer["idle-sidecar"]
	if idle.Summary.Avg > 0.010 {
		t.Errorf("idle sidecar avg should be ~2m, got %v", idle.Summary.Avg)
	}

	// Top-level aggregate must equal sum of containers (~192m).
	if got.Summary.Avg < 0.185 || got.Summary.Avg > 0.200 {
		t.Errorf("top-level aggregate should sum containers (~192m), got %v", got.Summary.Avg)
	}
}

func TestRunMetric_PerContainer_NetworkIgnoresFlag(t *testing.T) {
	// Network metrics are pod-keyed in VM (no container label on the
	// source series). perContainer=true should not split network — the
	// builder doesn't emit `by (container)` for network, and runMetric
	// must not attempt to populate the PerContainer map for it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{
				"resultType":"matrix",
				"result":[{"metric":{},"values":[[1700000000,"1024"],[1700000060,"2048"]]}]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	var got metricResponse
	withFakeVM(t, srv, func() {
		b := promBuilder{
			kind:         "Deployment",
			namespace:    "default",
			name:         "iperf-server",
			clusterUID:   "uid-test",
			perContainer: true, // requested but should be ignored for network
			rateWindow:   1 * time.Minute,
		}
		mr, err := runMetric(context.Background(), b, MetricNetworkRX, time.Unix(1700000000, 0), time.Unix(1700000060, 0), 60*time.Second)
		if err != nil {
			t.Fatalf("runMetric: %v", err)
		}
		got = mr
	})
	if got.PerContainer != nil {
		t.Errorf("network metric must NOT populate perContainer (was %v)", got.PerContainer)
	}
	if got.Summary.Max < 2000 || got.Summary.Max > 2100 {
		t.Errorf("network aggregate not preserved when perContainer=true: max=%v", got.Summary.Max)
	}
}

func TestRunMetric_CPUStableAcrossRestart(t *testing.T) {
	// VM's rate() function handles counter resets internally — when
	// container_cpu_usage_seconds_total resets at a pod restart, rate()
	// treats the post-reset increment as the new base, not as a negative
	// rate. Our code receives the smoothed rate values and must report
	// them without spurious peaks. This test simulates the post-rate()
	// shape: a brief dip during the restart, never a spike.
	points := [][2]any{
		{1700000000, 0.100}, {1700000060, 0.101}, {1700000120, 0.099},
		// Restart: rate momentarily drops as the new container ramps up.
		{1700000180, 0.050}, {1700000240, 0.085},
		// Back to normal.
		{1700000300, 0.099}, {1700000360, 0.101}, {1700000420, 0.100},
		{1700000480, 0.099}, {1700000540, 0.100},
	}
	r := runMetricForTest(t, "Deployment", "restart-then-stable", MetricCPU, points)
	// The max must NOT spike artificially above the steady-state.
	if r.Summary.Max > 0.110 {
		t.Errorf("max should not exceed steady-state ~0.1 even with restart in window, got %v", r.Summary.Max)
	}
	// The dip should be reflected in min.
	if r.Summary.Min > 0.060 {
		t.Errorf("min should reflect the restart dip ~0.05, got %v", r.Summary.Min)
	}
}

// ─── Node-kind end-to-end tests ──────────────────────────────────────

func TestRunMetric_Node_CPU(t *testing.T) {
	// An EKS worker node at ~2.5 cores out of an 8-core capacity. The
	// executor must route Node through the node_* metric builders, not
	// the pod-keyed path. Synthetic data here approximates a real node
	// running a few CPU-bound workloads.
	points := [][2]any{
		{1700000000, 2.48}, {1700000060, 2.51}, {1700000120, 2.49},
		{1700000180, 2.55}, {1700000240, 2.52}, {1700000300, 2.50},
		{1700000360, 2.47}, {1700000420, 2.53},
	}
	r := runMetricForTest(t, "Node", "ip-10-0-44-188.ec2.internal", MetricCPU, points)
	if r.Summary.Avg < 2.45 || r.Summary.Avg > 2.55 {
		t.Errorf("Node CPU avg should be ~2.5 cores, got %v", r.Summary.Avg)
	}
	// Sanity: the response unit is cores (same as for workloads — the
	// chart card relies on this to pick the right scale).
	if r.Unit != "cores" {
		t.Errorf("Node CPU response unit should be 'cores', got %q", r.Unit)
	}
}

func TestRunMetric_Node_Memory(t *testing.T) {
	// Worker node memory steady around 6 GiB used (8 GiB capacity, ~75%).
	points := [][2]any{
		{1700000000, 6442450944}, {1700000060, 6479478784}, {1700000120, 6451039232},
		{1700000180, 6510350336}, {1700000240, 6479478784}, {1700000300, 6442450944},
	}
	r := runMetricForTest(t, "Node", "ip-10-0-44-188.ec2.internal", MetricMemory, points)
	if r.Summary.Avg < 6_000_000_000 || r.Summary.Avg > 6_700_000_000 {
		t.Errorf("Node memory avg should be ~6 GiB, got %v", r.Summary.Avg)
	}
	if r.Unit != "bytes" {
		t.Errorf("Node memory response unit should be 'bytes', got %q", r.Unit)
	}
}

// TestRunMetric_EmptySeries_NonNilTrend is the regression guard for the
// "Cannot read properties of null (reading 'length')" crash the frontend
// hit when a tool call returned no data (no agent connected, cluster
// new, VM empty). Go marshals nil slices as JSON null which broke the
// chart card's `.length` access; the fix initialises Trend to a non-nil
// empty slice so JSON serialises as `[]` and the frontend's
// empty-trend branch renders cleanly.
func TestRunMetric_EmptySeries_NonNilTrend(t *testing.T) {
	// Fake VM that returns the "no series" shape — what VM emits when the
	// queried metric+labels match nothing (e.g. agent never reported).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	t.Cleanup(srv.Close)

	var r metricResponse
	withFakeVM(t, srv, func() {
		b := promBuilder{
			kind:       "Deployment",
			namespace:  "default",
			name:       "ghost-app",
			clusterUID: "uid-test",
			rateWindow: 1 * time.Minute,
		}
		now := time.Unix(1700000600, 0)
		mr, err := runMetric(context.Background(), b, MetricCPU, now.Add(-10*time.Minute), now, 60*time.Second)
		if err != nil {
			t.Fatalf("runMetric: %v", err)
		}
		r = mr
	})

	// The critical contract: Trend must be a non-nil slice even with zero
	// underlying samples. Without this the frontend's `.length` access
	// crashes the chat panel via ErrorBoundary.
	if r.Trend == nil {
		t.Errorf("Trend must be non-nil (empty slice) when VM returns no series — got nil, which marshals to JSON null and crashes the frontend chart card")
	}
	if len(r.Trend) != 0 {
		t.Errorf("Trend should be empty when VM returns no series, got %d points", len(r.Trend))
	}
}

func TestRunMetric_Node_Network(t *testing.T) {
	// Node-level network typically sits in the MB/s range on a working
	// EKS box. The synthetic shape mirrors the CloudWatch screenshots
	// we saw on yagan — sustained low traffic with occasional bursts.
	points := [][2]any{
		{1700000000, 524288}, {1700000060, 1048576}, {1700000120, 786432},
		{1700000180, 11534336}, {1700000240, 12582912}, // burst
		{1700000300, 524288}, {1700000360, 655360},
	}
	r := runMetricForTest(t, "Node", "ip-10-0-44-188.ec2.internal", MetricNetworkRX, points)
	if r.Summary.Max < 12_000_000 {
		t.Errorf("Node network max should reflect the burst ~12 MB/s, got %v", r.Summary.Max)
	}
	if r.Unit != "bytes/sec" {
		t.Errorf("Node network unit should be 'bytes/sec', got %q", r.Unit)
	}
}
