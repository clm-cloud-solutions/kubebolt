package channel

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

type fakeLiveness struct {
	mu      sync.Mutex
	touched [][]AgentKey
}

func (f *fakeLiveness) TouchConnected(keys []AgentKey, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, keys)
	return len(keys), nil
}

func (f *fakeLiveness) PruneUnseen(time.Time) (int, error) { return 0, nil }

func (f *fakeLiveness) beats() [][]AgentKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]AgentKey(nil), f.touched...)
}

func TestLiveKeys_AreTheAgentsThisReplicaHolds(t *testing.T) {
	r := NewAgentRegistry()
	r.Register(NewAgent("c1", "a1", "node-a", nil, nil, nil))
	r.Register(NewAgent("c1", "a2", "node-b", nil, nil, nil))
	gone := NewAgent("c2", "a3", "node-c", nil, nil, nil)
	r.Register(gone)
	r.Unregister(gone)

	keys := r.LiveKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].AgentID < keys[j].AgentID })
	want := []AgentKey{{"c1", "a1"}, {"c1", "a2"}}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("LiveKeys = %+v, want %+v", keys, want)
	}
}

func TestUnseenHorizon_NeverBelowTwelveHeartbeats(t *testing.T) {
	if got := UnseenHorizon(24 * time.Hour); got != 24*time.Hour {
		t.Fatalf("24h horizon: got %v", got)
	}
	// An operator who shortens the prune horizon to minutes must not let the
	// unseen prune take records of agents that are alive between two beats.
	if got := UnseenHorizon(10 * time.Minute); got != MinUnseenHorizon {
		t.Fatalf("10m horizon: got %v, want %v", got, MinUnseenHorizon)
	}
}

func TestRunLivenessHeartbeat_RefreshesLiveAgentsUntilCancelled(t *testing.T) {
	r := NewAgentRegistry()
	r.Register(NewAgent("c1", "a1", "node-a", nil, nil, nil))
	store := &fakeLiveness{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunLivenessHeartbeat(ctx, r, store, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(store.beats()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	beats := store.beats()
	if len(beats) < 2 {
		t.Fatalf("want at least 2 heartbeats, got %d", len(beats))
	}
	if len(beats[0]) != 1 || beats[0][0] != (AgentKey{"c1", "a1"}) {
		t.Fatalf("heartbeat touched %+v", beats[0])
	}
}
