package insights

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// --- Rule catalogue sanity ---

func TestAllRules_NonEmpty(t *testing.T) {
	rules := AllRules()
	if len(rules) == 0 {
		t.Fatal("AllRules returned none")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" {
			t.Error("rule missing ID")
		}
		if seen[r.ID] {
			t.Errorf("duplicate rule ID %q", r.ID)
		}
		seen[r.ID] = true
		if r.Evaluate == nil {
			t.Errorf("rule %q has nil Evaluate", r.ID)
		}
		switch r.Severity {
		case "critical", "warning", "info":
		default:
			t.Errorf("rule %q has invalid severity %q", r.ID, r.Severity)
		}
	}
}

// --- Individual rule evaluations ---

func pod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
	}
}

func TestCrashLoopRule_FiresOnRestartOver3(t *testing.T) {
	p := pod("default", "api")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "api",
		RestartCount: 5,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}

	got := crashLoopRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("want 1 insight, got %d", len(got))
	}
	if got[0].Severity != "critical" {
		t.Errorf("severity = %q", got[0].Severity)
	}
	if got[0].Namespace != "default" {
		t.Errorf("namespace = %q", got[0].Namespace)
	}
}

func TestCrashLoopRule_IgnoresLowRestarts(t *testing.T) {
	p := pod("default", "api")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "api",
		RestartCount: 2,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}

	if got := crashLoopRule().Evaluate(state); len(got) != 0 {
		t.Errorf("want 0 insights for restarts=2, got %d", len(got))
	}
}

func TestOOMKilledRule_Fires(t *testing.T) {
	p := pod("production", "worker")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "worker",
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	got := oomKilledRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("want 1 OOM insight, got %d", len(got))
	}
	if got[0].Title != "Container OOMKilled" {
		t.Errorf("title = %q", got[0].Title)
	}
}

func TestOOMKilledRule_IgnoresOtherTerminationReasons(t *testing.T) {
	p := pod("default", "worker")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "worker",
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := oomKilledRule().Evaluate(state); len(got) != 0 {
		t.Errorf("should not fire for Completed termination, got %d insights", len(got))
	}
}

func TestImagePullBackoffRule(t *testing.T) {
	p := pod("default", "broken")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "broken",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	got := imagePullBackoffRule().Evaluate(state)
	if len(got) != 1 {
		t.Errorf("want 1 insight, got %d", len(got))
	}
}

func TestNodeNotReadyRule(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Time{Time: time.Now()},
			}},
		},
	}
	state := &ClusterState{Nodes: []*corev1.Node{node}}
	got := nodeNotReadyRule().Evaluate(state)
	if len(got) != 1 {
		t.Errorf("want 1 insight, got %d", len(got))
	}
	if got[0].Category != "node" {
		t.Errorf("category = %q, want node", got[0].Category)
	}
}

func TestPVCPendingRule(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "data", Name: "vol-1"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	state := &ClusterState{PVCs: []*corev1.PersistentVolumeClaim{pvc}}
	got := pvcPendingRule().Evaluate(state)
	if len(got) != 1 {
		t.Errorf("want 1 insight for pending PVC, got %d", len(got))
	}
}

func TestFrequentRestartsRule_FiresAbove10(t *testing.T) {
	p := pod("default", "flaky")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "flaky",
		RestartCount: 12,
		// Not crash-looping — just frequent restarts
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := frequentRestartsRule().Evaluate(state); len(got) != 1 {
		t.Errorf("frequent restarts should fire at 12 restarts, got %d insights", len(got))
	}
}

func TestEngine_EvaluateIntegratesRules(t *testing.T) {
	// End-to-end: engine with real rules + crafted state → insights returned.
	e := NewEngine(websocket.NewHub())

	p := pod("default", "crash-pod")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 99,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}

	state := &ClusterState{
		Pods: []*corev1.Pod{p},
		PodMetrics: map[string]*models.MetricPoint{
			"default/crash-pod": {
				Timestamp: time.Now(),
				Resource:  "default/crash-pod",
				CPUUsage:  10,
				MemUsage:  100 * 1024 * 1024,
			},
		},
	}
	e.Evaluate(state)

	got := e.GetAllInsights()
	if len(got) == 0 {
		t.Fatal("engine produced no insights")
	}
	// At least the crash-loop insight should be present.
	var found bool
	for _, ins := range got {
		if ins.Title == "Pod in CrashLoopBackOff" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected crash-loop insight missing")
	}
}
