package insights

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// zero-replicas: desired against observed, and the controller's conditions —
// ReplicaFailure (a quota refusing the pods) is told apart from a deliberate
// scale only here.
func TestZeroReplicas_EvidenceCarriesReplicaFailure(t *testing.T) {
	d := &appsv1.Deployment{}
	d.Namespace, d.Name = "shop", "api"
	d.Spec.Replicas = i32(3)
	d.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable", Message: "Deployment does not have minimum availability."},
		{Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate", Message: `pods "api-7c5-x" is forbidden: exceeded quota: compute`},
	}
	got := zeroReplicasRule().Evaluate(&ClusterState{Deployments: []*appsv1.Deployment{d}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if r := evidenceByLabel(t, got[0], "Replicas").Detail; !strings.HasPrefix(r, "desired 3, 0 created") {
		t.Errorf("replicas: %q", r)
	}
	if f := evidenceByLabel(t, got[0], "ReplicaFailure").Detail; !strings.Contains(f, "exceeded quota") {
		t.Errorf("replica failure: %q", f)
	}
	// ReplicaFailure first: it is the one that names a cause.
	if got[0].Evidence[1].Label != "ReplicaFailure" {
		t.Errorf("order: %+v", got[0].Evidence)
	}
}
