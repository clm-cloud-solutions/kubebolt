package insights

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
			// Recent OOM — within recentEventWindow.
			Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(time.Now())},
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

// A recovered OOM (last termination well outside the window) must STOP firing so
// the engine resolves it — the lingering-forever bug this audit fixes (Bug A).
func TestOOMKilledRule_ResolvesWhenStale(t *testing.T) {
	p := pod("production", "worker")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "worker",
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(time.Now().Add(-2 * time.Hour))},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := oomKilledRule().Evaluate(state); len(got) != 0 {
		t.Errorf("stale OOM should not fire (must resolve), got %d insights", len(got))
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

func TestReadinessProbeFailingRule_FiresAfterGrace(t *testing.T) {
	p := pod("default", "not-ready")
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	p.Status.Conditions = []corev1.PodCondition{{
		Type:               corev1.PodReady,
		Status:             corev1.ConditionFalse,
		Reason:             "ContainersNotReady",
		LastTransitionTime: metav1.Time{Time: time.Now().Add(-5 * time.Minute)}, // past grace
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	got := readinessProbeFailingRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("want 1 insight for sustained not-Ready, got %d", len(got))
	}
}

func TestReadinessProbeFailingRule_IgnoresSlowStart(t *testing.T) {
	// Not-Ready but only for 10s — a legitimately slow start, must not fire.
	p := pod("default", "starting")
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	p.Status.Conditions = []corev1.PodCondition{{
		Type:               corev1.PodReady,
		Status:             corev1.ConditionFalse,
		Reason:             "ContainersNotReady",
		LastTransitionTime: metav1.Time{Time: time.Now().Add(-10 * time.Second)},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := readinessProbeFailingRule().Evaluate(state); len(got) != 0 {
		t.Errorf("slow start (10s) should not fire, got %d insights", len(got))
	}
}

func TestReadinessProbeFailingRule_IgnoresWaitingContainer(t *testing.T) {
	// A pod with a Waiting container is another rule's concern (crash/pull),
	// even if it's not-Ready past the grace window.
	p := pod("default", "crashing")
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}
	p.Status.Conditions = []corev1.PodCondition{{
		Type:               corev1.PodReady,
		Status:             corev1.ConditionFalse,
		Reason:             "ContainersNotReady",
		LastTransitionTime: metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := readinessProbeFailingRule().Evaluate(state); len(got) != 0 {
		t.Errorf("pod with Waiting container should not fire readiness rule, got %d", len(got))
	}
}

func TestLivenessProbeFailingRule_FiresOnRecurringEvent(t *testing.T) {
	ev := &corev1.Event{
		Reason:  "Unhealthy",
		Message: "Liveness probe failed: HTTP probe failed with statuscode: 500",
		Count:   4,
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Namespace: "prod", Name: "api-xyz",
		},
	}
	// The pod the event refers to must still exist for the rule to fire.
	state := &ClusterState{
		Pods:   []*corev1.Pod{pod("prod", "api-xyz")},
		Events: []*corev1.Event{ev},
	}
	got := livenessProbeFailingRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("want 1 insight for recurring liveness failure, got %d", len(got))
	}
	if got[0].Namespace != "prod" {
		t.Errorf("namespace = %q", got[0].Namespace)
	}
}

// A recurring Unhealthy event whose pod has already been deleted must NOT
// fire — Kubernetes keeps the event for ~1h after the pod is gone, and
// without a live-pod guard the rule emits a phantom insight (which the UI
// shows for an hour and Autopilot re-opens every poll tick).
func TestLivenessProbeFailingRule_IgnoresEventForDeletedPod(t *testing.T) {
	ev := &corev1.Event{
		Reason:  "Unhealthy",
		Message: "Liveness probe failed: HTTP probe failed with statuscode: 404",
		Count:   31,
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Namespace: "autopilot-demo", Name: "livefail-app-66f765fdd4-2zxm4",
		},
	}
	// No Pods in state — the workload was deleted, only stale events linger.
	state := &ClusterState{Events: []*corev1.Event{ev}}
	if got := livenessProbeFailingRule().Evaluate(state); len(got) != 0 {
		t.Errorf("stale event for deleted pod should not fire, got %d insights", len(got))
	}
}

func TestLivenessProbeFailingRule_IgnoresSingleBlip(t *testing.T) {
	ev := &corev1.Event{
		Reason:         "Unhealthy",
		Message:        "Liveness probe failed: connection refused",
		Count:          1, // single blip
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "blip"},
	}
	state := &ClusterState{Events: []*corev1.Event{ev}}
	if got := livenessProbeFailingRule().Evaluate(state); len(got) != 0 {
		t.Errorf("single blip (count=1) should not fire, got %d insights", len(got))
	}
}

func TestLivenessProbeFailingRule_IgnoresReadinessEvents(t *testing.T) {
	// A readiness probe failure must NOT be picked up by the liveness rule.
	ev := &corev1.Event{
		Reason:         "Unhealthy",
		Message:        "Readiness probe failed: HTTP probe failed",
		Count:          5,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "ready-fail"},
	}
	state := &ClusterState{Events: []*corev1.Event{ev}}
	if got := livenessProbeFailingRule().Evaluate(state); len(got) != 0 {
		t.Errorf("readiness event should not fire liveness rule, got %d insights", len(got))
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
		// Not crash-looping — just frequent restarts, the latest one recent.
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "Error", FinishedAt: metav1.NewTime(time.Now())},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := frequentRestartsRule().Evaluate(state); len(got) != 1 {
		t.Errorf("frequent restarts should fire at 12 restarts, got %d insights", len(got))
	}
}

// A high cumulative RestartCount whose last restart is old (pod stable for a
// while) must STOP firing — RestartCount never resets, so recency is what clears
// it (Bug A). Without this gate the warning showed for days.
func TestFrequentRestartsRule_ResolvesWhenStable(t *testing.T) {
	p := pod("default", "flaky")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "flaky",
		RestartCount: 12,
		State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "Error", FinishedAt: metav1.NewTime(time.Now().Add(-3 * time.Hour))},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}
	if got := frequentRestartsRule().Evaluate(state); len(got) != 0 {
		t.Errorf("stable pod (old last restart) should not fire, got %d insights", len(got))
	}
}

// Evicted pods linger as dead objects until GC — a recent eviction fires, a
// long-dead one resolves (Bug A).
func TestEvictedPodsRule_RecencyGate(t *testing.T) {
	recent := pod("default", "evicted-recent")
	recent.Status.Phase = corev1.PodFailed
	recent.Status.Reason = "Evicted"
	recent.Status.Conditions = []corev1.PodCondition{{LastTransitionTime: metav1.NewTime(time.Now())}}
	if got := evictedPodsRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{recent}}); len(got) != 1 {
		t.Errorf("recent eviction should fire, got %d insights", len(got))
	}

	stale := pod("default", "evicted-stale")
	stale.Status.Phase = corev1.PodFailed
	stale.Status.Reason = "Evicted"
	stale.CreationTimestamp = metav1.NewTime(time.Now().Add(-4 * time.Hour))
	stale.Status.Conditions = []corev1.PodCondition{{LastTransitionTime: metav1.NewTime(time.Now().Add(-4 * time.Hour))}}
	if got := evictedPodsRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{stale}}); len(got) != 0 {
		t.Errorf("stale evicted pod should not fire (must resolve), got %d insights", len(got))
	}
}

func cpuLimitPod(ns, name, cpu string) *corev1.Pod {
	p := pod(ns, name)
	p.Spec.Containers = []corev1.Container{{
		Name:      name,
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}},
	}}
	return p
}

// With the dampening window at 0, a pod over the CPU threshold fires immediately.
func TestCPUThrottleRule_FiresWhenSustained(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = 0
	defer func() { sustainedMetricWindow = old }()

	p := cpuLimitPod("default", "hot", "100m")
	state := &ClusterState{Pods: []*corev1.Pod{p}, PodMetrics: map[string]*models.MetricPoint{"default/hot": {CPUUsage: 90}}}
	if got := cpuThrottleRiskRule().Evaluate(state); len(got) != 1 {
		t.Fatalf("want 1 CPU throttle insight, got %d", len(got))
	}
}

// A brief spike (over threshold but not yet sustained past the window) is
// dampened — no insight on the first eval — so the rule doesn't flap.
func TestCPUThrottleRule_DampensBriefSpike(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = time.Hour
	defer func() { sustainedMetricWindow = old }()

	p := cpuLimitPod("default", "spike", "100m")
	state := &ClusterState{Pods: []*corev1.Pod{p}, PodMetrics: map[string]*models.MetricPoint{"default/spike": {CPUUsage: 95}}}
	if got := cpuThrottleRiskRule().Evaluate(state); len(got) != 0 {
		t.Fatalf("brief spike should be dampened, got %d insights", len(got))
	}
}

func TestMemoryPressureRule_FiresWhenSustained(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = 0
	defer func() { sustainedMetricWindow = old }()

	p := pod("default", "leaky")
	p.Spec.Containers = []corev1.Container{{
		Name:      "leaky",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("100Mi")}},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}, PodMetrics: map[string]*models.MetricPoint{"default/leaky": {MemUsage: 95 * 1024 * 1024}}}
	if got := memoryPressureRule().Evaluate(state); len(got) != 1 {
		t.Fatalf("want 1 memory pressure insight, got %d", len(got))
	}
}

// resourceUnderrequest is metric-based too, so it gets the same sustained-window.
func TestResourceUnderrequestRule_FiresWhenSustained(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = 0
	defer func() { sustainedMetricWindow = old }()

	p := pod("default", "greedy")
	p.Spec.Containers = []corev1.Container{{
		Name:      "greedy",
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
	}}
	// Usage 300m vs 100m request → request is 33% of usage (< 40%) → fires.
	state := &ClusterState{Pods: []*corev1.Pod{p}, PodMetrics: map[string]*models.MetricPoint{"default/greedy": {CPUUsage: 300}}}
	if got := resourceUnderrequestRule().Evaluate(state); len(got) != 1 {
		t.Fatalf("want 1 CPU underrequest insight, got %d", len(got))
	}
}

func TestResourceUnderrequestRule_DampensBriefSpike(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = time.Hour
	defer func() { sustainedMetricWindow = old }()

	p := pod("default", "spiky")
	p.Spec.Containers = []corev1.Container{{
		Name:      "spiky",
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}, PodMetrics: map[string]*models.MetricPoint{"default/spiky": {CPUUsage: 500}}}
	if got := resourceUnderrequestRule().Evaluate(state); len(got) != 0 {
		t.Fatalf("brief spike should be dampened, got %d insights", len(got))
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
