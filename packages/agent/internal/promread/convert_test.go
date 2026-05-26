package promread

import (
	"testing"
)

func newQueryResp(seriesMetric map[string]string, values [][]interface{}) *QueryRangeResponse {
	return &QueryRangeResponse{
		Status: "success",
		Data: QueryRangeData{
			ResultType: "matrix",
			Result: []QueryRangeResult{
				{Metric: seriesMetric, Values: values},
			},
		},
	}
}

// fakeNodeIndex is a stand-in for K8sNodeIndex in convert tests —
// avoids spinning up a fake clientset just to stub IP→name lookups.
type fakeNodeIndex map[string]string

func (f fakeNodeIndex) NodeByIP(ip string) string { return f[ip] }

func TestConvert_HappyPath(t *testing.T) {
	resp := newQueryResp(
		map[string]string{"__name__": "up", "instance": "a"},
		[][]interface{}{
			{float64(1700000000), "1"},
			{float64(1700000030), "0"},
		},
	)
	samples, err := Convert(resp, "cluster-1", "yagan-prod", "tenant-x", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}
	s := samples[0]
	if s.MetricName != "up" {
		t.Errorf("MetricName: got %q want up", s.MetricName)
	}
	if s.Labels["instance"] != "a" {
		t.Errorf("instance label missing: %+v", s.Labels)
	}
	if s.Labels["cluster_id"] != "cluster-1" || s.Labels["cluster_name"] != "yagan-prod" || s.Labels["tenant_id"] != "tenant-x" {
		t.Errorf("stamp labels missing: %+v", s.Labels)
	}
	if _, ok := s.Labels["__name__"]; ok {
		t.Errorf("__name__ should be moved to MetricName, not stay in Labels")
	}
	if s.Value != 1 || samples[1].Value != 0 {
		t.Errorf("values wrong: %v / %v", s.Value, samples[1].Value)
	}
}

func TestConvert_OmitsEmptyStampLabels(t *testing.T) {
	resp := newQueryResp(
		map[string]string{"__name__": "up"},
		[][]interface{}{{float64(1700000000), "1"}},
	)
	samples, _ := Convert(resp, "", "", "", nil)
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if _, has := samples[0].Labels["cluster_id"]; has {
		t.Error("cluster_id should be omitted when empty")
	}
	if _, has := samples[0].Labels["tenant_id"]; has {
		t.Error("tenant_id should be omitted when empty")
	}
}

func TestConvert_SkipsSeriesWithoutName(t *testing.T) {
	resp := newQueryResp(
		map[string]string{"instance": "a"}, // no __name__
		[][]interface{}{{float64(1700000000), "1"}},
	)
	samples, err := Convert(resp, "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("expected 0 samples (no __name__), got %d", len(samples))
	}
}

func TestConvert_SkipsUnparseableValues(t *testing.T) {
	resp := newQueryResp(
		map[string]string{"__name__": "up"},
		[][]interface{}{
			{float64(1700000000), "not-a-number"},
			{float64(1700000030), "1"},
		},
	)
	samples, err := Convert(resp, "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(samples) != 1 {
		t.Errorf("expected 1 sample (one skipped), got %d", len(samples))
	}
}

func TestConvert_AcceptsNonFiniteValues(t *testing.T) {
	// Prom emits "NaN" / "+Inf" / "-Inf" as legal value strings;
	// strconv.ParseFloat handles them, so they MUST round-trip.
	resp := newQueryResp(
		map[string]string{"__name__": "up"},
		[][]interface{}{
			{float64(1700000000), "NaN"},
			{float64(1700000030), "+Inf"},
			{float64(1700000060), "-Inf"},
		},
	)
	samples, err := Convert(resp, "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(samples) != 3 {
		t.Errorf("expected 3 samples (non-finite accepted), got %d", len(samples))
	}
}

func TestConvert_RejectsNonMatrixResponse(t *testing.T) {
	resp := &QueryRangeResponse{
		Status: "success",
		Data:   QueryRangeData{ResultType: "vector"},
	}
	if _, err := Convert(resp, "", "", "", nil); err == nil {
		t.Fatal("expected error for non-matrix result")
	}
}

func TestConvert_NodeEnrichmentStampsKubeNodeName(t *testing.T) {
	// node-exporter series come with instance=<host-network pod IP>:<port>.
	// Convert + NodeIndex must surface a `node=<k8s-node-name>` label so
	// the UI's Node Monitor panels (which filter by node="...") match.
	resp := newQueryResp(
		map[string]string{
			"__name__": "node_load1",
			"instance": "172.18.0.4:9100",
			"job":      "node-exporter",
		},
		[][]interface{}{{float64(1700000000), "0.7"}},
	)
	idx := fakeNodeIndex{"172.18.0.4": "worker-a"}
	samples, err := Convert(resp, "c1", "n1", "t1", idx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Labels["node"] != "worker-a" {
		t.Errorf("node label: got %q, want worker-a", samples[0].Labels["node"])
	}
	// Original labels still present (enrichment additive, not destructive).
	if samples[0].Labels["instance"] != "172.18.0.4:9100" {
		t.Errorf("instance should be preserved, got %q", samples[0].Labels["instance"])
	}
}

func TestConvert_NodeEnrichmentSkipsNonNodeMetrics(t *testing.T) {
	// kube_pod_* / container_* shouldn't get a `node` stamp even if
	// they happen to have an `instance` label — the prefix gate
	// must be exact.
	resp := newQueryResp(
		map[string]string{
			"__name__": "kube_pod_info",
			"instance": "172.18.0.4:9100",
		},
		[][]interface{}{{float64(1700000000), "1"}},
	)
	idx := fakeNodeIndex{"172.18.0.4": "worker-a"}
	samples, _ := Convert(resp, "", "", "", idx)
	if _, has := samples[0].Labels["node"]; has {
		t.Errorf("non-node_* metric should NOT get node stamp, labels=%+v", samples[0].Labels)
	}
}

func TestConvert_NodeEnrichmentLookupMissLeavesNoStamp(t *testing.T) {
	// An unknown IP must not produce a wrong stamp — the panel
	// showing nothing is better than the panel showing a value
	// attributed to the wrong node.
	resp := newQueryResp(
		map[string]string{
			"__name__": "node_load1",
			"instance": "10.99.99.99:9100",
		},
		[][]interface{}{{float64(1700000000), "0.5"}},
	)
	idx := fakeNodeIndex{"172.18.0.4": "worker-a"} // doesn't contain 10.99.99.99
	samples, _ := Convert(resp, "", "", "", idx)
	if _, has := samples[0].Labels["node"]; has {
		t.Errorf("unknown IP should NOT produce a node stamp, labels=%+v", samples[0].Labels)
	}
}

func TestConvert_NilNodeIndexSkipsEnrichment(t *testing.T) {
	// nil NodeIndex must not panic, just leave node_* without the
	// stamp — matches existing call sites that don't pass an index.
	resp := newQueryResp(
		map[string]string{"__name__": "node_load1", "instance": "172.18.0.4:9100"},
		[][]interface{}{{float64(1700000000), "0.5"}},
	)
	samples, err := Convert(resp, "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, has := samples[0].Labels["node"]; has {
		t.Error("nil NodeIndex must not stamp node")
	}
}

func TestConvert_LabelsAreNotAliased(t *testing.T) {
	// Mutating one sample's Labels must not affect another's. This
	// would break the agent's shipper which can reorder/buffer
	// samples concurrently.
	resp := newQueryResp(
		map[string]string{"__name__": "up", "instance": "a"},
		[][]interface{}{
			{float64(1700000000), "1"},
			{float64(1700000030), "0"},
		},
	)
	samples, _ := Convert(resp, "c1", "n1", "t1", nil)
	samples[0].Labels["instance"] = "MUTATED"
	if samples[1].Labels["instance"] != "a" {
		t.Errorf("Labels aliased across samples: sample[1].instance = %q", samples[1].Labels["instance"])
	}
}
