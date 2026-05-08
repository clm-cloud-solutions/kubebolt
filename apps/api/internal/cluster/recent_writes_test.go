package cluster

import (
	"sync"
	"testing"
	"time"
)

func TestRecentWritesOverlayApplyHit(t *testing.T) {
	o := NewRecentWritesOverlay()
	o.Record("deployments", "default", "payments", "paused", true, 5*time.Second)

	m := map[string]interface{}{"name": "payments", "paused": false}
	o.Apply("deployments", "default", "payments", m)

	if m["paused"] != true {
		t.Errorf("paused = %v, want true (overlay should override the stale informer value)", m["paused"])
	}
	if m["name"] != "payments" {
		t.Errorf("name clobbered: %v", m["name"])
	}
}

func TestRecentWritesOverlayApplyMiss(t *testing.T) {
	// Different resource — no override should apply.
	o := NewRecentWritesOverlay()
	o.Record("deployments", "default", "payments", "paused", true, 5*time.Second)

	m := map[string]interface{}{"name": "other", "paused": false}
	o.Apply("deployments", "default", "other", m)

	if m["paused"] != false {
		t.Errorf("paused = %v, want false (no overlay recorded for this resource)", m["paused"])
	}
}

// TestRecentWritesOverlayExpiry verifies expired entries are NOT
// applied. This is the safety property that prevents a stuck overlay
// from confusing the operator long after the informer caught up.
func TestRecentWritesOverlayExpiry(t *testing.T) {
	o := NewRecentWritesOverlay()
	o.Record("deployments", "default", "payments", "paused", true, 10*time.Millisecond)

	time.Sleep(30 * time.Millisecond)

	m := map[string]interface{}{"paused": false}
	o.Apply("deployments", "default", "payments", m)

	if m["paused"] != false {
		t.Errorf("paused = %v, want false (overlay should have expired)", m["paused"])
	}
}

// TestRecentWritesOverlayExpiredEntryIsGCd verifies that Apply
// opportunistically removes expired entries from the underlying map,
// so the structure doesn't grow unboundedly when callers churn
// resources.
func TestRecentWritesOverlayExpiredEntryIsGCd(t *testing.T) {
	o := NewRecentWritesOverlay()
	o.Record("deployments", "default", "payments", "paused", true, 5*time.Millisecond)

	time.Sleep(20 * time.Millisecond)

	m := map[string]interface{}{}
	o.Apply("deployments", "default", "payments", m)

	o.mu.RLock()
	defer o.mu.RUnlock()
	if _, ok := o.entries["deployments:default:payments"]; ok {
		t.Error("expired bucket was not GC'd by Apply")
	}
}

// TestRecentWritesOverlayReRecordReplaces — re-recording the same
// field replaces the prior entry. Required because the operator
// might Pause then Resume within a few seconds; the latest write
// wins, not the first.
func TestRecentWritesOverlayReRecordReplaces(t *testing.T) {
	o := NewRecentWritesOverlay()
	o.Record("deployments", "default", "payments", "paused", true, 5*time.Second)
	o.Record("deployments", "default", "payments", "paused", false, 5*time.Second)

	m := map[string]interface{}{"paused": true}
	o.Apply("deployments", "default", "payments", m)

	if m["paused"] != false {
		t.Errorf("paused = %v, want false (latest Record should win)", m["paused"])
	}
}

func TestRecentWritesOverlayMultipleFields(t *testing.T) {
	o := NewRecentWritesOverlay()
	o.Record("deployments", "default", "payments", "paused", true, 5*time.Second)
	o.Record("deployments", "default", "payments", "labels", map[string]string{"team": "payments"}, 5*time.Second)

	m := map[string]interface{}{"paused": false, "labels": map[string]string{}}
	o.Apply("deployments", "default", "payments", m)

	if m["paused"] != true {
		t.Errorf("paused not overlaid: %v", m["paused"])
	}
	if l, ok := m["labels"].(map[string]string); !ok || l["team"] != "payments" {
		t.Errorf("labels not overlaid: %v", m["labels"])
	}
}

func TestRecentWritesOverlayClear(t *testing.T) {
	o := NewRecentWritesOverlay()
	o.Record("deployments", "default", "payments", "paused", true, 5*time.Second)
	o.Clear("deployments", "default", "payments")

	m := map[string]interface{}{"paused": false}
	o.Apply("deployments", "default", "payments", m)

	if m["paused"] != false {
		t.Errorf("paused = %v, want false (Clear should drop the override)", m["paused"])
	}
}

func TestRecentWritesOverlayNilSafety(t *testing.T) {
	var o *RecentWritesOverlay
	// All methods should be safe on a nil overlay (defensive).
	o.Record("deployments", "default", "payments", "paused", true, time.Second)
	o.Apply("deployments", "default", "payments", map[string]interface{}{})
	o.Clear("deployments", "default", "payments")
}

// TestRecentWritesOverlayConcurrentRecordApply hammers the overlay
// from multiple goroutines to surface any obvious race. Run with
// `go test -race` to catch real races.
func TestRecentWritesOverlayConcurrentRecordApply(t *testing.T) {
	o := NewRecentWritesOverlay()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			o.Record("deployments", "default", "payments", "paused", i%2 == 0, 5*time.Second)
		}(i)
		go func() {
			defer wg.Done()
			m := map[string]interface{}{"paused": false}
			o.Apply("deployments", "default", "payments", m)
		}()
	}
	wg.Wait()
}
