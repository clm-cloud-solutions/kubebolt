package copilot

import (
	"math"
	"regexp"
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
		// History ranges — the product's own selector offers these, so the tool
		// rejecting them was a dead end the operator reached through Kobi.
		{"7d", 7 * 24 * time.Hour, 14 * time.Hour, 15 * time.Minute, false},
		{"14d", 14 * 24 * time.Hour, 28 * time.Hour, 30 * time.Minute, false},
		{"30d", 30 * 24 * time.Hour, 60 * time.Hour, 1 * time.Hour, false},
		{"", 15 * time.Minute, 75 * time.Second, 1 * time.Minute, false}, // default
		{"30m", 0, 0, 0, true},
		{"1d", 0, 0, 0, true},
		{"90d", 0, 0, 0, true},
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
	want := `sum(rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""}[1m]))`
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
	want := `sum by (container) (rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="argocd",pod=~"argo-argocd-application-controller-[0-9]+",pod_uid!=""}[1m]))`
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
	want := `sum(rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="default",pod="api-7c5d-abcd1",pod_uid!=""}[5m]))`
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
	want := `sum(container_memory_working_set_bytes{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""})`
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
	want := `sum by (container) (container_memory_working_set_bytes{cluster_id="uid-abc",namespace="default",pod="multi-container-pod",pod_uid!=""})`
	if got != want {
		t.Errorf("memory pod per-container PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

// TestPromBuilder_WorkloadRollup_SingleSeries is the regression guard for the
// kind-cluster bug discovered in vivo on scenario 1: `sum by (pod)` produces
// N series (one per pod). The executor only reads VM's first series, so the
// reported total was just one pod's rate (half the truth for a 2-replica
// workload at the same usage). The workload roll-up MUST collapse to one
// series — verified here by asserting the query starts with `sum(` not
// `sum by`.
func TestPromBuilder_WorkloadRollup_SingleSeries(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "cpu-burner",
		clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	for label, q := range map[string]string{"cpu": b.buildCPU(), "memory": b.buildMemory()} {
		if strings.HasPrefix(q, "sum by") {
			t.Errorf("%s workload rollup must produce a single series (sum(...) not sum by ...):\n  %s", label, q)
		}
		if !strings.HasPrefix(q, "sum(") {
			t.Errorf("%s query should start with sum(:\n  %s", label, q)
		}
	}
}

func TestPromBuilder_Network_Workload(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "api",
		clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	gotRX := b.buildNetwork(MetricNetworkRX)
	wantRX := `sum(rate(container_network_receive_bytes_total{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""}[1m]))`
	if gotRX != wantRX {
		t.Errorf("network RX PromQL mismatch:\n  want: %s\n  got:  %s", wantRX, gotRX)
	}
	gotTX := b.buildNetwork(MetricNetworkTX)
	wantTX := `sum(rate(container_network_transmit_bytes_total{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""}[1m]))`
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
	want := `sum(rate(container_network_receive_bytes_total{cluster_id="uid-abc",namespace="default",pod="single-pod",pod_uid!=""}[1m]))`
	if got != want {
		t.Errorf("network pod PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

// TestPromBuilder_CPU_Deployment_NotByWorkloadLabel is the regression guard
// for the kind-cluster bug discovered in vivo: agent emits
// workload_kind=ReplicaSet for Deployment-owned pods (only walks the direct
// ownerRef), so the prior query that filtered on workload_kind="Deployment"
// matched ZERO series and the tool reported 0 cores on a hot deployment.
// Lock in that the builder no longer emits the workload_kind label at all.
func TestPromBuilder_CPU_Deployment_NotByWorkloadLabel(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "cpu-burner",
		clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	got := b.buildCPU()
	if strings.Contains(got, "workload_kind") {
		t.Errorf("builder still references workload_kind — Deployment query will return 0 series:\n  %s", got)
	}
	if strings.Contains(got, "workload_name") {
		t.Errorf("builder still references workload_name:\n  %s", got)
	}
	if !strings.Contains(got, `pod_uid!=""`) {
		t.Errorf("builder missing pod_uid!=\"\" guard against duplicate Prometheus-scraped series:\n  %s", got)
	}
}

func TestPromBuilder_RequestsLimits(t *testing.T) {
	b := promBuilder{
		kind:       "Deployment",
		namespace:  "default",
		name:       "api",
		clusterUID: "uid-abc",
	}
	gotReq := b.buildRequestsLimits("cpu", "requests")
	wantReq := `sum(kube_pod_container_resource_requests{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",resource="cpu"})`
	if gotReq != wantReq {
		t.Errorf("requests PromQL mismatch:\n  want: %s\n  got:  %s", wantReq, gotReq)
	}
	gotLim := b.buildRequestsLimits("memory", "limits")
	wantLim := `sum(kube_pod_container_resource_limits{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",resource="memory"})`
	if gotLim != wantLim {
		t.Errorf("limits PromQL mismatch:\n  want: %s\n  got:  %s", wantLim, gotLim)
	}
}

func TestPromBuilder_RequestsLimits_Pod(t *testing.T) {
	// Pod-kind uses literal pod="<name>" (no regex needed — pods don't rotate).
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

func TestPodNamePattern_PerKind(t *testing.T) {
	// Pins the controller-specific naming conventions. If kubelet/kube
	// changes a naming scheme upstream (rare but happened with the
	// rs-template-hash format change), update these AND verify in vivo.
	cases := []struct {
		kind string
		name string
		want string
	}{
		{"Deployment", "api", `api-[a-z0-9]+-[a-z0-9]+`},
		{"Deployment", "cpu-burner", `cpu-burner-[a-z0-9]+-[a-z0-9]+`},
		{"StatefulSet", "redis", `redis-[0-9]+`},
		{"DaemonSet", "kubebolt-agent", `kubebolt-agent-[a-z0-9]+`},
		{"Job", "backup-1700000000", `backup-1700000000-[a-z0-9]+`},
		{"CronJob", "backup", `backup-[0-9]+-[a-z0-9]+`},
	}
	for _, c := range cases {
		t.Run(c.kind+"/"+c.name, func(t *testing.T) {
			b := promBuilder{kind: c.kind, name: c.name}
			got := b.podNamePattern()
			if got != c.want {
				t.Errorf("pattern mismatch for %s %q:\n  want: %s\n  got:  %s", c.kind, c.name, c.want, got)
			}
		})
	}
}

func TestPodNamePattern_CapturesAllGenerations(t *testing.T) {
	// The whole point of the pattern approach: a 1h range query on a
	// Deployment that rolled 30 min ago must capture pods from BOTH the
	// old ReplicaSet (since deleted) and the new one. Pattern-match it
	// regex-side rather than asking the connector to time-travel.
	b := promBuilder{kind: "Deployment", name: "cpu-burner"}
	pattern := b.podNamePattern()
	matches := []string{
		"cpu-burner-596b7dc5f9-95nx9", // RS 1
		"cpu-burner-596b7dc5f9-bkdr8", // RS 1
		"cpu-burner-7d8f4cab2e-abcd1", // RS 2 (after rolling update)
	}
	// Anchored full-match matches VM/RE2's implicit-anchor semantics.
	re, err := regexp.Compile(`^` + pattern + `$`)
	if err != nil {
		t.Fatalf("pattern compile: %v", err)
	}
	for _, p := range matches {
		if !re.MatchString(p) {
			t.Errorf("pattern %q should match pod name %q (cross-generation requirement)", pattern, p)
		}
	}
	// Prefix collisions must NOT match.
	nonMatches := []string{
		"cpu-burner2-596b7dc5f9-95nx9", // different deployment "cpu-burner2"
		"other-cpu-burner-596b-abcd1",  // would only collide if anchors were missing
	}
	for _, p := range nonMatches {
		if re.MatchString(p) {
			t.Errorf("pattern %q must NOT match unrelated pod name %q", pattern, p)
		}
	}
}

func TestPodNamePattern_EscapesWorkloadName(t *testing.T) {
	// K8s names are RFC1123 (no regex metachars in practice), but the
	// builder must not trust that — a future kind or admin tool could
	// emit a name with `.` or similar. regexp.QuoteMeta guards us.
	b := promBuilder{kind: "Deployment", name: "weird.name"}
	pattern := b.podNamePattern()
	if !strings.Contains(pattern, `weird\.name`) {
		t.Errorf("pattern must escape regex metachars in name:\n  %s", pattern)
	}
}

// ─── Node-kind golden tests ──────────────────────────────────────────
//
// Node queries use entirely different metric names from workload queries
// (node_* instead of container_*) and don't carry a namespace or a pod
// regex. These tests pin the exact PromQL shape so a refactor that
// silently routes Node through the pod-keyed path fails loud.

func TestPromBuilder_CPU_Node(t *testing.T) {
	b := promBuilder{
		kind:       "Node",
		name:       "ip-10-0-44-188.ec2.internal",
		clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	got := b.buildCPU()
	want := `sum(rate(node_cpu_usage_seconds_total{cluster_id="uid-abc",node="ip-10-0-44-188.ec2.internal"}[1m]))`
	if got != want {
		t.Errorf("CPU node PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
	// Negative checks: must NOT carry pod-keyed labels.
	if strings.Contains(got, "container_cpu_usage_seconds_total") {
		t.Errorf("Node CPU query must use node_* metric, not container_*:\n  %s", got)
	}
	if strings.Contains(got, "pod_uid") {
		t.Errorf("Node CPU query must not filter on pod_uid (no such label on node series):\n  %s", got)
	}
	if strings.Contains(got, "namespace=") {
		t.Errorf("Node CPU query must not carry a namespace filter:\n  %s", got)
	}
}

func TestPromBuilder_Memory_Node(t *testing.T) {
	b := promBuilder{
		kind:       "Node",
		name:       "kubebolt-dev-worker",
		clusterUID: "uid-abc",
	}
	got := b.buildMemory()
	want := `sum(node_memory_working_set_bytes{cluster_id="uid-abc",node="kubebolt-dev-worker"})`
	if got != want {
		t.Errorf("memory node PromQL mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

func TestPromBuilder_Network_Node_FiltersVirtualInterfaces(t *testing.T) {
	// The device whitelist is critical — sum without it would include
	// every veth/lxc/cilium interface and double-count container traffic.
	// We learned this from the yagan/CloudWatch/Grafana comparison.
	b := promBuilder{
		kind:       "Node",
		name:       "kubebolt-dev-worker",
		clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	gotRX := b.buildNetwork(MetricNetworkRX)
	wantRX := `sum(rate(node_network_receive_bytes_total{cluster_id="uid-abc",node="kubebolt-dev-worker",device=~"eth.*|ens.*|en[a-z].*"}[1m]))`
	if gotRX != wantRX {
		t.Errorf("network node RX PromQL mismatch:\n  want: %s\n  got:  %s", wantRX, gotRX)
	}
	gotTX := b.buildNetwork(MetricNetworkTX)
	wantTX := `sum(rate(node_network_transmit_bytes_total{cluster_id="uid-abc",node="kubebolt-dev-worker",device=~"eth.*|ens.*|en[a-z].*"}[1m]))`
	if gotTX != wantTX {
		t.Errorf("network node TX PromQL mismatch:\n  want: %s\n  got:  %s", wantTX, gotTX)
	}
}

func TestPromBuilder_RequestsLimits_Node(t *testing.T) {
	// For nodes: allocatable plays the role of "request", capacity plays
	// the role of "limit". This lets utilizationPercent vsLimit show "% of
	// node capacity" — operators already know to read it that way from
	// the Capacity dashboard's gauges.
	b := promBuilder{
		kind:       "Node",
		name:       "kubebolt-dev-worker",
		clusterUID: "uid-abc",
	}
	gotReq := b.buildRequestsLimits("cpu", "requests")
	wantReq := `sum(kube_node_status_allocatable{cluster_id="uid-abc",node="kubebolt-dev-worker",resource="cpu"})`
	if gotReq != wantReq {
		t.Errorf("node allocatable PromQL mismatch:\n  want: %s\n  got:  %s", wantReq, gotReq)
	}
	gotLim := b.buildRequestsLimits("memory", "limits")
	wantLim := `sum(kube_node_status_capacity{cluster_id="uid-abc",node="kubebolt-dev-worker",resource="memory"})`
	if gotLim != wantLim {
		t.Errorf("node capacity PromQL mismatch:\n  want: %s\n  got:  %s", wantLim, gotLim)
	}
}

func TestPromBuilder_PerContainer_IgnoredForNode(t *testing.T) {
	// Nodes don't have containers in this dimension. perContainer=true
	// on Node must NOT produce a sum by (container) — it should stay as
	// a single-series `sum(...)`. Otherwise the executor's per-container
	// routing would build an empty PerContainer map and confuse Kobi.
	b := promBuilder{
		kind:         "Node",
		name:         "ip-10-0-44-188.ec2.internal",
		clusterUID:   "uid-abc",
		perContainer: true, // requested but should be ignored
		rateWindow:   1 * time.Minute,
	}
	got := b.buildCPU()
	if strings.Contains(got, "by (container)") {
		t.Errorf("Node CPU must NOT split by container even when perContainer=true:\n  %s", got)
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

// Every range yields the same ~12 trend points. That invariant is what makes a
// wider window cost RESOLUTION rather than tokens, and it is the whole answer
// to "can we afford 30d": a month costs the model exactly what 15 minutes does.
func TestMetricsRangeTable_TwelvePointsEverywhere(t *testing.T) {
	for name, spec := range metricsRangeTable {
		points := int(spec.Duration / spec.Step)
		if points != trendTargetPoints {
			t.Errorf("range %s yields %d points (duration %s / step %s), want %d",
				name, points, spec.Duration, spec.Step, trendTargetPoints)
		}
	}
}

// Long ranges must summarise their step, not sample an instant inside it.
//
// Without a bucket, a 30d response is twelve 15-minute windows spread across a
// month — 0.4% of the timeline — and the peak that OOMKilled a pod last Tuesday
// is invisible unless it happened to land inside one of them. This tool's
// description makes it mandatory before proposing resource limits, so an
// under-reported peak here becomes an under-sized limit there.
func TestMetricsRangeTable_LongRangesSummariseTheirStep(t *testing.T) {
	for _, name := range []string{"6h", "24h", "7d", "14d", "30d"} {
		if metricsRangeTable[name].Bucket <= 0 {
			t.Errorf("range %s samples an instant per point instead of its step's peak", name)
		}
	}
	// Below an hour the step is at or under the rate window, so a bucket would
	// re-smooth what rate() already smoothed — noise, not information.
	for _, name := range []string{"5m", "15m", "1h"} {
		if metricsRangeTable[name].Bucket != 0 {
			t.Errorf("range %s buckets a step no wider than its rate window", name)
		}
	}
}

// The CPU peak has to be taken AFTER the sum: the workload's worst moment, not
// the sum of each pod's worst moment — a number no instant ever produced.
func TestPromBuilder_CPU_BucketedTakesThePeakOfTheSum(t *testing.T) {
	b := promBuilder{
		kind: "Deployment", namespace: "default", name: "api", clusterUID: "uid-abc",
		rateWindow: 15 * time.Minute, bucket: 14 * time.Hour,
	}
	want := `max_over_time(sum(rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""}[15m]))[14h:15m])`
	if got := b.buildCPU(); got != want {
		t.Errorf("bucketed CPU mismatch:\n  want: %s\n  got:  %s", want, got)
	}
}

// Memory is a gauge, so it takes a plain range — no subquery, and therefore no
// inner-evaluation cost at all.
func TestPromBuilder_Memory_BucketedUsesAPlainRange(t *testing.T) {
	b := promBuilder{
		kind: "Deployment", namespace: "default", name: "api", clusterUID: "uid-abc",
		rateWindow: 1 * time.Hour, bucket: 60 * time.Hour,
	}
	want := `max_over_time(sum(container_memory_working_set_bytes{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""})[60h])`
	got := b.buildMemory()
	if got != want {
		t.Errorf("bucketed memory mismatch:\n  want: %s\n  got:  %s", want, got)
	}
	if strings.Contains(got, ":") {
		t.Errorf("gauge query became a subquery, paying for inner evaluations it does not need: %s", got)
	}
}

// Short ranges keep the exact query they had. The bucket is an addition to the
// long end, not a rewrite of what already worked.
func TestPromBuilder_UnbucketedRangesAreUnchanged(t *testing.T) {
	b := promBuilder{
		kind: "Deployment", namespace: "default", name: "api", clusterUID: "uid-abc",
		rateWindow: 1 * time.Minute,
	}
	want := `sum(rate(container_cpu_usage_seconds_total{cluster_id="uid-abc",namespace="default",pod=~"api-[a-z0-9]+-[a-z0-9]+",pod_uid!=""}[1m]))`
	if got := b.buildCPU(); got != want {
		t.Errorf("unbucketed CPU changed:\n  want: %s\n  got:  %s", want, got)
	}
}

// A plan that keeps less history than the question asks for gets the widest
// range that EXISTS, and is told. Serving the requested window with its first
// half empty is the failure this prevents: the model reads absent data as idle
// and sizes from zeros.
func TestClampRangeToRetention(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		req       string
		retention time.Duration
		want      string
		adjusted  bool
	}{
		{"30d", 15 * day, "14d", true},  // Free: 15 days retained
		{"30d", 30 * day, "30d", false}, // Team: exactly enough
		{"7d", 15 * day, "7d", false},
		{"30d", 0, "30d", false},        // no cap — self-hosted
		{"24h", 15 * day, "24h", false}, // short ranges are never touched
		{"30d", 3 * day, "24h", true},   // a very short window still answers something
		{"garbage", 15 * day, "garbage", false},
	}
	for _, c := range cases {
		got, adjusted := ClampRangeToRetention(c.req, c.retention)
		if got != c.want || adjusted != c.adjusted {
			t.Errorf("Clamp(%s, %s) = (%s, %v), want (%s, %v)",
				c.req, c.retention, got, adjusted, c.want, c.adjusted)
		}
	}
}

// The isolation boundary. cluster_id does NOT provide it.
//
// Observed in vivo 2026-08-12: an operator connected an existing cluster to a
// SECOND org on the Free plan by installing the agent in another namespace. The
// new org had five minutes of data. Kobi answered a 14d node question with two
// weeks of history — the FIRST org's — because the same physical cluster carries
// one cluster_id under two tenant_ids, and these selectors keyed only on
// cluster_id. Worse than reading it: every query wraps the selector in sum(), so
// the other org's series were ADDED to the caller's own.
//
// Every selector this builder writes must carry the tenant. A new metric added
// without it reopens the leak silently, which is why this walks all of them
// rather than spot-checking one.
func TestPromBuilder_EverySelectorIsTenantScoped(t *testing.T) {
	b := promBuilder{
		kind: "Deployment", namespace: "default", name: "api",
		clusterUID: "uid-abc", tenantID: "org-1",
		rateWindow: 1 * time.Minute, pods: []string{"api-1"},
	}
	queries := map[string]string{
		"cpu":      b.buildCPU(),
		"memory":   b.buildMemory(),
		"net rx":   b.buildNetwork(MetricNetworkRX),
		"net tx":   b.buildNetwork(MetricNetworkTX),
		"requests": b.buildRequestsLimits("requests", "cpu"),
		"limits":   b.buildRequestsLimits("limits", "memory"),
	}
	nb := promBuilder{
		kind: "Node", name: "node-1", clusterUID: "uid-abc", tenantID: "org-1",
		rateWindow: 1 * time.Minute,
	}
	queries["node cpu"] = nb.buildCPU()
	queries["node memory"] = nb.buildMemory()
	queries["node net"] = nb.buildNetwork(MetricNetworkRX)
	queries["node capacity"] = nb.buildRequestsLimits("requests", "cpu")

	for label, q := range queries {
		if !strings.Contains(q, `tenant_id="org-1"`) {
			t.Errorf("%s reads across organisations — no tenant_id in:\n  %s", label, q)
		}
	}
}

// OSS carries no tenant_id label at all, so scoping there would match nothing
// and blank every chart. The sentinel is a NAME, never a stamped value.
func TestPromBuilder_UnscopedWhenThereIsNoTenantLabel(t *testing.T) {
	b := promBuilder{
		kind: "Deployment", namespace: "default", name: "api",
		clusterUID: "uid-abc", rateWindow: 1 * time.Minute,
	}
	if got := b.buildCPU(); strings.Contains(got, "tenant_id") {
		t.Errorf("single-tenant query filters on a label its series do not carry:\n  %s", got)
	}
}
