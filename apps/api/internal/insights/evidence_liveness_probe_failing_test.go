package insights

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// liveness-probe-failing: the probe's configuration — the timings are what an
// aggressive probe gets wrong and are recorded nowhere else — and the
// failures with their last occurrence.
func TestLivenessProbeFailing_EvidenceCarriesProbeAndFailures(t *testing.T) {
	last := time.Now()
	p := pod("lab", "flappy-0")
	p.Spec.Containers = []corev1.Container{{Name: "app", LivenessProbe: &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt(8080)}},
		InitialDelaySeconds: 2, PeriodSeconds: 5, TimeoutSeconds: 1, FailureThreshold: 2}}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(last.Add(-10 * time.Minute))}}}}
	ev := &corev1.Event{Reason: "Unhealthy", Message: "Liveness probe failed: HTTP probe failed with statuscode: 500", Count: 4,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "lab", Name: "flappy-0", FieldPath: "spec.containers{app}"},
		LastTimestamp:  metav1.NewTime(last)}
	got := livenessProbeFailingRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}, Events: []*corev1.Event{ev}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Liveness probe").Detail; d != "app: httpGet /healthz on port 8080; initialDelay 2s, period 5s, timeout 1s, failureThreshold 2" {
		t.Errorf("probe: %q", d)
	}
	if e := evidenceByLabel(t, got[0], "Failures"); e.Detail != "4: Liveness probe failed: HTTP probe failed with statuscode: 500" || e.At == nil {
		t.Errorf("failures: %+v", e)
	}
}
