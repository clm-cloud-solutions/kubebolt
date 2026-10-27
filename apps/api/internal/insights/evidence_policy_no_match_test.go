package insights

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// policy-no-match: the selector, the policy types, and the absence of any
// matching workload template.
func TestPolicyNoMatch_EvidenceCarriesSelectorTypesAndAbsence(t *testing.T) {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "np", Name: "allow-web"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}
	got := networkPolicyNoMatchRule().Evaluate(&ClusterState{NetworkPolicies: []*networkingv1.NetworkPolicy{np}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Pod selector").Detail; d != "app=web" {
		t.Errorf("selector: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Policy types").Detail; d != "Egress, Ingress" {
		t.Errorf("types: %q", d)
	}
	evidenceByLabel(t, got[0], "Matching workloads")
}
