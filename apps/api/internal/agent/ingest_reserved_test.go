package agent

import (
	"testing"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

func TestDropReserved(t *testing.T) {
	in := []*agentv2.Sample{
		{MetricName: "container_cpu_usage_seconds_total"},
		{MetricName: "kubebolt_agent_heap_alloc_bytes"}, // the agent's own: kept
		{MetricName: "kubebolt_kobi_copilot_cost_usd_total"},
		{MetricName: "kubebolt_build_info"},
	}
	kept, dropped := dropReserved(in)
	if dropped != 2 || len(kept) != 2 {
		t.Fatalf("dropped %d, kept %d; want 2 and 2", dropped, len(kept))
	}
	for _, s := range kept {
		if s.GetMetricName() == "kubebolt_kobi_copilot_cost_usd_total" || s.GetMetricName() == "kubebolt_build_info" {
			t.Errorf("reserved %q survived", s.GetMetricName())
		}
	}
}
