package costmetrics

import "testing"

func TestIsCostFamily(t *testing.T) {
	for _, name := range Families {
		if !IsCostFamily(name) {
			t.Errorf("%s is in Families but IsCostFamily says no", name)
		}
	}
	for _, name := range []string{
		"kubecost_http_requests_total", // OpenCost's own server metric
		"kubecost_network_",            // never a prefix
		"node_gpu_hourly_cost",         // read by nothing yet
		"container_gpu_allocation",
		"deployment_match_labels",
		"pod_info",
		"",
	} {
		if IsCostFamily(name) {
			t.Errorf("IsCostFamily(%q) = true, want false", name)
		}
	}
}

// What OpenCost publishes and the product never reads is dropped; the cost
// families and the KSM-compatible kube_* families stay.
func TestKeepFromOpenCost(t *testing.T) {
	keep := []string{"node_total_hourly_cost", "pod_pvc_allocation", "kubecost_node_is_spot", "kube_node_status_allocatable", "kube_pod_container_resource_requests"}
	drop := []string{"deployment_match_labels", "replicaset_match_labels", "pod_info", "service_selector_labels", "statefulSet_match_labels", "kubecost_http_requests_total", "kubecost_cluster_info", "container_gpu_allocation", "node_gpu_hourly_cost"}
	for _, n := range keep {
		if !KeepFromOpenCost(n) {
			t.Errorf("KeepFromOpenCost(%q) = false, want true", n)
		}
	}
	for _, n := range drop {
		if KeepFromOpenCost(n) {
			t.Errorf("KeepFromOpenCost(%q) = true, want false", n)
		}
	}
}

func TestFamiliesHaveNoDuplicates(t *testing.T) {
	if len(families) != len(Families) {
		t.Fatalf("Families has %d entries but %d distinct names", len(Families), len(families))
	}
}
