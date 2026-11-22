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
// Scoping: a session is keyed by (org, cluster, node), and Get and
// Cancel find it only under the same three. Keyed by node name alone,
// a caller could re-attach to or cancel a drain it did not start by
// naming a node, and two clusters with a same-named node (every kind
// cluster has `kind-control-plane`) refused each other's drains with a
// 409. A cluster switch still cancels the switching org's drains
// (CancelAllForOrg).

// drainEvent is one entry on the SSE channel. Name matches the SSE
// `event:` field; data is JSON-encoded by the writer.
type drainEvent struct {
	Name string      `json:"name"`
	Data interface{} `json:"data"`
}

// drainKey names one drain: the org that started it, the cluster (the
// per-request context, as Manager.ActiveContextFor resolves it) and the node.
type drainKey struct {
	org, cluster, node string
}

// drainSession is the in-memory record of one drain operation.
type drainSession struct {
	Node      string
	OrgID     string // tenant that started the drain; used to scope CancelAllForOrg
	Cluster   string // context the drain runs in
	StartedAt time.Time
	Params    map[string]any

	// cancel cancels the drain context, aborting eviction. Pods
	// already submitted for eviction continue to terminate per
	// their grace period; new evictions stop. Idempotent.
	cancel context.CancelFunc

	mu          sync.Mutex
	events      []drainEvent // full replay buffer for reconnects
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
	sessions map[drainKey]*drainSession
}

func newDrainSessionManager() *drainSessionManager {
	return &drainSessionManager{
		sessions: make(map[drainKey]*drainSession),
	}
}

// Start creates a session for `node` in org's `cluster`. Returns
// (session, true) if the caller acquired ownership of a fresh session,
// or (existing, false) if a session is already active for that node
// there. The drain context is created here; the goroutine that
// actually runs drain is the caller's responsibility.
func (m *drainSessionManager) Start(org, cluster, node string, params map[string]any) (*drainSession, context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := drainKey{org: org, cluster: cluster, node: node}
	if existing, ok := m.sessions[key]; ok && !existing.isFinished() {
		return existing, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &drainSession{
		Node:      node,
		OrgID:     org,
		Cluster:   cluster,
		StartedAt: time.Now(),
		Params:    params,
		cancel:    cancel,
	}
	m.sessions[key] = s
	return s, ctx, true
}

// Get returns org's session for the node in cluster, if any. The bool
// indicates presence; callers should also check session.isFinished()
// to distinguish "running" from "completed but still in the cache".
// Another org's drain of a same-named node is not found.
func (m *drainSessionManager) Get(org, cluster, node string) (*drainSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[drainKey{org: org, cluster: cluster, node: node}]
	return s, ok
}

// Cancel aborts org's drain on `node` in cluster. Returns true if a
// running session existed and was signalled, false otherwise —
// including when the only drain of that node name belongs to another
// org or cluster.
func (m *drainSessionManager) Cancel(org, cluster, node string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[drainKey{org: org, cluster: cluster, node: node}]
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
	m.sessions = make(map[drainKey]*drainSession)
}

// CancelAllForOrg aborts only the in-flight drains started by org (called on
// cluster switch): a switch cancels the switching org's drains and leaves every
// other org's running, so one org switching clusters can't leave another org's
// nodes cordoned mid-drain. In single-tenant every drain carries the same org,
// so this cancels them all, as CancelAll did.
func (m *drainSessionManager) CancelAllForOrg(org string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, s := range m.sessions {
		if key.org != org {
			continue
		}
		if !s.isFinished() {
			s.cancel()
		}
		delete(m.sessions, key)
	}
}

// Cleanup removes finished sessions older than `keepFor`. Kept
// finished sessions are useful so a slow reconnect can still see
// the result, but they shouldn't accumulate across days. Caller
// schedules this on a ticker; not called automatically.
func (m *drainSessionManager) Cleanup(keepFor time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-keepFor)
	for key, s := range m.sessions {
		if s.isFinished() && s.StartedAt.Before(cutoff) {
			delete(m.sessions, key)
		}
	}
}

func (s *drainSession) isFinished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}
