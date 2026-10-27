package insights

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// node-not-ready seeds node_rotation. Which kind of not-ready it is — a new
// node that never came up, an old one that stopped reporting while cordoned
// for a drain, one under DiskPressure — is the fact the burst classifier and
// the operator both need, and none of it survived "is not in Ready state".
func TestNodeNotReady_EvidenceCarriesTheDecidingFacts(t *testing.T) {
	flipped := time.Now().Add(-12 * time.Minute)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "aks-pool-1", CreationTimestamp: metav1.NewTime(time.Now().Add(-40 * 24 * time.Hour))},
		Spec:       corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, Reason: "NodeStatusUnknown",
				Message: "Kubelet stopped posting node status.", LastTransitionTime: metav1.NewTime(flipped)},
			{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
			{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
		}},
	}
	got := nodeNotReadyRule().Evaluate(&ClusterState{Nodes: []*corev1.Node{node}})
	if len(got) != 1 || got[0].EvidenceCount != len(got[0].Evidence) {
		t.Fatalf("%+v", got)
	}
	ready := evidenceByLabel(t, got[0], "Ready")
	if !strings.HasPrefix(ready.Detail, "Unknown (NodeStatusUnknown): Kubelet stopped posting") ||
		!strings.Contains(ready.Detail, "12m before this was flagged") || ready.At == nil || !ready.At.Equal(flipped) {
		t.Errorf("Ready: %+v", ready)
	}
	if d := evidenceByLabel(t, got[0], "Other conditions true").Detail; d != "DiskPressure" {
		t.Errorf("other conditions: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Cordoned").Detail; !strings.HasPrefix(d, "yes") {
		t.Errorf("cordoned: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Node created").Detail; d != "40d before this was flagged" {
		t.Errorf("node created: %q", d)
	}
}
