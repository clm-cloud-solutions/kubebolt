package channel

import (
	"sync"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

func TestAgentRegistry_RegisterAndGet(t *testing.T) {
	r := NewAgentRegistry()
	a := NewAgent("c1", "agent-1", "node-a", &auth.AgentIdentity{TenantID: "t1", Mode: auth.ModeIngestToken}, nil)

	if evicted := r.Register(a); evicted != nil {
		t.Errorf("first Register should not evict, got %+v", evicted)
	}
	if got := r.Get("c1"); got != a {
		t.Errorf("Get returned %p, want %p", got, a)
	}
	if got := r.Get("c-other"); got != nil {
		t.Errorf("Get(unknown) = %p, want nil", got)
	}
	if got := r.Count(); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}
}

func TestAgentRegistry_ReconnectEvictsPrevious(t *testing.T) {
	// Same (cluster_id, agent_id) means the SAME node reconnecting —
	// the previous record is stale and must be evicted.
	r := NewAgentRegistry()
	a1 := NewAgent("c1", "agent-1", "node-a", nil, nil)
	a2 := NewAgent("c1", "agent-1", "node-a", nil, nil) // same agent_id

	r.Register(a1)
	evicted := r.Register(a2)
	if evicted != a1 {
		t.Errorf("Register should return a1 as evicted on same-agent_id reconnect")
	}
	if got := r.GetByAgentID("c1", "agent-1"); got != a2 {
		t.Errorf("GetByAgentID returned the wrong agent after eviction")
	}
	if got := r.CountByCluster("c1"); got != 1 {
		t.Errorf("CountByCluster = %d, want 1", got)
	}
}

func TestAgentRegistry_AllowsMultipleAgentsPerCluster(t *testing.T) {
	// A DaemonSet has one Pod per node, all reporting the same
	// cluster_id but distinct agent_ids. They MUST coexist; the
	// registry must not evict peers.
	r := NewAgentRegistry()
	a1 := NewAgent("c1", "agent-1", "node-a", nil, nil)
	a2 := NewAgent("c1", "agent-2", "node-b", nil, nil)
	a3 := NewAgent("c1", "agent-3", "node-c", nil, nil)

	if evicted := r.Register(a1); evicted != nil {
		t.Errorf("a1 Register should not evict, got %+v", evicted)
	}
	if evicted := r.Register(a2); evicted != nil {
		t.Errorf("a2 Register should not evict a1 (different agent_id)")
	}
	if evicted := r.Register(a3); evicted != nil {
		t.Errorf("a3 Register should not evict peers")
	}

	if got := r.CountByCluster("c1"); got != 3 {
		t.Errorf("CountByCluster = %d, want 3", got)
	}
	if got := r.Count(); got != 3 {
		t.Errorf("Count = %d, want 3", got)
	}

	// GetByAgentID addresses each peer individually.
	if got := r.GetByAgentID("c1", "agent-1"); got != a1 {
		t.Errorf("GetByAgentID(agent-1) returned wrong record")
	}
	if got := r.GetByAgentID("c1", "agent-2"); got != a2 {
		t.Errorf("GetByAgentID(agent-2) returned wrong record")
	}

	// Get() still returns *some* peer — the choice is arbitrary, but
	// it must be one of them.
	picked := r.Get("c1")
	if picked != a1 && picked != a2 && picked != a3 {
		t.Errorf("Get returned an unknown agent: %p", picked)
	}
}

func TestAgentRegistry_StaleUnregisterIsNoop(t *testing.T) {
	// Pin the contract: when the SAME node reconnects (same agent_id)
	// and replaces the previous record, the previous handler's
	// defer-Unregister must NOT remove the fresh one.
	r := NewAgentRegistry()
	a1 := NewAgent("c1", "agent-1", "node-a", nil, nil)
	a2 := NewAgent("c1", "agent-1", "node-a", nil, nil) // same agent_id

	r.Register(a1)
	r.Register(a2) // evicts a1

	r.Unregister(a1) // stale call — must NOT remove a2
	if got := r.GetByAgentID("c1", "agent-1"); got != a2 {
		t.Errorf("stale Unregister silently removed the live agent")
	}

	r.Unregister(a2) // legitimate cleanup
	if got := r.GetByAgentID("c1", "agent-1"); got != nil {
		t.Errorf("Unregister did not clear the live agent")
	}
	if got := r.CountByCluster("c1"); got != 0 {
		t.Errorf("empty bucket should be pruned, CountByCluster = %d", got)
	}
}

func TestAgentRegistry_UnregisterNilIsNoop(t *testing.T) {
	r := NewAgentRegistry()
	r.Unregister(nil) // must not panic
	if got := r.Count(); got != 0 {
		t.Errorf("Count = %d, want 0", got)
	}
}

func TestAgentRegistry_ListSorted(t *testing.T) {
	r := NewAgentRegistry()
	r.Register(NewAgent("c-z", "agent-z", "node-z", nil, nil))
	r.Register(NewAgent("c-a", "agent-a", "node-a", &auth.AgentIdentity{TenantID: "t1", Mode: auth.ModeTokenReview}, nil))
	r.Register(NewAgent("c-m", "agent-m", "node-m", nil, nil))

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	if list[0].ClusterID != "c-a" || list[1].ClusterID != "c-m" || list[2].ClusterID != "c-z" {
		t.Errorf("not sorted: %v", []string{list[0].ClusterID, list[1].ClusterID, list[2].ClusterID})
	}
	// Identity fields surface only when present.
	if list[0].TenantID != "t1" || list[0].AuthMode != string(auth.ModeTokenReview) {
		t.Errorf("identity not surfaced for c-a: %+v", list[0])
	}
	if list[1].TenantID != "" || list[2].TenantID != "" {
		t.Error("nil identity should leave TenantID empty")
	}
}

func TestAgent_CloseClosesChanAndCancelsPending(t *testing.T) {
	a := NewAgent("c1", "agent-1", "node-a", nil, nil)

	// Reserve a pending request_id so we can verify Close() cleans it.
	_, _, err := a.Pending.Register("rid", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Pending.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}

	a.Close()
	a.Close() // idempotent — must not panic

	select {
	case <-a.Closed():
		// expected
	default:
		t.Error("Closed() chan should be closed after Close()")
	}
	if got := a.Pending.Pending(); got != 0 {
		t.Errorf("Close should cancel pending requests, got %d", got)
	}
}

func TestAgentRegistry_ConcurrentRegisterUnregister(t *testing.T) {
	// 10 clusters, each: register + unregister concurrently. Just
	// verify no panics and the registry settles to empty.
	r := NewAgentRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i))
			a := NewAgent(id, "agent-"+id, "node", nil, nil)
			r.Register(a)
			r.Unregister(a)
		}(i)
	}
	wg.Wait()
	if got := r.Count(); got != 0 {
		t.Errorf("Count = %d, want 0 after concurrent register/unregister", got)
	}
}
