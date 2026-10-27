package insights

import (
	"testing"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// pdb-no-match: the selector, the budget, and the absence that makes it a
// likely typo — no workload's pod template matches.
func TestPDBNoMatch_EvidenceCarriesSelectorBudgetAndAbsence(t *testing.T) {
	one := intstr.FromInt(1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: "api-pdb"},
		Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "api", "app": "shop"}}},
	}
	got := pdbNoMatchRule().Evaluate(&ClusterState{PDBs: []*policyv1.PodDisruptionBudget{pdb}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Selector").Detail; d != "app=shop, tier=api" {
		t.Errorf("selector: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Budget").Detail; d != "minAvailable 1" {
		t.Errorf("budget: %q", d)
	}
	evidenceByLabel(t, got[0], "Matching workloads")
}
