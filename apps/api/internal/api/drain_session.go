package api

import (
	"context"
	"sync"
	"time"
)

// Drain sessions are server-side records of an in-flight (or
// recently-finished) drain operation. The promise to the operator,
// from the spec:
//
//   "Server-side drain survives browser disconnect; UI re-attaches
//    on return."
//
// To meet that we keep the drain goroutine alive on its own context
// (NOT the HTTP request context — that dies as soon as the SSE
// reader disconnects), and we buffer every emitted event so a
// reconnecting client gets the full history before the live tail.
//
// Single-cluster scoping: the active context already gates which
// cluster's resources we touch, so we key sessions by node name
// alone. If the operator switches clusters mid-drain, the manager's
// CancelAll runs (mirrors PortForwardManager.StopAll); a same-named
// node in a different cluster gets its own fresh session.

// drainEvent is one entry on the SSE channel. Name matches the SSE
// `event:` field; data is JSON-encoded by the writer.
type drainEvent struct {
	Name string      `json:"name"`
	Data interface{} `json:"data"`
}

// drainSession is the in-memory record of one drain operation.
type drainSession struct {
	Node      string
	StartedAt time.Time
	Params    map[string]any

	// cancel cancels the drain context, aborting eviction. Pods
	// already submitted for eviction continue to terminate per
	// their grace period; new evictions stop. Idempotent.
	cancel context.CancelFunc

	mu          sync.Mutex
	events      []drainEvent  // full replay buffer for reconnects
	subscribers []chan drainEvent
	finished    bool
}

func (s *drainSession) emit(ev drainEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		// Late callbacks after we finalized the session — ignore.
		// Drain.Helper occasionally fires a final OnPodDeleted after
		// RunNodeDrain returns; safer to drop than to broadcast to
		// unsubscribed channels.
		return
	}
	s.events = append(s.events, ev)
	for _, ch := range s.subscribers {
		// Non-blocking send: a slow consumer must not stall drain
		// progress. Buffered channels (capacity 64) absorb bursts;
		// past that we drop. Reconnecting client will pick up the
		// dropped events from `events` because they're appended
		// before the broadcast.
		select {
		case ch <- ev:
		default:
		}
	}
}

// subscribe returns a buffered channel that will receive every
// future event, AND a snapshot of all events emitted before the
// subscribe call. The caller is responsible for unsubscribing
// (releasing the channel) when done.
func (s *drainSession) subscribe() (<-chan drainEvent, []drainEvent, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan drainEvent, 64)
	if !s.finished {
		s.subscribers = append(s.subscribers, ch)
	} else {
		// Already done — return a closed channel so the caller's
		// for/range terminates immediately after replaying the
		// buffer.
		close(ch)
	}
	replay := make([]drainEvent, len(s.events))
	copy(replay, s.events)
	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, c := range s.subscribers {
			if c == ch {
				s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
				break
			}
		}
	}
	return ch, replay, unsub
}

func (s *drainSession) finalize() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	for _, ch := range s.subscribers {
		close(ch)
	}
	s.subscribers = nil
}

// drainSessionManager owns the lifecycle of all in-flight drain
// sessions for the active cluster. One drain at a time per node
// (concurrent attempts hit Conflict).
type drainSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*drainSession // key = node name
}

func newDrainSessionManager() *drainSessionManager {
	return &drainSessionManager{
		sessions: make(map[string]*drainSession),
	}
}

// Start creates a session for `node`. Returns (session, true) if
// the caller acquired ownership of a fresh session, or (existing,
// false) if a session is already active for that node. The drain
// context is created here; the goroutine that actually runs drain
// is the caller's responsibility.
func (m *drainSessionManager) Start(node string, params map[string]any) (*drainSession, context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[node]; ok && !existing.isFinished() {
		return existing, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &drainSession{
		Node:      node,
		StartedAt: time.Now(),
		Params:    params,
		cancel:    cancel,
	}
	m.sessions[node] = s
	return s, ctx, true
}

// Get returns the session for the node if any. The bool indicates
// presence; callers should also check session.isFinished() to
// distinguish "running" from "completed but still in the cache".
func (m *drainSessionManager) Get(node string) (*drainSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[node]
	return s, ok
}

// Cancel aborts the drain on `node`. Returns true if a running
// session existed and was signalled, false otherwise.
func (m *drainSessionManager) Cancel(node string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[node]
	if !ok || s.isFinished() {
		return false
	}
	s.cancel()
	return true
}

// CancelAll aborts every in-flight drain. Called on cluster switch
// — pods evicted on the previous cluster stay evicted, but we stop
// trying to evict more (the new cluster's restConfig wouldn't be
// the right target anyway). Mirrors PortForwardManager.StopAll.
func (m *drainSessionManager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if !s.isFinished() {
			s.cancel()
		}
	}
	m.sessions = make(map[string]*drainSession)
}

// Cleanup removes finished sessions older than `keepFor`. Kept
// finished sessions are useful so a slow reconnect can still see
// the result, but they shouldn't accumulate across days. Caller
// schedules this on a ticker; not called automatically.
func (m *drainSessionManager) Cleanup(keepFor time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-keepFor)
	for node, s := range m.sessions {
		if s.isFinished() && s.StartedAt.Before(cutoff) {
			delete(m.sessions, node)
		}
	}
}

func (s *drainSession) isFinished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}
