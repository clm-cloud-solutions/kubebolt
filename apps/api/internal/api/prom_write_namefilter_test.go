package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/golang/snappy"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// appendUnknownVarintField tacks a varint-typed top-level field onto a
// WriteRequest so tests can assert the filter passes unknown/future fields
// through verbatim.
func appendUnknownVarintField(body []byte, field int, val uint64) []byte {
	body = protowire.AppendTag(body, protowire.Number(field), protowire.VarintType)
	body = protowire.AppendVarint(body, val)
	return body
}

func TestIsCoreMetricName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Core families (kept).
		{"kube_pod_info", true},
		{"kube_pod_status_phase", true},
		{"node_load1", true},
		{"node_cpu_usage_seconds_total", true},
		{"container_cpu_usage_seconds_total", true},
		{"kubelet_volume_stats_used_bytes", true},
		{"pod_flow_events_total", true},
		{"pod_dns_resolutions_total", true},
		{"hubble_collector_up", true},
		{"kubebolt_agent_samples_collected_total", true},
		{"process_resident_memory_bytes", true},
		{"up", true}, // exact
		// Custom (dropped) — the customer's own app metrics.
		{"gitlab_sql_duration_seconds_bucket", false},
		{"sidekiq_jobs_processed_total", false},
		{"grpc_server_handled_total", false},
		{"go_gc_duration_seconds", false},
		{"controller_runtime_reconcile_total", false},
		{"", false},
		// Near-misses must NOT match a core prefix by accident.
		{"upstream_latency", false}, // "up" is exact, not a prefix
		{"kubernetes_build_info", false},
	}
	for _, c := range cases {
		if got := isCoreMetricName(c.name); got != c.want {
			t.Errorf("isCoreMetricName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// mixedSeries builds a WriteRequest with 3 core (4 samples) + 2 custom
// (8 samples) TimeSeries. Core: kube_pod_info(1) + node_load1(2) + up(1).
// Custom: gitlab(3) + sidekiq(5).
func mixedSeries() []byte {
	return buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "kube_pod_info"}, {"namespace", "default"}}, Samples: 1},
		{Labels: [][2]string{{"__name__", "gitlab_sql_duration_seconds_bucket"}, {"le", "0.5"}}, Samples: 3},
		{Labels: [][2]string{{"__name__", "node_load1"}}, Samples: 2},
		{Labels: [][2]string{{"__name__", "sidekiq_jobs_total"}}, Samples: 5},
		{Labels: [][2]string{{"__name__", "up"}, {"job", "kubelet"}}, Samples: 1},
	})
}

func TestFilterNonCoreSeries_DropsCustomKeepsCore(t *testing.T) {
	filtered, droppedSeries, droppedSamples, err := filterNonCoreSeries(mixedSeries())
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if droppedSeries != 2 {
		t.Errorf("droppedSeries = %d, want 2", droppedSeries)
	}
	if droppedSamples != 8 {
		t.Errorf("droppedSamples = %d, want 8 (gitlab 3 + sidekiq 5)", droppedSamples)
	}
	// The filtered payload must carry exactly the 4 core samples.
	kept, err := countSamplesInWriteRequest(filtered)
	if err != nil {
		t.Fatalf("count filtered: %v", err)
	}
	if kept != 4 {
		t.Errorf("kept samples = %d, want 4 (kube 1 + node 2 + up 1)", kept)
	}
}

func TestFilterNonCoreSeries_AllCore_NoDrop(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "kube_pod_info"}}, Samples: 2},
		{Labels: [][2]string{{"__name__", "container_cpu_usage_seconds_total"}}, Samples: 4},
	})
	filtered, ds, dsamp, err := filterNonCoreSeries(body)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if ds != 0 || dsamp != 0 {
		t.Errorf("all-core dropped series=%d samples=%d, want 0/0", ds, dsamp)
	}
	kept, _ := countSamplesInWriteRequest(filtered)
	if kept != 6 {
		t.Errorf("kept samples = %d, want 6", kept)
	}
}

func TestFilterNonCoreSeries_AllCustom_DropsEverything(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "gitlab_x"}}, Samples: 3},
		{Labels: [][2]string{{"__name__", "sidekiq_y"}}, Samples: 2},
	})
	filtered, ds, dsamp, err := filterNonCoreSeries(body)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if ds != 2 || dsamp != 5 {
		t.Errorf("all-custom dropped series=%d samples=%d, want 2/5", ds, dsamp)
	}
	kept, _ := countSamplesInWriteRequest(filtered)
	if kept != 0 {
		t.Errorf("kept samples = %d, want 0", kept)
	}
}

// TestFilterNonCoreSeries_PreservesUnknownTopLevelFields ensures a
// WriteRequest.metadata (or any future top-level field) survives the filter
// verbatim — we only touch field-1 TimeSeries.
func TestFilterNonCoreSeries_PreservesUnknownTopLevelFields(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "node_load1"}}, Samples: 1},
	})
	// Append an unknown varint top-level field (field 99).
	body = appendUnknownVarintField(body, 99, 7)
	filtered, ds, _, err := filterNonCoreSeries(body)
	if err != nil {
		t.Fatalf("filter with unknown field: %v", err)
	}
	if ds != 0 {
		t.Errorf("droppedSeries = %d, want 0", ds)
	}
	kept, err := countSamplesInWriteRequest(filtered)
	if err != nil {
		t.Fatalf("count: %v — unknown field likely corrupted the payload", err)
	}
	if kept != 1 {
		t.Errorf("kept samples = %d, want 1", kept)
	}
}

func TestInspectTimeSeries(t *testing.T) {
	ts := buildTimeSeriesWithLabels([][2]string{{"__name__", "kube_pod_info"}, {"namespace", "kube-system"}}, 3)
	name, samples := inspectTimeSeries(ts)
	if name != "kube_pod_info" {
		t.Errorf("name = %q, want kube_pod_info", name)
	}
	if samples != 3 {
		t.Errorf("samples = %d, want 3", samples)
	}

	// No __name__ label → empty name (never core).
	tsNoName := buildTimeSeriesWithLabels([][2]string{{"job", "node"}}, 2)
	name, samples = inspectTimeSeries(tsNoName)
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
	if samples != 2 {
		t.Errorf("samples = %d, want 2", samples)
	}
}

func TestPromNameFilter_Policy(t *testing.T) {
	allow := true
	deny := false
	newFilter := func(enabled bool, defaultAllow bool) *PromNameFilter {
		defaults := auth.EffectiveLimits{AllowCustomSeries: defaultAllow}
		return NewPromNameFilter(enabled, defaults, NewPromWriteMetrics(prometheus.NewRegistry()))
	}

	t.Run("disabled is a no-op", func(t *testing.T) {
		f := newFilter(false, false)
		out, ds, dsamp, rewrote, err := f.Filter("t1", nil, mixedSeries())
		if err != nil {
			t.Fatal(err)
		}
		if rewrote || ds != 0 || dsamp != 0 {
			t.Errorf("disabled should not filter: rewrote=%v ds=%d dsamp=%d", rewrote, ds, dsamp)
		}
		if kept, _ := countSamplesInWriteRequest(out); kept != 12 {
			t.Errorf("disabled kept=%d, want 12 (all)", kept)
		}
	})

	t.Run("core-only default drops custom", func(t *testing.T) {
		f := newFilter(true, false) // default AllowCustomSeries=false
		_, ds, dsamp, rewrote, err := f.Filter("t1", nil, mixedSeries())
		if err != nil {
			t.Fatal(err)
		}
		if !rewrote || ds != 2 || dsamp != 8 {
			t.Errorf("core-only should drop custom: rewrote=%v ds=%d dsamp=%d, want true/2/8", rewrote, ds, dsamp)
		}
	})

	t.Run("tenant opt-in keeps custom", func(t *testing.T) {
		f := newFilter(true, false)
		out, ds, dsamp, rewrote, err := f.Filter("t1", &auth.TenantLimits{AllowCustomSeries: &allow}, mixedSeries())
		if err != nil {
			t.Fatal(err)
		}
		if rewrote || ds != 0 || dsamp != 0 {
			t.Errorf("opt-in should keep custom: rewrote=%v ds=%d dsamp=%d", rewrote, ds, dsamp)
		}
		if kept, _ := countSamplesInWriteRequest(out); kept != 12 {
			t.Errorf("opt-in kept=%d, want 12 (all)", kept)
		}
	})

	t.Run("tenant opt-out overrides allow default", func(t *testing.T) {
		f := newFilter(true, true) // default allows, tenant denies
		_, ds, _, rewrote, err := f.Filter("t1", &auth.TenantLimits{AllowCustomSeries: &deny}, mixedSeries())
		if err != nil {
			t.Fatal(err)
		}
		if !rewrote || ds != 2 {
			t.Errorf("opt-out should drop custom: rewrote=%v ds=%d, want true/2", rewrote, ds)
		}
	})

	t.Run("nil filter is a no-op", func(t *testing.T) {
		var f *PromNameFilter
		out, ds, _, rewrote, err := f.Filter("t1", nil, mixedSeries())
		if err != nil || rewrote || ds != 0 {
			t.Errorf("nil filter: err=%v rewrote=%v ds=%d", err, rewrote, ds)
		}
		if kept, _ := countSamplesInWriteRequest(out); kept != 12 {
			t.Errorf("nil filter kept=%d, want 12", kept)
		}
	})
}

func TestDropReservedSeries(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "kube_pod_info"}, {"namespace", "kubebolt"}}, Samples: 1},
		{Labels: [][2]string{{"__name__", "kubebolt_http_requests_total"}, {"code", "5xx"}}, Samples: 4},
		{Labels: [][2]string{{"__name__", "kubebolt_agent_heap_alloc_bytes"}}, Samples: 2},
		{Labels: [][2]string{{"__name__", "kubebolt_kobi_copilot_cost_usd_total"}}, Samples: 3},
	})
	out, series, samples, rewrote, err := dropReservedSeries(body)
	if err != nil {
		t.Fatal(err)
	}
	if !rewrote || series != 2 || samples != 7 {
		t.Fatalf("dropped %d series / %d samples (rewrote=%v), want 2 / 7", series, samples, rewrote)
	}
	if kept, _ := countSamplesInWriteRequest(out); kept != 3 {
		t.Errorf("kept samples = %d, want 3 (kube_pod_info + the agent's heap)", kept)
	}

	clean := mixedSeries()
	out, series, _, rewrote, err = dropReservedSeries(clean)
	if err != nil || rewrote || series != 0 || len(out) != len(clean) {
		t.Fatalf("a payload without reserved names changed: rewrote=%v series=%d err=%v", rewrote, series, err)
	}
}

// Through the real handler, on a paid-plan-or-free tenant alike: the reserved
// series never reach VictoriaMetrics and the drop is counted.
func TestE2E_PromRemoteWrite_DropsReservedNames(t *testing.T) {
	roomy := auth.EffectiveLimits{WriteSamplesPerSec: 100_000, WriteBurstSamples: 1_000_000, MaxActiveSeries: 10_000_000}
	h, plaintext, tenantID, upstream, reg := newE2EHandler(t, promWriteAuthEnforced, roomy)
	body := snappy.Encode(nil, buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "node_cpu_usage_seconds_total"}, {"tenant_id", tenantID}}, Samples: 2},
		{Labels: [][2]string{{"__name__", "kubebolt_http_requests_total"}, {"tenant_id", tenantID}, {"code", "5xx"}}, Samples: 5},
	}))
	req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	h.handlePromWrite(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	upstream.mu.Lock()
	forwarded, derr := snappy.Decode(nil, upstream.lastBody)
	upstream.mu.Unlock()
	if derr != nil {
		t.Fatal(derr)
	}
	if bytes.Contains(forwarded, []byte("kubebolt_http_requests_total")) {
		t.Error("the reserved series reached VictoriaMetrics")
	}
	if !bytes.Contains(forwarded, []byte("node_cpu_usage_seconds_total")) {
		t.Error("the customer's series was lost")
	}
	if v := counterByLabels(t, reg, "kubebolt_prom_write_dropped_series_total", map[string]string{"tenant_id": tenantID, "reason": "reserved"}); v != 1 {
		t.Errorf("dropped_series_total{reason=reserved} = %v, want 1", v)
	}
}
