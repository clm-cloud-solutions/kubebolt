package insights

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// A store that cannot be read is not a store that says "new". A Postgres
// restart or switchover used to make every active insight look brand new: a
// second episode, a reset FirstSeen and a second notification. And the policy
// cache answered a failed read with factory defaults, cached until a policy
// changed, so the rules an org had turned off came back.

var errStoreDown = errors.New("connection refused")

// flakyStore fails Get (and the hydration List) while down is set.
type flakyStore struct {
	*MemoryInsightStore
	down atomic.Bool
}

func (s *flakyStore) Get(tenantID, clusterID, fingerprint string) (*InsightRecord, bool, error) {
	if s.down.Load() {
		return nil, false, errStoreDown
	}
	return s.MemoryInsightStore.Get(tenantID, clusterID, fingerprint)
}

func (s *flakyStore) List(q InsightQuery) ([]InsightRecord, error) {
	if s.down.Load() {
		return nil, errStoreDown
	}
	return s.MemoryInsightStore.List(q)
}

func TestEngine_UnreadableStore_DoesNotReopenOrRenotify(t *testing.T) {
	store := &flakyStore{MemoryInsightStore: NewMemoryInsightStore()}
	broken := &ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api")}}

	// Process 1 detects the crash loop: one episode, one notification.
	e1 := newHydratedEngine(store)
	var fired1 int
	e1.SetOnNewInsight(func(models.Insight) { fired1++ })
	e1.Evaluate(broken)
	if fired1 != 1 {
		t.Fatalf("precondition: first detection should notify once, got %d", fired1)
	}
	before := recordOf(t, store, e1)

	// Process 2 boots while Postgres is switching over: hydration and reads fail.
	store.down.Store(true)
	e2 := newHydratedEngine(store)
	var fired2 int
	e2.SetOnNewInsight(func(models.Insight) { fired2++ })
	e2.Evaluate(broken)
	e2.Evaluate(broken)
	if fired2 != 0 {
		t.Fatalf("an unreadable store notified %d time(s) for an insight already open", fired2)
	}
	if got := len(e2.GetAllInsights()); got != 0 {
		t.Errorf("an insight the store could not classify was admitted (%d in memory)", got)
	}

	// Postgres is back: the insight is recognized as the episode already open.
	store.down.Store(false)
	e2.Evaluate(broken)
	if fired2 != 0 {
		t.Errorf("the open episode notified again after the store came back (%d)", fired2)
	}
	after := recordOf(t, store, e1)
	if len(after.Occurrences) != len(before.Occurrences) {
		t.Errorf("occurrences %d → %d: a second episode was opened", len(before.Occurrences), len(after.Occurrences))
	}
	if !after.FirstSeen.Equal(before.FirstSeen) {
		t.Errorf("FirstSeen reset from %v to %v", before.FirstSeen, after.FirstSeen)
	}
	if got := e2.GetAllInsights(); len(got) != 1 || got[0].ID != before.CurrentOccurrenceID {
		t.Errorf("in memory %+v, want the open occurrence %s", got, before.CurrentOccurrenceID)
	}
}

func TestEngine_UnreadableStore_KeepsTheOccurrenceHistory(t *testing.T) {
	store := &flakyStore{MemoryInsightStore: NewMemoryInsightStore()}
	broken := &ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api")}}
	healthy := &ClusterState{Pods: []*corev1.Pod{pod("prod", "api")}}

	prevCooldown := ReopenCooldown
	ReopenCooldown = 0
	defer func() { ReopenCooldown = prevCooldown }()

	// Two episodes of the same identity: fire, resolve, fire again.
	e := newHydratedEngine(store)
	e.Evaluate(broken)
	e.Evaluate(healthy)
	time.Sleep(2 * time.Millisecond)
	e.Evaluate(broken)
	before := recordOf(t, store, e)
	if len(before.Occurrences) != 2 {
		t.Fatalf("precondition: want 2 occurrences, got %d", len(before.Occurrences))
	}

	// A refresh whose read fails while the write still works must not
	// replace the record with a rebuilt one.
	store.down.Store(true)
	e.Evaluate(broken)
	store.down.Store(false)
	e.Evaluate(broken)

	after := recordOf(t, store, e)
	if len(after.Occurrences) != 2 {
		t.Errorf("occurrences %d → %d: a failed read rebuilt the record", len(before.Occurrences), len(after.Occurrences))
	}
	if !after.FirstSeen.Equal(before.FirstSeen) {
		t.Errorf("FirstSeen moved from %v to %v", before.FirstSeen, after.FirstSeen)
	}
}

func TestEngine_UnavailablePolicies_SkipThePass(t *testing.T) {
	store := NewMemoryInsightStore()
	broken := &ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api")}}
	healthy := &ClusterState{Pods: []*corev1.Pod{pod("prod", "api")}}
	t.Cleanup(func() { SetPolicySource(nil) })

	e := newHydratedEngine(store)
	var fired int
	e.SetOnNewInsight(func(models.Insight) { fired++ })
	e.Evaluate(broken)
	if fired != 1 {
		t.Fatalf("precondition: one notification, got %d", fired)
	}

	// The policies cannot be read: the pass changes nothing, in either
	// direction — no resolution of what is open, no admission of what is new.
	SetPolicySource(func(string, string) PolicySnapshot { return PolicySnapshot{Unavailable: true} })
	e.Evaluate(healthy)
	if got := statusOf(t, store, e); got != "active" {
		t.Errorf("a pass without policies resolved the open insight (status %q)", got)
	}
	e.Evaluate(&ClusterState{Pods: []*corev1.Pod{crashPod("prod", "api"), crashPod("prod", "worker")}})
	if fired != 1 {
		t.Errorf("a pass without policies admitted and notified a new insight (%d notifications)", fired)
	}

	// Readable again: evaluation resumes.
	SetPolicySource(nil)
	e.Evaluate(healthy)
	e.Evaluate(healthy)
	if got := statusOf(t, store, e); got != "resolved" {
		t.Errorf("after the policies came back the insight should resolve, status %q", got)
	}
}

func recordOf(t *testing.T, store InsightStore, e *Engine) InsightRecord {
	t.Helper()
	recs, err := store.List(InsightQuery{TenantID: e.tenantID, ClusterID: e.clusterID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 record, got %d", len(recs))
	}
	return recs[0]
}
