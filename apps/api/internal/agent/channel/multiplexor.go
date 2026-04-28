// Package channel hosts the per-agent state the backend keeps for an
// open AgentChannel stream. The Multiplexor owns request_id → reply
// correlation; the AgentRegistry indexes live agents by cluster_id.
//
// Sprint A.5 commit 2 lands the types + tests. The Sprint A.5 transport
// (commit 5) is what actually issues kube_request and consumes the
// pending replies.
package channel

import (
	"errors"
	"fmt"
	"sync"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// Multiplexor is the per-agent map of in-flight request_ids → reply
// channels. It is intentionally minimal: it does not write to the gRPC
// stream itself. The transport layer registers a slot, sends the
// BackendMessage on the stream, and reads replies from the chan.
//
// Watch streams: a single registration receives multiple AgentMessages
// (kube_event ...) on the same chan until terminated by either a
// final kube_response (status code carrier) or a stream_closed; the
// Multiplexor then auto-cleans the slot.
//
// Unary calls: a single AgentMessage arrives (kube_response) and the
// slot auto-cleans.
//
// Cancel exists for caller-initiated cleanup (ctx cancellation, agent
// disconnect, etc.).
type Multiplexor struct {
	mu      sync.Mutex
	pending map[string]*pendingCall
}

type pendingCall struct {
	ch     chan *agentv2.AgentMessage
	watch  bool
	closed bool // pin idempotency: closing twice would panic on the chan
}

func NewMultiplexor() *Multiplexor {
	return &Multiplexor{pending: make(map[string]*pendingCall)}
}

// ErrDuplicateRequestID is returned when Register is called twice with
// the same request_id. Callers MUST mint fresh UUIDs (the transport
// already does — this guard catches programmer errors only).
var ErrDuplicateRequestID = errors.New("multiplexor: duplicate request_id")

// Register reserves a slot for the given request_id and returns the
// chan the caller will read replies from + a cancel func that releases
// the slot. The caller is responsible for writing the corresponding
// BackendMessage to the gRPC stream after Register succeeds.
//
// watch=true allocates a buffered chan (64 slots) so a slow consumer
// does not block Deliver immediately. Saturation drops the oldest event
// — see Deliver. watch=false uses a 1-slot buffer; deliver-after-deliver
// is a programmer error.
func (m *Multiplexor) Register(requestID string, watch bool) (<-chan *agentv2.AgentMessage, func(), error) {
	if requestID == "" {
		return nil, nil, errors.New("multiplexor: empty request_id")
	}
	m.mu.Lock()
	if _, exists := m.pending[requestID]; exists {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: %s", ErrDuplicateRequestID, requestID)
	}
	bufSize := 1
	if watch {
		bufSize = 64
	}
	pc := &pendingCall{ch: make(chan *agentv2.AgentMessage, bufSize), watch: watch}
	m.pending[requestID] = pc
	m.mu.Unlock()

	cancel := func() {
		m.cancel(requestID)
	}
	return pc.ch, cancel, nil
}

// Deliver routes an incoming AgentMessage to the slot matching its
// request_id. Messages without a request_id (heartbeat, metrics, hello)
// are dropped silently — the caller should dispatch those before
// reaching here.
//
// Auto-cleanup:
//   - Non-watch: the slot is released as soon as one message lands.
//     A second message for the same request_id is dropped.
//   - Watch: the slot stays open until either a kube_response (final
//     status) or stream_closed message arrives, after which it is
//     released.
//
// If the slot's chan is full (watch saturation), the oldest event is
// dropped to keep Deliver non-blocking. The transport surfaces this as
// "buffer overflow → backend should re-list" via the bookmark
// machinery (Sprint B+).
func (m *Multiplexor) Deliver(msg *agentv2.AgentMessage) {
	if msg == nil || msg.GetRequestId() == "" {
		return
	}
	m.mu.Lock()
	pc, ok := m.pending[msg.GetRequestId()]
	m.mu.Unlock()
	if !ok || pc.closed {
		return
	}

	select {
	case pc.ch <- msg:
	default:
		// Saturated. Drop oldest, then push.
		select {
		case <-pc.ch:
		default:
		}
		select {
		case pc.ch <- msg:
		default:
			// Race-lost: another goroutine consumed in between. Skip.
		}
	}

	if !pc.watch || isTerminal(msg) {
		m.cancel(msg.GetRequestId())
	}
}

// Pending returns the count of in-flight request_ids. Test-only;
// production code should not depend on this.
func (m *Multiplexor) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// cancel releases a slot. Idempotent: calling it twice (the auto-cleanup
// path + the caller's cancel) is safe.
func (m *Multiplexor) cancel(requestID string) {
	m.mu.Lock()
	pc, ok := m.pending[requestID]
	if ok {
		delete(m.pending, requestID)
	}
	m.mu.Unlock()
	if ok && !pc.closed {
		pc.closed = true
		close(pc.ch)
	}
}

// CancelAll releases every pending slot. Called when the agent's
// stream ends so any in-flight transport.RoundTrip wakes up with a
// closed chan instead of hanging forever.
func (m *Multiplexor) CancelAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.pending))
	for id := range m.pending {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.cancel(id)
	}
}

// isTerminal reports whether this message ends a watch stream — i.e.
// the slot should be cleaned up on Deliver.
func isTerminal(msg *agentv2.AgentMessage) bool {
	switch msg.GetKind().(type) {
	case *agentv2.AgentMessage_KubeResponse, *agentv2.AgentMessage_StreamClosed:
		return true
	}
	return false
}
