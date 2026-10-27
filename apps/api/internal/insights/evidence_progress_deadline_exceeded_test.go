package insights

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

// progress-deadline-exceeded: the condition with when it was set, the
// replicas by revision, and the revision with the deadline it was measured
// against.
func TestProgressDeadline_EvidenceCarriesRevisionDeadlineAndReplicas(t *testing.T) {
	d := stalledDeployment("4", 3, 1, 4)
	secs := int32(300)
	d.Spec.ProgressDeadlineSeconds = &secs
	got := progressDeadlineExceededRule().Evaluate(&ClusterState{Deployments: []*appsv1.Deployment{d}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if e := evidenceByLabel(t, got[0], "Progressing"); !strings.Contains(e.Detail, "has timed out progressing") {
		t.Errorf("progressing: %+v", e)
	}
	if v := evidenceByLabel(t, got[0], "Revision and deadline").Detail; v != "revision 4, progress deadline 300s" {
		t.Errorf("revision: %q", v)
	}
	if r := evidenceByLabel(t, got[0], "Replicas").Detail; r != "3 desired, 1 on the current revision 4, 4 in total, 0 available" {
		t.Errorf("replicas: %q", r)
	}
}
