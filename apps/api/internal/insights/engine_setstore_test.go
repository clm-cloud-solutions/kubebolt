package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// SetStore must wire persistence onto a LIVE engine — the boot-race recovery.
// The manager's initial cluster connection is async and can create the engine
// BEFORE main.go calls SetInsightStore, leaving the engine with a nil store
// for its whole lifetime (no history reads, no persistence — the bug found
// in-vivo on a rollout restart). Once the store is set on the live engine, it
// must start persisting, keyed under the SetStore tenant + its own clusterID.
func TestEngine_SetStore_RecoversPersistence(t *testing.T) {
	e := NewEngine(websocket.NewHub(), nil, "race-cluster", "t1")

	p := pod("default", "crash-pod")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 99,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}
	state := &ClusterState{Pods: []*corev1.Pod{p}}

	// Store nil (lost the race): evaluate detects insights but persists nothing.
	e.Evaluate(state)
	store := NewMemoryInsightStore()
	if recs, _ := store.List(InsightQuery{}); len(recs) != 0 {
		t.Fatalf("precondition: fresh store should be empty, got %d", len(recs))
	}

	// Wire the store late (recovery) + evaluate again.
	e.SetStore(store, "t1")
	e.Evaluate(state)

	recs, err := store.List(InsightQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("after SetStore the live engine must persist insights, got 0")
	}
	found := false
	for _, r := range recs {
		if r.TenantID == "t1" && r.ClusterID == "race-cluster" {
			found = true
		}
	}
	if !found {
		t.Errorf("persisted record not keyed under (t1, race-cluster): %+v", recs)
	}
}

// An empty tenantID defaults to "default" (mirrors NewEngine).
func TestEngine_SetStore_DefaultsTenant(t *testing.T) {
	e := NewEngine(websocket.NewHub(), nil, "c", "")
	store := NewMemoryInsightStore()
	e.SetStore(store, "")

	p := pod("default", "crash-pod")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", RestartCount: 99,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}
	e.Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})

	recs, _ := store.List(InsightQuery{})
	if len(recs) == 0 {
		t.Fatal("expected a persisted record")
	}
	if recs[0].TenantID != "default" {
		t.Errorf("empty tenant should default to \"default\", got %q", recs[0].TenantID)
	}
}
