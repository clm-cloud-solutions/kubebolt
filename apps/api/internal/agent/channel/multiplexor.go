// Package channel hosts the per-agent state the backend keeps for an
// open AgentChannel stream. The Multiplexor owns request_id → reply
// correlation; the AgentRegistry indexes live agents by cluster_id.
//
// Sprint A.5 commit 2 lands the types + tests. The Sprint A.5 transport
// (commit 5) is what actually issues kube_request and consumes the
// pending replies. Commit 8c extends the Multiplexor with a
// loss-intolerant tunnel mode for SPDY/WebSocket upgrade tunnels.
package channel

import (
	"errors"
	"fmt"
	"sync"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// SlotMode classifies how Deliver behaves when more than one message
// arrives for the same request_id and how saturation is handled.
//
// Three modes today:
//
//   - SlotUnary: one reply expected. Buffer size 1; auto-clean on the
//     first message.
//
//   - SlotWatch: many KubeProxyWatchEvent messages until a terminal
//     KubeProxyResponse / StreamClosed. Buffer size 64. Saturation
//     drops the OLDEST event — client-go's reflector recovers via
//     re-list when it notices the gap (Sprint A.5 §0.2 contract).
//
//   - SlotTunnel: bidirectional byte stream for SPDY/WebSocket
//     upgrade traffic (pod exec / portforward). Buffer size 256.
//     Saturation MUST NOT drop bytes — exec sessions corrupt at the
//     character level if any frame is lost. Instead the slot is
//     closed with a synthetic StreamClosed{reason="buffer_overflow"}
//     so the consumer can tear the tunnel down cleanly.
//
//     Normal-operation backpressure is enforced one layer up by the
//     credit-based flow control on KubeStreamAck (Sprint A.5 §0.8).
//     The tunnel buffer here is a safety margin, not the primary
//     congestion mechanism — hitting saturation should be rare and
//     indicates a sender ignoring ACKs.
type SlotMode int

const (
	SlotUnary SlotMode = iota
	SlotWatch
	SlotTunnel
)

// String renders the mode for log lines / error messages.
func (m SlotMode) String() string {
	switch m {
	case SlotUnary:
		return "unary"
	case SlotWatch:
		return "watch"
	case SlotTunnel:
		return "tunnel"
	}
	return fmt.Sprintf("SlotMode(%d)", int(m))
}

// Buffer sizes per mode. Tuned so the common case never blocks the
// gRPC read loop and the worst case (drop / close) is at least
// observable.
const (
	unarySlotBufferSize  = 1
	watchSlotBufferSize  = 64
	tunnelSlotBufferSize = 256
)

func (m SlotMode) bufferSize() int {
	switch m {
	case SlotUnary:
		return unarySlotBufferSize
	case SlotWatch:
		return watchSlotBufferSize
	case SlotTunnel:
		return tunnelSlotBufferSize
	}
	return 1
}

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
// Tunnel streams: like watch but loss-intolerant. KubeStreamData
// flows in one direction (agent→backend) on the slot's chan; the
// peer direction (backend→agent) is sent directly via agent.Send().
// The slot terminates on KubeStreamData{eof:true} or StreamClosed.
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
	mode   SlotMode
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
// mode picks buffer size + saturation policy (see SlotMode docs).
// Production callers:
//   - SlotUnary  for non-watch kube_request
//   - SlotWatch  for kube_request with watch=true
//   - SlotTunnel for kube_request with `Connection: Upgrade` headers
//     (SPDY exec / portforward / attach)
func (m *Multiplexor) Register(requestID string, mode SlotMode) (<-chan *agentv2.AgentMessage, func(), error) {
	if requestID == "" {
		return nil, nil, errors.New("multiplexor: empty request_id")
	}
	m.mu.Lock()
	if _, exists := m.pending[requestID]; exists {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: %s", ErrDuplicateRequestID, requestID)
	}
	pc := &pendingCall{
		ch:   make(chan *agentv2.AgentMessage, mode.bufferSize()),
		mode: mode,
	}
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
//   - SlotUnary: the slot is released as soon as one message lands.
//     A second message for the same request_id is dropped.
//   - SlotWatch: the slot stays open until either a kube_response
//     (final status) or stream_closed message arrives, after which
//     it is released. Saturation drops the OLDEST event (§0.2).
//   - SlotTunnel: the slot stays open until KubeStreamData{eof:true},
//     a kube_response (101 Switching Protocols only — anything else
//     is a protocol violation we still treat as terminal), or
//     stream_closed. Saturation closes the slot with a synthetic
//     StreamClosed{reason="buffer_overflow"} — bytes MUST NOT be
//     dropped silently, the consumer needs to know the tunnel is
//     unrecoverable.
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
		// Delivered.
	default:
		// Saturation. Behavior depends on mode.
		switch pc.mode {
		case SlotWatch:
			// Drop oldest, then push the new one.
			select {
			case <-pc.ch:
			default:
			}
			select {
			case pc.ch <- msg:
			default:
				// Race-lost: another goroutine consumed in between. Skip.
			}
		case SlotTunnel:
			// Tunnels can't tolerate byte loss. Replace the buffer
			// contents with a synthetic terminator so the consumer
			// reads it next, sees the overflow, and tears down.
			m.terminateWithOverflow(msg.GetRequestId())
			return
		default:
			// SlotUnary: a second message arrived. Drop silently (the
			// first one is already in flight to the consumer).
		}
	}

	if isTerminal(msg, pc.mode) {
		m.cancel(msg.GetRequestId())
	}
}

// terminateWithOverflow drains the slot's chan and pushes a synthetic
// StreamClosed so the consumer's blocked Read sees it before EOF.
// Called only on tunnel saturation (a should-never-happen safety net).
func (m *Multiplexor) terminateWithOverflow(requestID string) {
	m.mu.Lock()
	pc, ok := m.pending[requestID]
	if !ok || pc.closed {
		m.mu.Unlock()
		return
	}
	// Drain whatever's in the buffer — the consumer can't process it
	// safely (preceding bytes were dropped), so we'd rather it sees
	// the overflow signal first.
	for {
		select {
		case <-pc.ch:
			continue
		default:
		}
		break
	}
	overflow := &agentv2.AgentMessage{
		RequestId: requestID,
		Kind: &agentv2.AgentMessage_StreamClosed{
			StreamClosed: &agentv2.StreamClosed{Reason: "buffer_overflow"},
		},
	}
	select {
	case pc.ch <- overflow:
	default:
		// Race-lost. The slot is being drained from elsewhere; cancel
		// will close the chan anyway.
	}
	m.mu.Unlock()
	m.cancel(requestID)
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

// isTerminal reports whether this message ends the stream — i.e.
// the slot should be cleaned up on Deliver. Per-mode:
//
//   - Unary  : KubeProxyResponse → terminal (StreamClosed shouldn't
//     reach unary slots in practice but treated terminal for safety).
//   - Watch  : KubeProxyResponse OR StreamClosed → terminal.
//   - Tunnel : StreamClosed OR KubeStreamData{eof:true} → terminal.
//     KubeProxyResponse on a tunnel slot is the 101 Switching
//     Protocols handshake; that does NOT terminate the slot — the
//     bytes phase is just starting.
func isTerminal(msg *agentv2.AgentMessage, mode SlotMode) bool {
	switch m := msg.GetKind().(type) {
	case *agentv2.AgentMessage_StreamClosed:
		return true
	case *agentv2.AgentMessage_KubeResponse:
		// 101 Switching Protocols on a tunnel slot is the handshake
		// completion; bytes phase follows. Anything else (including
		// 5xx during upgrade) terminates the slot.
		if mode == SlotTunnel && m.KubeResponse.GetStatusCode() == 101 {
			return false
		}
		return true
	case *agentv2.AgentMessage_KubeStreamData:
		return mode == SlotTunnel && m.KubeStreamData.GetEof()
	}
	return false
}
