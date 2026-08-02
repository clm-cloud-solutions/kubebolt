package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// The engine's resolve loop only walks IN-MEMORY insights, and e.insights starts
// empty on every boot. So without hydration, a record the previous process left
// `active` is unreachable forever: MarkResolved has exactly one caller (off that
// loop) and Prune refuses to touch active records. These lock the consequences.

// crashPod is the cheapest state that makes crashLoopRule fire.
func crashPod(ns, name string) *corev1.Pod {
	p := pod(ns, name)
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 99,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}
	return p
}

func newHydratedEngine(store InsightStore) *Engine {
	return NewEngine(websocket.NewHub(), store, "c1", "t1")
}

// The headline regression, end to end. An insight is open when the API goes
// down; the condition clears while it is down; the SAME condition returns later.
// The operator must be notified about that return.
//
// Before hydration the stale `active` record made admitNew classify the return
// as a CONTINUATION (freshEpisode=false), so the Slack/email notification was
// silently dropped and the card inherited the ancient FirstSeen. An API restart
// — i.e. every deploy — was enough to arm it.
func TestEngine_Hydrate_RecurrenceAfterRestartStillNotifies(t *testing.T) {
	store := NewMemoryInsightStore()
	broken := &ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api")}}
	healthy := &ClusterState{Pods: []*corev1.Pod{pod("prod", "api")}}

	// Process 1: the condition fires and is persisted as active.
	e1 := newHydratedEngine(store)
	var fired1 int
	e1.SetOnNewInsight(func(models.Insight) { fired1++ })
	e1.Evaluate(broken)
	if fired1 != 1 {
		t.Fatalf("precondition: first detection should notify once, got %d", fired1)
	}

	// Process 2 (restart). The pod was fixed while we were down, so no rule
	// reproduces it. One grace evaluation, then it must resolve.
	e2 := newHydratedEngine(store)
	e2.Evaluate(healthy) // grace pass — not yet resolved
	if got := statusOf(t, store, e1); got != "active" {
		t.Fatalf("hydrated insight must survive its grace evaluation, got status %q", got)
	}
	e2.Evaluate(healthy) // grace spent — resolve
	if got := statusOf(t, store, e1); got != "resolved" {
		t.Fatalf("hydrated insight must resolve once unconfirmed twice, got status %q", got)
	}

	// The condition comes back. This is a NEW episode and must notify.
	var fired2 int
	e2.SetOnNewInsight(func(models.Insight) { fired2++ })
	e2.Evaluate(broken)
	if fired2 != 1 {
		t.Errorf("recurrence after a restart must fire a fresh notification, got %d", fired2)
	}
}

// The grace exists so one half-warm evaluation can't close the whole set. A
// hydrated insight that IS reproduced must continue seamlessly — same open
// occurrence, original FirstSeen, and no duplicate notification.
func TestEngine_Hydrate_ReconfirmedInsightContinues(t *testing.T) {
	store := NewMemoryInsightStore()
	broken := &ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api")}}

	e1 := newHydratedEngine(store)
	e1.Evaluate(broken)
	before := e1.GetAllInsights()
	if len(before) != 1 {
		t.Fatalf("precondition: want 1 insight, got %d", len(before))
	}

	e2 := newHydratedEngine(store)
	var renotified int
	e2.SetOnNewInsight(func(models.Insight) { renotified++ })
	e2.Evaluate(broken)

	after := e2.GetAllInsights()
	if len(after) != 1 {
		t.Fatalf("want 1 insight after restart, got %d", len(after))
	}
	if renotified != 0 {
		t.Errorf("a continuation must not re-notify, got %d", renotified)
	}
	if !after[0].FirstSeen.Equal(before[0].FirstSeen) {
		t.Errorf("FirstSeen must survive the restart: %s vs %s", after[0].FirstSeen, before[0].FirstSeen)
	}
	if after[0].ID != before[0].ID {
		t.Errorf("open occurrence must be kept: %s vs %s", after[0].ID, before[0].ID)
	}
}

// Hydration must be scoped: one engine may not adopt (or resolve) another
// tenant's or cluster's records.
func TestEngine_Hydrate_ScopedToTenantAndCluster(t *testing.T) {
	store := NewMemoryInsightStore()
	broken := &ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api")}}

	NewEngine(websocket.NewHub(), store, "c1", "t1").Evaluate(broken)

	other := NewEngine(websocket.NewHub(), store, "c2", "t2")
	other.Evaluate(&ClusterState{}) // nothing of its own to find
	if got := len(other.GetAllInsights()); got != 0 {
		t.Errorf("engine (t2,c2) must not hydrate (t1,c1) records, got %d", got)
	}
	recs, _ := store.List(InsightQuery{TenantID: "t1", ClusterID: "c1"})
	if len(recs) != 1 || recs[0].Status != "active" {
		t.Errorf("the other engine's record must be untouched, got %+v", recs)
	}
}

// A nil store must stay a no-op (OSS in-memory-only mode) rather than panic.
func TestEngine_Hydrate_NilStoreIsNoOp(t *testing.T) {
	e := NewEngine(websocket.NewHub(), nil, "c1", "t1")
	e.Evaluate(&ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api")}})
	if got := len(e.GetAllInsights()); got != 1 {
		t.Errorf("want 1 insight with no store wired, got %d", got)
	}
}

func statusOf(t *testing.T, store InsightStore, e *Engine) string {
	t.Helper()
	recs, err := store.List(InsightQuery{TenantID: e.tenantID, ClusterID: e.clusterID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 record, got %d", len(recs))
	}
	return recs[0].Status
}
