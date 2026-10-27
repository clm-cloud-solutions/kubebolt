package insights

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// readiness-probe-failing: when the pod went not-Ready, and the probe of the
// container that is not ready, with its timings.
func TestReadinessProbeFailing_EvidenceCarriesProbeAndTransition(t *testing.T) {
	since := time.Now().Add(-5 * time.Minute)
	p := pod("lab", "never-ready-0")
	p.Status.Phase = corev1.PodRunning
	p.Spec.Containers = []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "exit 1"}}}}}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: false,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse,
		Reason: "ContainersNotReady", Message: "containers with unready status: [app]", LastTransitionTime: metav1.NewTime(since)}}
	got := readinessProbeFailingRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if e := evidenceByLabel(t, got[0], "Ready"); !strings.Contains(e.Detail, "5m before this was flagged") || e.At == nil {
		t.Errorf("ready: %+v", e)
	}
	if d := evidenceByLabel(t, got[0], "Readiness probe").Detail; d != "app: exec sh -c exit 1; initialDelay 0s, period 10s, timeout 1s, failureThreshold 3" {
		t.Errorf("probe: %q", d)
	}
}

// Between the deaths of a crash-looping container it is briefly Running and
// not Ready under a Ready condition False for minutes. Without a readiness
// probe, or with a run that just started, that is a start, not a readiness
// failure — the rule said "check the readiness probe" of an OOMKilled
// container that had none (lab, 2026-10-11).
func TestReadinessProbeFailing_NotForAContainerThatIsStarting(t *testing.T) {
	longAgo := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	mk := func(probe *corev1.Probe, started time.Time) *corev1.Pod {
		p := pod("lab", "ev-oom-0")
		p.Status.Phase = corev1.PodRunning
		p.Spec.Containers = []corev1.Container{{Name: "app", ReadinessProbe: probe}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: false, RestartCount: 8,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}}}}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse,
			Reason: "ContainersNotReady", LastTransitionTime: longAgo}}
		return p
	}
	probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"true"}}}}
	for name, p := range map[string]*corev1.Pod{
		"no readiness probe":  mk(nil, time.Now().Add(-5*time.Minute)),
		"run started 5 s ago": mk(probe, time.Now().Add(-5*time.Second)),
	} {
		if got := readinessProbeFailingRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}}); len(got) != 0 {
			t.Errorf("%s: fired %+v", name, got)
		}
	}
	if got := readinessProbeFailingRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{mk(probe, time.Now().Add(-5*time.Minute))}}); len(got) != 1 {
		t.Errorf("a probe failing on a run of 5 min should fire, got %d", len(got))
	}
}
