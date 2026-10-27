package insights

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// evicted-pods seeds node_pressure — but only an eviction the node forced:
// the kubelet's message is what tells the pod's own limit from node
// pressure, and the transition is when it happened.
func TestEvictedPods_EvidenceCarriesTheKubeletsMessageAndTime(t *testing.T) {
	at := time.Now().Add(-3 * time.Minute)
	p := pod("lab", "report-gen-x")
	p.Status.Phase = corev1.PodFailed
	p.Status.Reason = "Evicted"
	p.Status.Message = "The node was low on resource: memory. Threshold quantity: 100Mi, available: 80Mi. "
	p.Status.Conditions = []corev1.PodCondition{{LastTransitionTime: metav1.NewTime(at)}}
	got := evictedPodsRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	e := evidenceByLabel(t, got[0], "Eviction")
	if e.Detail != p.Status.Message || e.At == nil || !e.At.Equal(at) {
		t.Errorf("eviction: %+v", e)
	}
}
