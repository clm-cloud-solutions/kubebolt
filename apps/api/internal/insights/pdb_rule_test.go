package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pdbWithSelector(ns, name string, matchLabels map[string]string) *policyv1.PodDisruptionBudget {
	var sel *metav1.LabelSelector
	if matchLabels != nil {
		sel = &metav1.LabelSelector{MatchLabels: matchLabels}
	}
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: sel},
	}
}

func TestPDBNoMatchRule(t *testing.T) {
	rule := pdbNoMatchRule()

	matchingPod := pod("prod", "api-0")
	matchingPod.Labels = map[string]string{"app": "api"}

	state := &ClusterState{
		Pods: []*corev1.Pod{matchingPod},
		PDBs: []*policyv1.PodDisruptionBudget{
			pdbWithSelector("prod", "api-pdb", map[string]string{"app": "api"}),     // matches → no insight
			pdbWithSelector("prod", "ghost-pdb", map[string]string{"app": "gone"}), // matches nothing → insight
			pdbWithSelector("prod", "catch-all", map[string]string{}),               // empty selector → skipped
			pdbWithSelector("prod", "nil-sel", nil),                                 // nil selector → skipped
		},
	}

	got := rule.Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 insight (ghost-pdb), got %d: %+v", len(got), got)
	}
	if got[0].Resource != "PodDisruptionBudget/prod/ghost-pdb" {
		t.Fatalf("wrong resource flagged: %s", got[0].Resource)
	}
	if got[0].Severity != "warning" {
		t.Fatalf("expected warning severity, got %s", got[0].Severity)
	}
}
