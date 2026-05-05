package channel

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// DefaultTunnelIdleTimeout is the inactivity window after which an
// open tunnel is force-closed by the backend's watchdog. Catches
// orphan tunnels left behind when the agent crashes mid-tunnel and
// the apiserver-side HTTP client doesn't notice the upstream EOF
// quickly enough. 5 minutes is generous enough that a paused exec
// session (operator typing slowly, kubectl logs --follow with no
// output) doesn't get killed, but tight enough that leaks self-heal
// in well under the prune horizon. Set to 0 to disable (default off
// in tests). Operators override via `KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT`.
var DefaultTunnelIdleTimeout = 5 * time.Minute

// DefaultTunnelWindowBytes is the credit window for one tunnel
// session. It bounds how many bytes the local sender may have
// in-flight before it must wait for KubeStreamAck from the peer
// (Sprint A.5 §0.8). 256 KiB sits in the sweet spot:
//
//   - too small: every burst pays a round-trip; throughput collapses
//     for portforward / kubectl-cp use cases
//   - too large: a single noisy exec can buffer 16 MiB of stdout and
//     starve other tunnels' fairness
//
// Operators can override per-instance via the env var documented in
// §0.9 (`KUBEBOLT_AGENT_TUNNEL_WINDOW_BYTES`). The transport reads
// it once at construction time.
const DefaultTunnelWindowBytes = 256 * 1024

// MaxTunnelChunkBytes caps a single KubeStreamData payload. Larger
// writes are split into multiple frames. Picked to fit comfortably
// inside the gRPC default max receive size (4 MiB) with headroom for
// proto framing — and small enough that ACK round-trips refresh the
// credit window before throughput stalls.
const MaxTunnelChunkBytes = 32 * 1024

// ErrTunnelClosed is returned by Read/Write after Close() or after
// the peer half-closes the duplex.
var ErrTunnelClosed = errors.New("channel: tunnel closed")

// TunnelConn implements net.Conn over the bidi gRPC channel for one
// upgrade session (SPDY exec / portforward / WebSocket). It is
// returned as the Body of the *http.Response that AgentProxyTransport
// produces when it detects the 101 Switching Protocols handshake.
//
// Threading model:
//
//   - Read is single-consumer, called by the upper SPDY framing layer
//     (k8s.io/apimachinery/pkg/util/httpstream). A demux goroutine
//     drains the multiplexor chan and pushes data into a private
//     bytes channel; Read pops from it. KubeStreamAck messages take
//     a separate path into the credit tracker; KubeStreamData{eof}
//     terminates Read with io.EOF.
//
//   - Write is single-producer, called by the SPDY layer. It chunks
//     data, blocks on the credit window when the peer falls behind,
//     and emits BackendMessage{KubeStreamData} via agent.Send.
//
//   - Deadlines are honored at Read/Write granularity. SetDeadline
//     replaces both halves; SetReadDeadline / SetWriteDeadline scope
//     to one direction. Implementation uses a per-direction ticker
//     channel so blocking reads/writes wake on time.
//
// Production hardening (idle timeout, max duration, audit logging,
// metrics) is handled one layer up by the caller (Sprint A.5 §0.9
// commit 8f).
type TunnelConn struct {
	requestID string
	agent     *Agent

	// Demux: fed by a goroutine reading from incoming; data goes to
	// readBytes, ACKs to creditCh.
	incoming <-chan *agentv2.AgentMessage
	cancel   func()

	// Read side. readBytes is a chan-of-byte-slice; the demux pushes
	// payloads as-is, Read drains them. Closed when the read half is
	// done (either peer EOF, peer StreamClosed, or local Close).
	readBytes chan []byte
	readErr   atomic.Value // stores error; nil = stream healthy
	readBuf   []byte       // tail of the last partial slice

	// Write side. credit is the number of bytes the local sender may
	// transmit before having to wait for KubeStreamAck. It starts
	// equal to window; each Send subtracts the chunk size; each ACK
	// adds the consumed bytes. Block when credit < chunkLen.
	//
	// credit can briefly exceed window if the peer over-ACKs (peer
	// bug) — we don't cap because clamping there would silently lose
	// the surplus and stall future writes. Healthy peer never
	// produces credit > window in practice.
	creditCh chan uint64
	window   uint64
	credit   uint64 // accessed only by Write goroutine + reserveCredits
	writeMu  sync.Mutex

	// Lifecycle.
	closeOnce sync.Once
	closed    chan struct{}

	// Deadlines.
	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time

	// Synthetic addresses for net.Conn — never inspected by SPDY but
	// must be non-nil to satisfy the interface.
	clusterID string

	// Audit + idle-timeout instrumentation. `path` is the apiserver
	// path being tunneled (e.g. /api/v1/namespaces/X/pods/Y/exec) —
	// captured at open for the audit log so operators can correlate
	// a tunnel with the user-facing action that opened it.
	// `lastActivity` is the unix-nano of the most recent successful
	// Read or Write; the watchdog goroutine compares it against
	// idleTimeout. `closeReason` is set before closing so the audit
	// log distinguishes peer-EOF from idle-timeout from local-Close.
	path         string
	openedAt     time.Time
	lastActivity atomic.Int64
	bytesIn      atomic.Uint64
	bytesOut     atomic.Uint64
	idleTimeout  time.Duration
	closeReason  atomic.Value // string
}

// newTunnelConn wires up the conn and spawns the demux goroutine.
// Caller is responsible for ensuring `incoming` is the channel
// returned by Multiplexor.Register with mode=SlotTunnel.
//
// `path` is the apiserver path being tunneled (used for audit logs
// only — never inspected as a routing key). `idleTimeout` of 0
// disables the watchdog; production callers should pass a non-zero
// value (DefaultTunnelIdleTimeout) so orphan tunnels don't leak when
// the agent crashes mid-session.
func newTunnelConn(requestID, clusterID, path string, agent *Agent, incoming <-chan *agentv2.AgentMessage, cancel func(), window uint64, idleTimeout time.Duration) *TunnelConn {
	if window == 0 {
		window = DefaultTunnelWindowBytes
	}
	t := &TunnelConn{
		requestID:   requestID,
		clusterID:   clusterID,
		agent:       agent,
		incoming:    incoming,
		cancel:      cancel,
		readBytes:   make(chan []byte, 32),
		creditCh:    make(chan uint64, 32),
		closed:      make(chan struct{}),
		window:      window,
		credit:      window, // start with full send capacity
		path:        path,
		openedAt:    time.Now(),
		idleTimeout: idleTimeout,
	}
	t.lastActivity.Store(t.openedAt.UnixNano())
	slog.Info("agent-proxy tunnel opened",
		slog.String("cluster_id", clusterID),
		slog.String("agent_id", agent.AgentID),
		slog.String("request_id", requestID),
		slog.String("path", path),
	)
	go t.demuxLoop()
	if idleTimeout > 0 {
		go t.idleWatchdog(idleTimeout)
	}
	return t
}

// idleWatchdog closes the tunnel when no Read/Write has happened
// inside `idleTimeout`. Ticks every quarter of the timeout, floored
// at 100ms (so unit tests with sub-second timeouts work) and capped
// implicitly via the timeout itself. Production runs with timeout=5m
// → tick=75s, which is plenty fine-grained: orphan detection is
// bounded at timeout + tick. Exits when the tunnel closes by any
// other path.
func (t *TunnelConn) idleWatchdog(timeout time.Duration) {
	tick := timeout / 4
	if tick < 100*time.Millisecond {
		tick = 100 * time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-t.closed:
			return
		case now := <-ticker.C:
			last := time.Unix(0, t.lastActivity.Load())
			if now.Sub(last) >= timeout {
				t.closeReason.Store("idle timeout")
				_ = t.Close()
				return
			}
		}
	}
}

// demuxLoop drains the multiplexor chan and routes each message to
// the right side of the duplex. Exits when the chan closes (slot
// terminated) or when Close() runs.
func (t *TunnelConn) demuxLoop() {
	defer close(t.readBytes)
	for {
		select {
		case msg, ok := <-t.incoming:
			if !ok {
				// Slot closed by Multiplexor — record final error so
				// pending Read calls wake.
				if t.readErr.Load() == nil {
					t.readErr.Store(io.EOF)
				}
				if _, ok := t.closeReason.Load().(string); !ok {
					t.closeReason.Store("multiplexor slot closed")
				}
				return
			}
			switch m := msg.GetKind().(type) {
			case *agentv2.AgentMessage_KubeStreamData:
				data := m.KubeStreamData.GetData()
				if len(data) > 0 {
					select {
					case t.readBytes <- data:
					case <-t.closed:
						return
					}
				}
				if m.KubeStreamData.GetEof() {
					t.readErr.Store(io.EOF)
					if _, ok := t.closeReason.Load().(string); !ok {
						t.closeReason.Store("peer EOF")
					}
					return
				}
			case *agentv2.AgentMessage_KubeStreamAck:
				select {
				case t.creditCh <- m.KubeStreamAck.GetBytesConsumed():
				default:
					// Saturated credit chan: peer is ACKing faster
					// than we're consuming. We can safely drop —
					// outstanding will be released on the next ACK.
				}
			case *agentv2.AgentMessage_StreamClosed:
				reason := m.StreamClosed.GetReason()
				t.readErr.Store(fmt.Errorf("%w: peer closed (%s)", ErrTunnelClosed, reason))
				if _, ok := t.closeReason.Load().(string); !ok {
					t.closeReason.Store("peer stream_closed: " + reason)
				}
				return
			case *agentv2.AgentMessage_KubeResponse:
				// 101 handshake already consumed by RoundTrip before
				// constructing this conn. A late KubeProxyResponse on
				// a tunnel slot is anomalous; surface the status as
				// an error so callers can distinguish from clean EOF.
				t.readErr.Store(fmt.Errorf("agent-proxy tunnel: unexpected late kube_response status=%d", m.KubeResponse.GetStatusCode()))
				return
			}
		case <-t.closed:
			return
		}
	}
}

// Read pulls bytes from the demux pipeline. Honors SetReadDeadline
// by waking with a "read deadline exceeded" error when the timer
// fires. Returns io.EOF on clean peer half-close.
func (t *TunnelConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Drain the carry-over from the previous Read first.
	if len(t.readBuf) > 0 {
		n := copy(p, t.readBuf)
		t.readBuf = t.readBuf[n:]
		return n, nil
	}

	deadline := t.getReadDeadline()
	var deadlineC <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		deadlineC = timer.C
	}

	select {
	case chunk, ok := <-t.readBytes:
		if !ok {
			if err, _ := t.readErr.Load().(error); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		n := copy(p, chunk)
		if n < len(chunk) {
			t.readBuf = chunk[n:]
		}
		// Bump activity + counters AFTER the copy so a partial chunk
		// still counts as live traffic and the watchdog won't fire on
		// a slow consumer.
		t.lastActivity.Store(time.Now().UnixNano())
		t.bytesIn.Add(uint64(len(chunk)))
		return n, nil
	case <-t.closed:
		return 0, ErrTunnelClosed
	case <-deadlineC:
		return 0, &deadlineError{op: "read"}
	}
}

// Write splits p into chunks no larger than MaxTunnelChunkBytes, then
// emits each as a BackendMessage{KubeStreamData} subject to credit
// flow control. Blocks on the credit window when outstanding bytes
// reach the window. Honors SetWriteDeadline.
func (t *TunnelConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	written := 0
	for written < len(p) {
		// Cap chunk to MaxTunnelChunkBytes (proto frame size) AND to
		// the window (so a single chunk can never exceed our credit
		// allowance — otherwise we'd deadlock when chunkLen > window).
		chunkLen := len(p) - written
		if chunkLen > MaxTunnelChunkBytes {
			chunkLen = MaxTunnelChunkBytes
		}
		if uint64(chunkLen) > t.window {
			chunkLen = int(t.window)
		}
		if err := t.reserveCredits(uint64(chunkLen)); err != nil {
			return written, err
		}
		chunk := p[written : written+chunkLen]
		err := t.agent.Send(&agentv2.BackendMessage{
			RequestId: t.requestID,
			Kind: &agentv2.BackendMessage_KubeStreamData{
				KubeStreamData: &agentv2.KubeStreamData{Data: chunk},
			},
		})
		if err != nil {
			// Couldn't send — return whatever we managed and let the
			// caller decide whether to retry. The credit reservation
			// is sunk (peer never sees the data, never ACKs); the
			// next Write may stall — but at that point the agent is
			// likely disconnecting anyway.
			return written, err
		}
		written += chunkLen
		t.lastActivity.Store(time.Now().UnixNano())
		t.bytesOut.Add(uint64(chunkLen))
	}
	return written, nil
}

// reserveCredits drains creditCh until at least `need` bytes of
// credit are available, then deducts. Returns once credits are
// reserved, deadline expires, or conn closes.
//
// Single-consumer pattern: writeMu (held by caller) ensures we are
// the only goroutine reading t.credit, so the read-modify-write
// here doesn't need a CAS — plain assignment is safe.
func (t *TunnelConn) reserveCredits(need uint64) error {
	deadline := t.getWriteDeadline()
	var deadlineC <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		deadlineC = timer.C
	}

	for atomic.LoadUint64(&t.credit) < need {
		select {
		case consumed := <-t.creditCh:
			atomic.AddUint64(&t.credit, consumed)
		case <-t.closed:
			return ErrTunnelClosed
		case <-deadlineC:
			return &deadlineError{op: "write"}
		}
	}
	atomic.AddUint64(&t.credit, ^uint64(need-1)) // credit -= need
	return nil
}

// Close sends a half-close signal to the peer and tears down the
// slot. Idempotent. Safe to call concurrently with Read/Write —
// pending operations wake with ErrTunnelClosed.
func (t *TunnelConn) Close() error {
	var sendErr error
	t.closeOnce.Do(func() {
		// Best-effort EOF marker so the peer's exec session ends
		// cleanly. If Send fails (agent disconnected) we still tear
		// down locally — the slot's chan close will let readers wake.
		sendErr = t.agent.Send(&agentv2.BackendMessage{
			RequestId: t.requestID,
			Kind: &agentv2.BackendMessage_KubeStreamData{
				KubeStreamData: &agentv2.KubeStreamData{Eof: true},
			},
		})
		close(t.closed)
		t.cancel() // releases the multiplexor slot

		// Audit log on close — single line per tunnel lifecycle so
		// operators can grep "agent-proxy tunnel closed" and see
		// who/where/how-much/why for every session. Reason defaults
		// to "local close" when no other path set it (idle watchdog,
		// peer EOF in demuxLoop) — captures the intentional local
		// teardown path.
		reason, _ := t.closeReason.Load().(string)
		if reason == "" {
			reason = "local close"
		}
		slog.Info("agent-proxy tunnel closed",
			slog.String("cluster_id", t.clusterID),
			slog.String("agent_id", t.agent.AgentID),
			slog.String("request_id", t.requestID),
			slog.String("path", t.path),
			slog.String("reason", reason),
			slog.Duration("duration", time.Since(t.openedAt)),
			slog.Uint64("bytes_in", t.bytesIn.Load()),
			slog.Uint64("bytes_out", t.bytesOut.Load()),
		)
	})
	return sendErr
}

// SetDeadline sets both read and write deadlines. Zero clears them.
func (t *TunnelConn) SetDeadline(deadline time.Time) error {
	t.deadlineMu.Lock()
	t.readDeadline = deadline
	t.writeDeadline = deadline
	t.deadlineMu.Unlock()
	return nil
}

// SetReadDeadline scopes the deadline to Read calls.
func (t *TunnelConn) SetReadDeadline(deadline time.Time) error {
	t.deadlineMu.Lock()
	t.readDeadline = deadline
	t.deadlineMu.Unlock()
	return nil
}

// SetWriteDeadline scopes the deadline to Write calls (including the
// credit-wait phase).
func (t *TunnelConn) SetWriteDeadline(deadline time.Time) error {
	t.deadlineMu.Lock()
	t.writeDeadline = deadline
	t.deadlineMu.Unlock()
	return nil
}

func (t *TunnelConn) getReadDeadline() time.Time {
	t.deadlineMu.Lock()
	defer t.deadlineMu.Unlock()
	return t.readDeadline
}

func (t *TunnelConn) getWriteDeadline() time.Time {
	t.deadlineMu.Lock()
	defer t.deadlineMu.Unlock()
	return t.writeDeadline
}

// LocalAddr returns a synthetic address. SPDY libraries don't actually
// inspect this — it just needs to be non-nil to satisfy net.Conn.
func (t *TunnelConn) LocalAddr() net.Addr { return tunnelAddr{role: "backend"} }

// RemoteAddr returns the synthetic apiserver address scoped by
// cluster_id, mirroring the rest.Config.Host built by ClusterAccess.
func (t *TunnelConn) RemoteAddr() net.Addr { return tunnelAddr{role: "agent", cluster: t.clusterID} }

// tunnelAddr is the net.Addr for a tunneled conn. Network() is
// "agent-proxy" so anyone log-grepping can immediately see what
// kind of connection produced an event.
type tunnelAddr struct {
	role    string // "backend" | "agent"
	cluster string
}

func (a tunnelAddr) Network() string { return "agent-proxy" }
func (a tunnelAddr) String() string {
	if a.cluster != "" {
		return fmt.Sprintf("%s:%s.agent.local", a.role, a.cluster)
	}
	return a.role
}

// TunnelHandshakeBody is the *http.Response.Body we return after a
// successful 101 Switching Protocols handshake. It wraps the actual
// TunnelConn but presents a deliberately decoupled lifecycle:
//
//   - Read on the body returns EOF immediately (HTTP-level body for
//     a 101 response is empty by definition).
//   - Close on the body is a no-op AFTER Extract() has been called.
//
// Why: K8s' spdy.Negotiate (used by remotecommand exec / portforward)
// does `defer resp.Body.Close()` immediately after a successful
// upgrade. If our Body were the TunnelConn directly, that defer would
// terminate the tunnel ~microseconds after 101 — before the SPDY
// framing layer even starts the handshake. Pinned by the SPDY
// smoke-test failure where pumpToApiserver exited 1ms after pumping
// began, with bytes_received=0.
//
// Extract() is the handoff: the upgrader (NewConnection) calls
// Extract to take ownership of the raw TunnelConn. After that,
// closing the body is a no-op (Negotiate's defer is harmless), and
// the TunnelConn lifecycle is managed by the SPDY conn that wraps
// it (utilspdy.NewClientConnection). When the SPDY conn legitimately
// closes — exec session ended, user closed terminal — it calls
// TunnelConn.Close, which sends KubeStreamData{eof:true} and
// releases the multiplexor slot.
type TunnelHandshakeBody struct {
	conn      *TunnelConn
	extracted atomic.Bool
}

// NewTunnelHandshakeBody wraps a TunnelConn for use as *http.Response.Body
// of a 101 Switching Protocols response.
func NewTunnelHandshakeBody(conn *TunnelConn) *TunnelHandshakeBody {
	return &TunnelHandshakeBody{conn: conn}
}

// Read returns io.EOF immediately — the HTTP body of a 101 response
// is always empty per spec, and post-extraction the underlying conn
// is owned by the SPDY layer.
func (b *TunnelHandshakeBody) Read(p []byte) (int, error) {
	return 0, io.EOF
}

// Close is the lifecycle hinge:
//
//   - Before Extract() was called: the upgrade attempt is being
//     abandoned (handler error path); close the underlying tunnel
//     so the slot doesn't leak.
//   - After Extract() was called: the SPDY layer owns the conn —
//     return nil so Negotiate's defer is harmless. The SPDY conn's
//     own Close path will reach TunnelConn.Close when the session
//     legitimately ends.
func (b *TunnelHandshakeBody) Close() error {
	if b.extracted.Load() {
		return nil
	}
	return b.conn.Close()
}

// Extract takes ownership of the raw TunnelConn. After this call,
// Body.Close becomes a no-op so Negotiate's `defer resp.Body.Close()`
// in K8s' spdy.Negotiate doesn't tear down the tunnel. The caller
// is now responsible for closing the returned conn — typically by
// passing it to utilspdy.NewClientConnection, which closes the conn
// when the SPDY session ends.
func (b *TunnelHandshakeBody) Extract() *TunnelConn {
	b.extracted.Store(true)
	return b.conn
}

// deadlineError is the net-package-style error returned when a
// deadline expires. Implements net.Error with Timeout()=true so the
// SPDY framing layer treats it the way it would a TCP read timeout.
type deadlineError struct{ op string }

func (e *deadlineError) Error() string { return "agent-proxy tunnel: " + e.op + " deadline exceeded" }
func (e *deadlineError) Timeout() bool { return true }

// Temporary marks the error as retryable. SPDY consumers tend to
// honor this.
func (e *deadlineError) Temporary() bool { return true }
