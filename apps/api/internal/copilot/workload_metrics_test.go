package copilot

import (
	"math"
	"strings"
	"testing"
	"time"
)

// Golden-output tests for the PromQL builders. They exist to fail loud when
// someone changes a selector by accident — the spec ties the LLM's expected
// behavior to the query shapes, so a silent shift here means the model sees
// data it didn't ask for. When the spec deliberately changes, update these
// strings AND the spec in one commit.

func TestParseMetricsRange(t *testing.T) {
	now := time.Date(2026, 5, 21, 14, 0, 0, 0, time.UTC)

	cases := []struct {
		in           string
		wantDuration time.Duration
		wantStep     time.Duration
		wantRate     time.Duration
		wantErr      bool
	}{
		{"5m", 5 * time.Minute, 25 * time.Second, 1 * time.Minute, false},
		{"15m", 15 * time.Minute, 75 * time.Second, 1 * time.Minute, false},
		{"1h", 1 * time.Hour, 5 * time.Minute, 5 * time.Minute, false},
		{"6h", 6 * time.Hour, 30 * time.Minute, 5 * time.Minute, false},
		{"24h", 24 * time.Hour, 2 * time.Hour, 15 * time.Minute, false},
		{"", 15 * time.Minute, 75 * time.Second, 1 * time.Minute, false}, // default
		{"30m", 0, 0, 0, true},
		{"1d", 0, 0, 0, true},
		{"garbage", 0, 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			start, end, spec, err := parseMetricsRange(c.in, now)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got none", c.in)
				}
				if !strings.Contains(err.Error(), "valid:") {
					t.Errorf("error %q should list valid values", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if spec.Duration != c.wantDuration {
				t.Errorf("duration: want %v, got %v", c.wantDuration, spec.Duration)
			}
			if spec.Step != c.wantStep {
				t.Errorf("step: want %v, got %v", c.wantStep, spec.Step)
			}
			if spec.RateWindow != c.wantRate {
				t.Errorf("rate window: want %v, got %v", c.wantRate, spec.RateWindow)
			}
			if !start.Equal(end.Add(-c.wantDuration)) {
				t.Errorf("start should be end - duration, got start=%v end=%v", start, end)
			}
		})
	}
}

func TestPromBuilder_CPU_Workload(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "api",
		clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	got := b.buildCPU()
	want := `sum by (workload_kind, workload_name) (rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="default",workload_kind="Deployment",workload_name="api"}[1m]))`
	if got != want {
		t.Errorf("CPU workload PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_CPU_Workload_PerContainer(t *testing.T) {
	b := promBuilder{
		kind:         "StatefulSet",
		namespace:    "argocd",
		name:         "argo-argocd-application-controller",
		clusterUID:   "uid-abc",
		perContainer: true,
		rateWindow:   1 * time.Minute,
	}
	got := b.buildCPU()
	want := `sum by (workload_kind, workload_name, container) (rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="argocd",workload_kind="StatefulSet",workload_name="argo-argocd-application-controller"}[1m]))`
	if got != want {
		t.Errorf("CPU per-container PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_CPU_Pod(t *testing.T) {
	b := promBuilder{
		kind:       "Pod",
		namespace:  "default",
		name:       "api-7c5d-abcd1",
		clusterUID: "uid-abc",
		rateWindow: 5 * time.Minute,
	}
	got := b.buildCPU()
	want := `sum by (pod) (rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="default",pod="api-7c5d-abcd1"}[5m]))`
	if got != want {
		t.Errorf("CPU pod PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_Memory_Workload(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "api",
		clusterUID: "uid-abc",
	}
	got := b.buildMemory()
	want := `sum by (workload_kind, workload_name) (container_memory_working_set_bytes{cluster_id="uid-abc",namespace="default",workload_kind="Deployment",workload_name="api"})`
	if got != want {
		t.Errorf("memory workload PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_Memory_Pod_PerContainer(t *testing.T) {
	b := promBuilder{
		kind:         "Pod",
		namespace:    "default",
		name:         "multi-container-pod",
		clusterUID:   "uid-abc",
		perContainer: true,
	}
	got := b.buildMemory()
	want := `sum by (pod, container) (container_memory_working_set_bytes{cluster_id="uid-abc",namespace="default",pod="multi-container-pod"})`
	if got != want {
		t.Errorf("memory pod per-container PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_Network_Workload(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "api",
		clusterUID: "uid-abc",
		pods:       []string{"api-7c5d-abcd1", "api-7c5d-efgh2"},
		rateWindow: 1 * time.Minute,
	}
	gotRX := b.buildNetwork(MetricNetworkRX)
	wantRX := `sum(rate(container_network_receive_bytes_total{cluster_id="uid-abc",namespace="default",pod=~"api-7c5d-abcd1|api-7c5d-efgh2"}[1m]))`
	if gotRX != wantRX {
		t.Errorf("network RX PromQL mismatch:\n  want: %s\n  got:  %s", wantRX, gotRX)
	}
	gotTX := b.buildNetwork(MetricNetworkTX)
	wantTX := `sum(rate(container_network_transmit_bytes_total{cluster_id="uid-abc",namespace="default",pod=~"api-7c5d-abcd1|api-7c5d-efgh2"}[1m]))`
	if gotTX != wantTX {
		t.Errorf("network TX PromQL mismatch:\n  want: %s\n  got:  %s", wantTX, gotTX)
	}
}

func TestPromBuilder_Network_Pod(t *testing.T) {
	b := promBuilder{
		kind:       "Pod",
		namespace:  "default",
		name:       "single-pod",
		clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	got := b.buildNetwork(MetricNetworkRX)
	want := `sum(rate(container_network_receive_bytes_total{cluster_id="uid-abc",namespace="default",pod="single-pod"}[1m]))`
	if got != want {
		t.Errorf("network pod PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_RequestsLimits(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "api",
		clusterUID: "uid-abc",
		pods:       []string{"api-7c5d-abcd1", "api-7c5d-efgh2"},
	}
	gotReq := b.buildRequestsLimits("cpu", "requests")
	wantReq := `sum(kube_pod_container_resource_requests{cluster_id="uid-abc",namespace="default",pod=~"api-7c5d-abcd1|api-7c5d-efgh2",resource="cpu"})`
	if gotReq != wantReq {
		t.Errorf("requests PromQL mismatch:\n  want: %s\n  got:  %s", wantReq, gotReq)
	}
	gotLim := b.buildRequestsLimits("memory", "limits")
	wantLim := `sum(kube_pod_container_resource_limits{cluster_id="uid-abc",namespace="default",pod=~"api-7c5d-abcd1|api-7c5d-efgh2",resource="memory"})`
	if gotLim != wantLim {
		t.Errorf("limits PromQL mismatch:\n  want: %s\n  got:  %s", wantLim, gotLim)
	}
}

func TestPromBuilder_RequestsLimits_Pod(t *testing.T) {
	// Pod-kind ignores the pods slice — uses pod="<name>" directly.
	b := promBuilder{
		kind:       "Pod",
		namespace:  "default",
		name:       "single-pod",
		clusterUID: "uid-abc",
	}
	got := b.buildRequestsLimits("cpu", "requests")
	want := `sum(kube_pod_container_resource_requests{cluster_id="uid-abc",namespace="default",pod="single-pod",resource="cpu"})`
	if got != want {
		t.Errorf("pod requests PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_RequestsLimits_NoPodsFallback(t *testing.T) {
	// Workload with zero resolved pods must produce a query that returns
	// nothing rather than crash or skip the join — the spec says the tool
	// still answers, just without utilizationPercent.
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "api",
		clusterUID: "uid-abc",
		pods:       nil,
	}
	got := b.buildRequestsLimits("cpu", "requests")
	if !strings.Contains(got, "__no_pods__") {
		t.Errorf("zero-pod query must use the no-match sentinel:\n  got: %s", got)
	}
}

func TestPodRegex_Escapes_Dots(t *testing.T) {
	// Pod names with dots (StatefulSet headless service style) must not be
	// treated as regex wildcards. RFC1123 names don't include other
	// metachars, so dot is the only one that matters in practice.
	got := podRegex([]string{"pod-a.cluster.local", "pod-b"})
	want := `pod-a\.cluster\.local|pod-b`
	if got != want {
		t.Errorf("regex escape mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{1 * time.Minute, "1m"},
		{5 * time.Minute, "5m"},
		{15 * time.Minute, "15m"},
		{1 * time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{25 * time.Second, "25s"},
		{75 * time.Second, "75s"},
		{30 * time.Minute, "30m"},
	}
	for _, c := range cases {
		if got := promDuration(c.in); got != c.want {
			t.Errorf("promDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSummarize_Basic(t *testing.T) {
	now := time.Now()
	pts := []metricPoint{
		{T: now, V: 1.0},
		{T: now.Add(time.Minute), V: 2.0},
		{T: now.Add(2 * time.Minute), V: 3.0},
		{T: now.Add(3 * time.Minute), V: 4.0},
		{T: now.Add(4 * time.Minute), V: 5.0},
	}
	s := summarize(pts)
	if s.Min != 1.0 {
		t.Errorf("min: want 1, got %v", s.Min)
	}
	if s.Max != 5.0 {
		t.Errorf("max: want 5, got %v", s.Max)
	}
	if s.Avg != 3.0 {
		t.Errorf("avg: want 3, got %v", s.Avg)
	}
	// p95 of 5 values with nearest-rank: ceil(0.95*5)=5 → index 4 → 5.0
	if s.P95 != 5.0 {
		t.Errorf("p95: want 5, got %v", s.P95)
	}
}

func TestSummarize_Empty(t *testing.T) {
	s := summarize(nil)
	if s != (metricSummary{}) {
		t.Errorf("empty input should return zero summary, got %+v", s)
	}
}

func TestSummarize_SkipsNaN(t *testing.T) {
	now := time.Now()
	pts := []metricPoint{
		{T: now, V: 1.0},
		{T: now.Add(time.Minute), V: math.NaN()},
		{T: now.Add(2 * time.Minute), V: 3.0},
	}
	s := summarize(pts)
	if s.Min != 1.0 || s.Max != 3.0 {
		t.Errorf("NaN should be skipped, got min=%v max=%v", s.Min, s.Max)
	}
	if s.Avg != 2.0 {
		t.Errorf("avg should be (1+3)/2 = 2, got %v", s.Avg)
	}
}

func TestDownsample_Passthrough(t *testing.T) {
	pts := make([]metricPoint, 8)
	for i := range pts {
		pts[i] = metricPoint{V: float64(i)}
	}
	out := downsample(pts, 12)
	if len(out) != 8 {
		t.Errorf("len <= target should passthrough, got %d", len(out))
	}
}

func TestDownsample_Reduces(t *testing.T) {
	pts := make([]metricPoint, 60) // simulate VM returning more than the budget
	now := time.Now()
	for i := range pts {
		pts[i] = metricPoint{T: now.Add(time.Duration(i) * time.Minute), V: float64(i)}
	}
	out := downsample(pts, 12)
	if len(out) != 12 {
		t.Errorf("want 12 points, got %d", len(out))
	}
	// First bucket [0..5) averages 0..4 → 2; last bucket [55..60) avg 55..59 → 57.
	if out[0].V != 2.0 {
		t.Errorf("first bucket average: want 2, got %v", out[0].V)
	}
	if out[len(out)-1].V != 57.0 {
		t.Errorf("last bucket average: want 57, got %v", out[len(out)-1].V)
	}
}
