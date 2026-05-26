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

func TestConvert_HappyPath(t *testing.T) {
	resp := newQueryResp(
		map[string]string{"__name__": "up", "instance": "a"},
		[][]interface{}{
			{float64(1700000000), "1"},
			{float64(1700000030), "0"},
		},
	)
	samples, err := Convert(resp, "cluster-1", "yagan-prod", "tenant-x")
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
	samples, _ := Convert(resp, "", "", "")
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
	samples, err := Convert(resp, "", "", "")
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
	samples, err := Convert(resp, "", "", "")
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
	samples, err := Convert(resp, "", "", "")
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
	if _, err := Convert(resp, "", "", ""); err == nil {
		t.Fatal("expected error for non-matrix result")
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
	samples, _ := Convert(resp, "c1", "n1", "t1")
	samples[0].Labels["instance"] = "MUTATED"
	if samples[1].Labels["instance"] != "a" {
		t.Errorf("Labels aliased across samples: sample[1].instance = %q", samples[1].Labels["instance"])
	}
}
