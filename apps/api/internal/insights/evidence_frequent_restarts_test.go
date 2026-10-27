package insights

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// frequent-restarts: the same facts as crash-loop — how the last run ended
// and when, the count against the bar, the image.
func TestFrequentRestarts_EvidenceCarriesTheLastRun(t *testing.T) {
	ended := time.Now().Add(-time.Minute)
	p := pod("shop", "api-0")
	p.Spec.Containers = []corev1.Container{{Name: "api", Image: "shop/api:2.4.1"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "api", RestartCount: 8,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(ended)}},
	}}
	got := frequentRestartsRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if e := evidenceByLabel(t, got[0], "Last termination"); e.Detail != "Error (exit 1)" || e.At == nil {
		t.Errorf("last termination: %+v", e)
	}
	if d := evidenceByLabel(t, got[0], "Restarts").Detail; d != "8 (fires above 5)" {
		t.Errorf("restarts: %q", d)
	}
}
