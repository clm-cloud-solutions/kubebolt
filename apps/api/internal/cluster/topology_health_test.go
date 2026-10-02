package cluster

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The cluster map folds healthy replicas into one node and draws the rest on
// their own, so it needs to know which is which. The phase cannot say: a pod in
// CrashLoopBackOff reports phase Running.
func TestPodTopologyHealth(t *testing.T) {
	running := func(ready bool, waiting string) *corev1.Pod {
		cs := corev1.ContainerStatus{Ready: ready}
		if waiting != "" {
			cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: waiting}
		}
		return &corev1.Pod{Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{cs},
		}}
	}
	cases := []struct {
		name       string
		pod        *corev1.Pod
		wantReady  string
		wantReason string
	}{
		{"running and ready", running(true, ""), "true", ""},
		{"crash loop is still phase Running", running(false, "CrashLoopBackOff"), "false", "CrashLoopBackOff"},
		{"running but not ready", running(false, ""), "false", ""},
		{"completed job pod", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}, "true", ""},
		{"pending", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}, "false", ""},
	}
	for _, c := range cases {
		got := podTopologyHealth(c.pod)
		if got["ready"] != c.wantReady || got["reason"] != c.wantReason {
			t.Errorf("%s: got %v, want ready=%s reason=%q", c.name, got, c.wantReady, c.wantReason)
		}
	}
}
