package insights

import (
	"testing"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// hpa-maxed-out: replicas against both bounds, the target, and the CPU
// against its target — which says whether more replicas would help.
func TestHPAMaxedOut_EvidenceCarriesBoundsAndCPU(t *testing.T) {
	h := &autoscalingv1.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec: autoscalingv1.HorizontalPodAutoscalerSpec{MinReplicas: i32(2), MaxReplicas: 5, TargetCPUUtilizationPercentage: i32(80),
			ScaleTargetRef: autoscalingv1.CrossVersionObjectReference{Kind: "Deployment", Name: "api"}},
		Status: autoscalingv1.HorizontalPodAutoscalerStatus{CurrentReplicas: 5, CurrentCPUUtilizationPercentage: i32(140)},
	}
	got := hpaMaxedOutRule().Evaluate(&ClusterState{HPAs: []*autoscalingv1.HorizontalPodAutoscaler{h}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Replicas").Detail; d != "5 current, min 2, max 5" {
		t.Errorf("replicas: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Scale target").Detail; d != "Deployment/api" {
		t.Errorf("target: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "CPU").Detail; d != "140% of requests, target 80%" {
		t.Errorf("cpu: %q", d)
	}
}
