package cluster

import (
	"testing"
	"time"
)

// fakePooledRuntime builds a clusterRuntime that survives eviction's teardown
// (cancelFn + connector.Stop) without a live apiserver: a no-op cancel and a
// Connector whose stopCh is initialized so Stop()'s close() doesn't panic.
func fakePooledRuntime(lastUsed time.Time) *clusterRuntime {
	return &clusterRuntime{
		connector: &Connector{stopCh: make(chan struct{})},
		cancelFn:  func() {},
		lastUsed:  lastUsed,
	}
}

func TestEnforcePoolCap_EvictsLRUKeepsBuilding(t *testing.T) {
	now := time.Now()
	m := &Manager{
		tenantID:        "default",
		poolMaxRuntimes: 2,
		runtimes:        map[poolKey]*clusterRuntime{},
	}
	// Oldest, mid, newest fully-built runtimes...
	m.runtimes[poolKey{"default", "old"}] = fakePooledRuntime(now.Add(-30 * time.Minute))
	m.runtimes[poolKey{"default", "mid"}] = fakePooledRuntime(now.Add(-10 * time.Minute))
	m.runtimes[poolKey{"default", "new"}] = fakePooledRuntime(now)
	// ...plus a placeholder still building (connector nil) that must NOT be
	// evicted to satisfy the cap, even though the pool is over it.
	m.runtimes[poolKey{"default", "building"}] = &clusterRuntime{ready: make(chan struct{})}

	m.mu.Lock()
	m.enforcePoolCapLocked()
	m.mu.Unlock()

	if _, ok := m.runtimes[poolKey{"default", "old"}]; ok {
		t.Fatalf("LRU 'old' should have been evicted to satisfy the cap")
	}
	for _, keep := range []string{"mid", "new", "building"} {
		if _, ok := m.runtimes[poolKey{"default", keep}]; !ok {
			t.Fatalf("%q should have survived cap enforcement", keep)
		}
	}
	// Cap is 2 for built runtimes; the building placeholder is exempt, so 3 total.
	if n := len(m.runtimes); n != 3 {
		t.Fatalf("pool size = %d, want 3 (2 built + 1 building placeholder)", n)
	}
}

func TestReapIdle_EvictsStaleKeepsFresh(t *testing.T) {
	now := time.Now()
	m := &Manager{
		tenantID:        "default",
		poolIdleTimeout: 10 * time.Minute,
		runtimes:        map[poolKey]*clusterRuntime{},
	}
	m.runtimes[poolKey{"default", "stale"}] = fakePooledRuntime(now.Add(-20 * time.Minute))
	m.runtimes[poolKey{"default", "fresh"}] = fakePooledRuntime(now)
	m.runtimes[poolKey{"default", "building"}] = &clusterRuntime{ready: make(chan struct{})}

	m.reapIdlePooledRuntimes()

	if _, ok := m.runtimes[poolKey{"default", "stale"}]; ok {
		t.Fatalf("'stale' (idle 20m > 10m timeout) should have been reaped")
	}
	if _, ok := m.runtimes[poolKey{"default", "fresh"}]; !ok {
		t.Fatalf("'fresh' should have survived the reaper")
	}
	if _, ok := m.runtimes[poolKey{"default", "building"}]; !ok {
		t.Fatalf("building placeholder should never be reaped")
	}
}

func TestReapIdle_DisabledNoop(t *testing.T) {
	m := &Manager{
		tenantID:        "default",
		poolIdleTimeout: 0, // disabled
		runtimes:        map[poolKey]*clusterRuntime{},
	}
	m.runtimes[poolKey{"default", "x"}] = fakePooledRuntime(time.Now().Add(-24 * time.Hour))
	m.reapIdlePooledRuntimes()
	if _, ok := m.runtimes[poolKey{"default", "x"}]; !ok {
		t.Fatalf("idle eviction disabled (timeout=0) must not evict anything")
	}
}

func TestEvictPooledContext_DropsParkedRuntime(t *testing.T) {
	m := &Manager{
		tenantID: "default",
		runtimes: map[poolKey]*clusterRuntime{},
	}
	m.runtimes[poolKey{"default", "gone"}] = fakePooledRuntime(time.Now())
	m.runtimes[poolKey{"default", "stays"}] = fakePooledRuntime(time.Now())

	m.mu.Lock()
	m.evictPooledContextLocked("gone", "cluster removed")
	m.mu.Unlock()

	if _, ok := m.runtimes[poolKey{"default", "gone"}]; ok {
		t.Fatalf("removed cluster's parked runtime should be gone from the pool")
	}
	if _, ok := m.runtimes[poolKey{"default", "stays"}]; !ok {
		t.Fatalf("unrelated pooled runtime should be untouched")
	}
}
