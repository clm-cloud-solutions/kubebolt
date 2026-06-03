package insights

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

func TestFingerprint_StableAndDistinct(t *testing.T) {
	a := Fingerprint("default", "c1", "crash-loop", "Pod/ns/p")
	b := Fingerprint("default", "c1", "crash-loop", "Pod/ns/p")
	if a != b {
		t.Fatalf("fingerprint not deterministic: %s != %s", a, b)
	}
	// Different rule, resource, cluster, or tenant → different fingerprint.
	for _, other := range []string{
		Fingerprint("default", "c1", "oom-killed", "Pod/ns/p"),
		Fingerprint("default", "c1", "crash-loop", "Pod/ns/other"),
		Fingerprint("default", "c2", "crash-loop", "Pod/ns/p"),
		Fingerprint("t2", "c1", "crash-loop", "Pod/ns/p"),
	} {
		if other == a {
			t.Fatalf("fingerprint collision: %s", other)
		}
	}
}

func TestMemoryInsightStore_LifecycleAndPrune(t *testing.T) {
	s := NewMemoryInsightStore()
	fp := Fingerprint("default", "c1", "crash-loop", "Pod/ns/p")
	t0 := time.Now().Add(-time.Hour)
	rec := &InsightRecord{
		Fingerprint: fp, TenantID: "default", ClusterID: "c1",
		RuleID: "crash-loop", Resource: "Pod/ns/p", Status: "active",
		FirstSeen: t0, LastSeen: t0,
	}
	appendOccurrence(rec, "occ-1", t0)
	if err := s.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, found, err := s.Get("default", "c1", fp)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.CurrentOccurrenceID != "occ-1" || len(got.Occurrences) != 1 {
		t.Fatalf("occurrence not recorded: %+v", got)
	}

	// Resolve, then it must drop out of the active filter and become prunable.
	resolveAt := time.Now().Add(-30 * time.Minute)
	if err := s.MarkResolved("default", "c1", fp, resolveAt); err != nil {
		t.Fatalf("mark resolved: %v", err)
	}
	got, _, _ = s.Get("default", "c1", fp)
	if got.Status != "resolved" || got.ResolvedAt == nil || got.CurrentOccurrenceID != "" {
		t.Fatalf("resolve not applied: %+v", got)
	}
	if got.Occurrences[0].ClosedAt == nil {
		t.Fatalf("occurrence not closed on resolve")
	}

	active, _ := s.List(InsightQuery{Status: "active"})
	if len(active) != 0 {
		t.Fatalf("resolved record still listed as active: %d", len(active))
	}

	// Prune horizon AFTER resolveAt removes it; an active record never prunes.
	removed, err := s.Prune(time.Now())
	if err != nil || removed != 1 {
		t.Fatalf("prune: removed=%d err=%v", removed, err)
	}
	if _, found, _ := s.Get("default", "c1", fp); found {
		t.Fatalf("record not pruned")
	}
}

// crashState returns a ClusterState containing one pod in CrashLoopBackOff
// (fires crashLoopRule deterministically).
func crashState() *ClusterState {
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

func TestEngine_PersistsAndSurvivesRestart(t *testing.T) {
	store := NewMemoryInsightStore()

	// First engine instance evaluates and persists the crash-loop insight.
	e1 := NewEngine(websocket.NewHub(), store, "c1", "default")
	e1.Evaluate(crashState())

	active := e1.GetInsights("", false)
	if len(active) == 0 {
		t.Fatalf("expected an active insight after first evaluate")
	}
	var ins = active[0]
	if ins.Fingerprint == "" || ins.RuleID == "" || ins.ClusterID != "c1" || ins.TenantID != "default" {
		t.Fatalf("insight identity not stamped: %+v", ins)
	}
	firstSeen := ins.FirstSeen
	occID := ins.ID
	if occID == "" {
		t.Fatalf("insight has no occurrence id")
	}

	// Persisted record exists.
	rec, found, _ := store.Get("default", "c1", ins.Fingerprint)
	if !found || rec.Status != "active" {
		t.Fatalf("insight not persisted active: found=%v rec=%+v", found, rec)
	}

	// Simulate a backend restart: brand-new engine, SAME store, empty memory.
	e2 := NewEngine(websocket.NewHub(), store, "c1", "default")
	if len(e2.GetInsights("", false)) != 0 {
		t.Fatalf("fresh engine should start with no in-memory insights")
	}
	e2.Evaluate(crashState())

	active2 := e2.GetInsights("", false)
	if len(active2) == 0 {
		t.Fatalf("insight should reappear after restart")
	}
	ins2 := active2[0]
	if ins2.Fingerprint != ins.Fingerprint {
		t.Fatalf("fingerprint changed across restart: %s -> %s", ins.Fingerprint, ins2.Fingerprint)
	}
	if !ins2.FirstSeen.Equal(firstSeen) {
		t.Fatalf("FirstSeen not preserved across restart: %v -> %v", firstSeen, ins2.FirstSeen)
	}
	// Same active occurrence continues (not a new episode) since it never resolved.
	if ins2.ID != occID {
		t.Fatalf("active occurrence should continue across restart: %s -> %s", occID, ins2.ID)
	}

	// Condition clears → resolved in store.
	e2.Evaluate(&ClusterState{})
	rec2, _, _ := store.Get("default", "c1", ins.Fingerprint)
	if rec2.Status != "resolved" {
		t.Fatalf("insight should be resolved after condition clears: %+v", rec2)
	}

	// History query returns it (resolved).
	hist, err := e2.ListHistory(InsightQuery{})
	if err != nil || len(hist) == 0 {
		t.Fatalf("history should include the resolved insight: n=%d err=%v", len(hist), err)
	}
}
