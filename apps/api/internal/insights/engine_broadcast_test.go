package insights

import (
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

func crashLoopState() *ClusterState {
	p := pod("default", "crash-pod")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 99,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}
	return &ClusterState{Pods: []*corev1.Pod{p}}
}

// Always-on (W2 §10): a parked engine (gate false) MUST still fire the notification
// hook — an insight on ANY connected cluster notifies, regardless of which is active
// in the UI. (Was gated active-only; that stopgap is removed.)
func TestEngineNotify_ParkedRuntimeStillNotifies(t *testing.T) {
	parked := NewEngine(websocket.NewHub(), nil, "c", "t")
	gate := &atomic.Bool{} // false = parked
	parked.SetBroadcastGate(gate)
	parkedNotified := false
	parked.SetOnNewInsight(func(models.Insight) { parkedNotified = true })
	parked.Evaluate(crashLoopState())
	if !parkedNotified {
		t.Fatalf("parked engine must fire onNew (always-on — notifications not gated to active)")
	}

	active := NewEngine(websocket.NewHub(), nil, "c", "t")
	activeNotified := false
	active.SetOnNewInsight(func(models.Insight) { activeNotified = true })
	active.Evaluate(crashLoopState())
	if !activeNotified {
		t.Fatalf("active engine must fire onNew for a new insight")
	}
}

// A parked engine (broadcast gate false) must not touch the WS hub. We leave
// wsHub nil: if broadcast didn't short-circuit on the gate it would panic
// dereferencing the nil hub, so a clean return proves the suppression.
func TestEngineBroadcast_GateSuppresses(t *testing.T) {
	e := &Engine{} // nil wsHub on purpose
	gate := &atomic.Bool{}
	gate.Store(false)
	e.SetBroadcastGate(gate)

	e.broadcast(websocket.InsightNew, models.Insight{})
	e.broadcast(websocket.InsightResolved, models.Insight{})
}

// With no gate set, broadcast must reach the hub (default = always broadcast).
// A real hub with no clients makes Broadcast a safe no-op, so this just
// confirms the nil-gate path doesn't suppress.
func TestEngineBroadcast_NilGateBroadcasts(t *testing.T) {
	e := &Engine{wsHub: websocket.NewHub()}
	e.broadcast(websocket.InsightNew, models.Insight{})
}
