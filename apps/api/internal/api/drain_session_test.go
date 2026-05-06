package api

import (
	"sync"
	"testing"
	"time"
)

// drainSessionManager tests cover the contract the SSE handler
// relies on:
//   1. Start is single-acquisition — concurrent attempts on the
//      same node return the same session, with `fresh=false` for
//      losers. Without this two parallel POSTs would launch two
//      drain goroutines on the same node and the audit log would
//      be ambiguous.
//   2. subscribe replays buffered events AND delivers live ones.
//      The reconnect-after-disconnect promise depends on this.
//   3. finalize closes subscriber channels exactly once. A
//      double-close would panic.
//   4. Cancel signals the drainCtx; CancelAll signals every
//      session. These hook the cluster-switch + DELETE endpoints.

func TestDrainSessionStartIsSingleAcquisition(t *testing.T) {
	m := newDrainSessionManager()
	const node = "n1"

	// First start wins.
	s1, ctx1, fresh1 := m.Start(node, nil)
	if !fresh1 || s1 == nil || ctx1 == nil {
		t.Fatalf("first Start: got fresh=%v session=%v ctx=%v", fresh1, s1, ctx1)
	}

	// Second start while first is in-flight gets the SAME session
	// back, with fresh=false. ctx2 is meaningless (caller shouldn't
	// use it on the loser side); we don't assert on it.
	s2, _, fresh2 := m.Start(node, nil)
	if fresh2 {
		t.Errorf("second Start should not be fresh")
	}
	if s2 != s1 {
		t.Errorf("second Start should return the same session, got %p vs %p", s2, s1)
	}

	// After finalize, a NEW Start gets a fresh session.
	s1.finalize()
	s3, _, fresh3 := m.Start(node, nil)
	if !fresh3 {
		t.Errorf("after finalize, Start should be fresh")
	}
	if s3 == s1 {
		t.Errorf("post-finalize Start should produce a new session, got the same %p", s1)
	}
}

func TestDrainSessionStartConcurrent(t *testing.T) {
	// 50 goroutines race to Start on the same node. Exactly one
	// must win (fresh=true); the rest must get the same session.
	m := newDrainSessionManager()
	const node = "n1"
	const N = 50

	var wins int32
	var mu sync.Mutex
	winners := []*drainSession{}
	losers := []*drainSession{}

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			s, _, fresh := m.Start(node, nil)
			mu.Lock()
			if fresh {
				wins++
				winners = append(winners, s)
			} else {
				losers = append(losers, s)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d", wins)
	}
	if len(winners) != 1 {
		t.Fatalf("expected 1 winner session, got %d", len(winners))
	}
	for _, l := range losers {
		if l != winners[0] {
			t.Errorf("loser got a different session %p, want winner's %p", l, winners[0])
		}
	}
}

func TestDrainSessionSubscribeReplaysBuffer(t *testing.T) {
	m := newDrainSessionManager()
	s, _, _ := m.Start("n1", nil)

	s.emit(drainEvent{Name: "pod-evicted", Data: map[string]any{"pod": "p1"}})
	s.emit(drainEvent{Name: "pod-evicted", Data: map[string]any{"pod": "p2"}})

	// Subscribe AFTER the events were emitted. Replay slice should
	// have both; live channel should have nothing pending yet.
	ch, replay, unsub := s.subscribe()
	defer unsub()

	if len(replay) != 2 {
		t.Fatalf("replay len = %d, want 2", len(replay))
	}
	if replay[0].Data.(map[string]any)["pod"] != "p1" || replay[1].Data.(map[string]any)["pod"] != "p2" {
		t.Errorf("replay order wrong: %v", replay)
	}

	// Emit a third — should land on the live channel.
	s.emit(drainEvent{Name: "pod-evicted", Data: map[string]any{"pod": "p3"}})
	select {
	case ev := <-ch:
		if ev.Data.(map[string]any)["pod"] != "p3" {
			t.Errorf("live event = %v, want p3", ev)
		}
	case <-time.After(time.Second):
		t.Error("live event never delivered")
	}
}

func TestDrainSessionSubscribeAfterFinalize(t *testing.T) {
	// Subscribing to a finalized session must NOT block, must NOT
	// panic, and must return the full replay buffer + a closed
	// channel so the caller's range loop terminates immediately.
	m := newDrainSessionManager()
	s, _, _ := m.Start("n1", nil)
	s.emit(drainEvent{Name: "pod-evicted", Data: map[string]any{"pod": "p1"}})
	s.emit(drainEvent{Name: "drain-complete", Data: map[string]any{"status": "drained"}})
	s.finalize()

	ch, replay, unsub := s.subscribe()
	defer unsub()

	if len(replay) != 2 {
		t.Errorf("replay len = %d, want 2", len(replay))
	}

	// Channel must be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed when session already finalized")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel read blocked; expected immediate close")
	}
}

func TestDrainSessionFinalizeIdempotent(t *testing.T) {
	// Calling finalize twice must not panic (the close on
	// subscriber channels would panic on second invocation if not
	// guarded). drain.Helper occasionally surfaces a final
	// callback even after RunNodeDrain returns; the goroutine's
	// `defer session.finalize()` plus an explicit finalize on
	// error path are both legitimate and we shouldn't crash.
	m := newDrainSessionManager()
	s, _, _ := m.Start("n1", nil)
	ch, _, _ := s.subscribe()

	s.finalize()
	s.finalize() // must not panic

	// Channel must be closed exactly once.
	_, ok := <-ch
	if ok {
		t.Error("expected closed channel after finalize")
	}
}

func TestDrainSessionEmitAfterFinalizeIsSilent(t *testing.T) {
	// drain.Helper can fire OnPodDeletedOrEvicted after we've
	// finalized (race between the goroutine returning and a
	// trailing eviction confirmation). The session must drop those
	// events silently rather than write to closed channels.
	m := newDrainSessionManager()
	s, _, _ := m.Start("n1", nil)
	s.finalize()

	// Should NOT panic, should NOT block.
	done := make(chan struct{})
	go func() {
		s.emit(drainEvent{Name: "pod-evicted", Data: map[string]any{"pod": "late"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("emit after finalize blocked")
	}
}

func TestDrainSessionCancel(t *testing.T) {
	m := newDrainSessionManager()
	s, ctx, _ := m.Start("n1", nil)

	if ctx.Err() != nil {
		t.Fatalf("ctx unexpectedly already cancelled: %v", ctx.Err())
	}
	if !m.Cancel("n1") {
		t.Error("Cancel returned false on running session")
	}
	if ctx.Err() == nil {
		t.Error("ctx should be cancelled after Cancel")
	}

	// Cancel of an already-cancelled session is a no-op (returns
	// true once finalized).
	s.finalize()
	if m.Cancel("n1") {
		t.Error("Cancel of finished session should return false")
	}
	if m.Cancel("n2") {
		t.Error("Cancel of unknown node should return false")
	}
}

func TestDrainSessionCancelAll(t *testing.T) {
	m := newDrainSessionManager()
	_, ctx1, _ := m.Start("n1", nil)
	_, ctx2, _ := m.Start("n2", nil)
	_, ctx3, _ := m.Start("n3", nil)

	m.CancelAll()

	if ctx1.Err() == nil || ctx2.Err() == nil || ctx3.Err() == nil {
		t.Error("CancelAll did not cancel every session")
	}
	// Map should be cleared so a fresh Start gets a new session.
	s, _, fresh := m.Start("n1", nil)
	if !fresh {
		t.Errorf("Start after CancelAll should be fresh")
	}
	_ = s
}

func TestDrainSessionCleanup(t *testing.T) {
	m := newDrainSessionManager()
	old, _, _ := m.Start("old", nil)
	old.finalize()
	// Backdate the start so cleanup will purge it.
	old.StartedAt = time.Now().Add(-2 * time.Hour)

	running, _, _ := m.Start("running", nil)
	_ = running // stays in map regardless of age (still active)

	m.Cleanup(time.Hour)

	if _, ok := m.Get("old"); ok {
		t.Error("Cleanup should have removed the old finished session")
	}
	if _, ok := m.Get("running"); !ok {
		t.Error("Cleanup must NOT remove an active session")
	}
}
