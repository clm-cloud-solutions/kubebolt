package insights

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// crash-loop: how the last run ended and when, the restarts against the bar,
// and the image. Exit 0 under CrashLoopBackOff (a process that finishes, run
// by a controller that restarts it) and exit 1 after a new tag are different
// fixes, and the prose said neither.
func TestCrashLoop_EvidenceCarriesTheDecidingFacts(t *testing.T) {
	ended := time.Now().Add(-90 * time.Second)
	p := pod("shop", "api-0")
	p.Spec.Containers = []corev1.Container{{Name: "api", Image: "shop/api:2.4.1"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "api", RestartCount: 7,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Completed", ExitCode: 0, FinishedAt: metav1.NewTime(ended),
		}},
	}}
	got := crashLoopRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	last := evidenceByLabel(t, got[0], "Last termination")
	if last.Detail != "Completed (exit 0)" || last.At == nil || !last.At.Equal(ended) {
		t.Errorf("last termination: %+v", last)
	}
	if d := evidenceByLabel(t, got[0], "Restarts").Detail; d != "7 (fires above 3)" {
		t.Errorf("restarts: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Image").Detail; d != "shop/api:2.4.1" {
		t.Errorf("image: %q", d)
	}
}

// A container its liveness probe keeps killing reads "Completed (exit 0)" —
// a graceful server exits 0 on SIGTERM — and the liveness rule hands that
// state to crash-loop. So crash-loop names the kubelet as the cause and
// carries the probe and its failures (lab, 2026-10-11).
func TestCrashLoop_NamesTheLivenessKill(t *testing.T) {
	ended := time.Now().Add(-90 * time.Second)
	p := pod("lab", "ev-live-0")
	p.Spec.Containers = []corev1.Container{{Name: "web", Image: "nginx:1.27-alpine", LivenessProbe: &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt(9999)}}}}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "web", RestartCount: 7,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Completed", ExitCode: 0, FinishedAt: metav1.NewTime(ended)}},
	}}
	kill := &corev1.Event{Reason: "Unhealthy", Count: 16,
		Message:        `Liveness probe failed: Get "http://10.0.0.1:9999/healthz": dial tcp 10.0.0.1:9999: connect: connection refused`,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "lab", Name: "ev-live-0", FieldPath: "spec.containers{web}"},
		LastTimestamp:  metav1.NewTime(ended)}
	got := crashLoopRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}, Events: []*corev1.Event{kill}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got[0].Message, "liveness probe fails") || !strings.Contains(got[0].Suggestion, "kubelet kills it") {
		t.Errorf("cause not named: %q / %q", got[0].Message, got[0].Suggestion)
	}
	evidenceByLabel(t, got[0], "Liveness probe")
	if e := evidenceByLabel(t, got[0], "Failures"); !strings.HasPrefix(e.Detail, "16: Liveness probe failed") {
		t.Errorf("failures: %+v", e)
	}
}

// A loop of OOM kills is a memory problem; the command/args advice pointed
// the wrong way.
func TestCrashLoop_NamesTheOOMLoop(t *testing.T) {
	p := pod("lab", "ev-oom-0")
	p.Spec.Containers = []corev1.Container{{Name: "app", Image: "python:3.12-alpine"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", RestartCount: 9,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(time.Now())}},
	}}
	got := crashLoopRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 || !strings.HasSuffix(got[0].Message, "each run is OOMKilled") || strings.Contains(got[0].Suggestion, "command/args") {
		t.Errorf("%+v", got)
	}
}
