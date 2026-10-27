package insights

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// oom-killed: the memory limit and when it died. It decides patch against
// rollback (a deploy that cut the limit is reverted; an app that grew is
// patched), and its absence decides that the node ran out, not the limit.
func TestOOMKilled_EvidenceCarriesLimitAndTime(t *testing.T) {
	died := time.Now().Add(-2 * time.Minute)
	p := pod("lab", "oomy-0")
	p.Spec.Containers = []corev1.Container{{Name: "app",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}}}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app",
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(died),
		}},
	}}
	got := oomKilledRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	last := evidenceByLabel(t, got[0], "Last termination")
	if last.Detail != "OOMKilled (exit 137)" || last.At == nil || !last.At.Equal(died) {
		t.Errorf("last termination: %+v", last)
	}
	if d := evidenceByLabel(t, got[0], "Memory limit").Detail; d != "128Mi" {
		t.Errorf("memory limit: %q", d)
	}
}
