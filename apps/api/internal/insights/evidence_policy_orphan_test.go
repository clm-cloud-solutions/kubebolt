package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// policy-orphan: what was counted, and what could not be read — recorded as
// "not read", never as zero.
func TestPolicyOrphan_EvidenceSaysWhatWasNotRead(t *testing.T) {
	p := pod("shop", "web-0")
	p.Status.Phase = corev1.PodRunning
	got := namespaceWithoutNetworkPolicyRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "CiliumNetworkPolicies").Detail; d != "not read" {
		t.Errorf("cilium: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Running pods").Detail; d != "1" {
		t.Errorf("pods: %q", d)
	}

	known := &ClusterState{Pods: []*corev1.Pod{p}, CiliumPoliciesKnown: true, CiliumClusterwidePolicies: 2}
	got = namespaceWithoutNetworkPolicyRule().Evaluate(known)
	if d := evidenceByLabel(t, got[0], "Cluster-wide Cilium policies").Detail; d != "2" {
		t.Errorf("clusterwide: %q", d)
	}
}
