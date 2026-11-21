package agent

import (
	"testing"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// Finding #101: what the agent read from OpenCost and nothing reads is dropped
// for every tenant; the cost families and OpenCost's kube_* stay, and the same
// names arriving by another path are the customer's, never touched.
func TestDropOpenCostUnread(t *testing.T) {
	oc := map[string]string{"source": "opencost"}
	batch := []*agentv2.Sample{
		sample("node_total_hourly_cost", oc),
		sample("pod_pvc_allocation", oc),
		sample("kube_node_status_allocatable", oc),
		sample("deployment_match_labels", oc),
		sample("kubecost_http_requests_total", oc),
		sample("container_gpu_allocation", oc),
		sample("pod_info", map[string]string{"source": "postgres"}), // another exporter
		sample("deployment_match_labels", nil),                      // not from OpenCost
		sample("node_load1", nil),
	}
	kept, dropped := dropOpenCostUnread(batch)
	if dropped != 3 {
		t.Errorf("dropped = %d, want 3 (deployment_match_labels, kubecost_http_*, the GPU family)", dropped)
	}
	want := []string{"node_total_hourly_cost", "pod_pvc_allocation", "kube_node_status_allocatable", "pod_info", "deployment_match_labels", "node_load1"}
	if len(kept) != len(want) {
		t.Fatalf("kept %d samples, want %d", len(kept), len(want))
	}
	for i, s := range kept {
		if s.GetMetricName() != want[i] {
			t.Errorf("kept[%d] = %s, want %s", i, s.GetMetricName(), want[i])
		}
	}
}
