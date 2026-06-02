package insights

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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

func TestMissingConfigDependencyRule_FiresForMissingConfigMap(t *testing.T) {
	p := pod("default", "needs-config")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "CreateContainerConfigError",
				Message: `configmap "app-config" not found`,
			},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	got := missingConfigDependencyRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("want 1 insight for missing configmap, got %d", len(got))
	}
	if got[0].Severity != "critical" {
		t.Errorf("severity = %q", got[0].Severity)
	}
	// The message should name the ConfigMap specifically (not the generic kind).
	if !strings.Contains(got[0].Message, "ConfigMap") {
		t.Errorf("message should identify ConfigMap, got %q", got[0].Message)
	}
}

func TestMissingConfigDependencyRule_FiresForMissingSecret(t *testing.T) {
	p := pod("prod", "needs-secret")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "CreateContainerConfigError",
				Message: `secret "db-creds" not found`,
			},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	got := missingConfigDependencyRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("want 1 insight for missing secret, got %d", len(got))
	}
	if !strings.Contains(got[0].Message, "Secret") {
		t.Errorf("message should identify Secret, got %q", got[0].Message)
	}
}

func TestMissingConfigDependencyRule_IgnoresOtherWaitingReasons(t *testing.T) {
	// A plain ImagePullBackOff is a different rule's concern — must not fire here.
	p := pod("default", "pulling")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := missingConfigDependencyRule().Evaluate(state); len(got) != 0 {
		t.Errorf("should not fire for ImagePullBackOff, got %d insights", len(got))
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

// deploymentWithProgressing builds a Deployment carrying a single
// Progressing condition with the given status+reason — the fixture the
// progress-deadline rule keys off.
func deploymentWithProgressing(ns, name string, status corev1.ConditionStatus, reason, msg string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{{
				Type:    appsv1.DeploymentProgressing,
				Status:  status,
				Reason:  reason,
				Message: msg,
			}},
		},
	}
}

func TestProgressDeadlineExceededRule_Fires(t *testing.T) {
	d := deploymentWithProgressing("prod", "api",
		corev1.ConditionFalse, "ProgressDeadlineExceeded",
		`ReplicaSet "api-7c5" has timed out progressing.`)
	state := &ClusterState{Deployments: []*appsv1.Deployment{d}}
	got := progressDeadlineExceededRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("want 1 insight for stalled rollout, got %d", len(got))
	}
	if got[0].Title != "Rollout Progress Deadline Exceeded" {
		t.Errorf("unexpected title: %q", got[0].Title)
	}
}

func TestProgressDeadlineExceededRule_IgnoresHealthyRollout(t *testing.T) {
	// A normal, progressing Deployment: Progressing=True, reason=NewReplicaSetAvailable.
	d := deploymentWithProgressing("prod", "api",
		corev1.ConditionTrue, "NewReplicaSetAvailable", "ReplicaSet is available.")
	state := &ClusterState{Deployments: []*appsv1.Deployment{d}}
	if got := progressDeadlineExceededRule().Evaluate(state); len(got) != 0 {
		t.Errorf("healthy rollout should not fire, got %d insights", len(got))
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
	e := NewEngine(websocket.NewHub(), nil, "test-cluster", "default")

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
