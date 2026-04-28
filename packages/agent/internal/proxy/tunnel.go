package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// MessageSender is the subset of channel.Client the tunnel pump uses.
// Extracted as an interface so tests can plug a chan-backed stub
// instead of needing a real bidi gRPC stream. *channel.Client
// satisfies this implicitly.
type MessageSender interface {
	Send(*agentv2.AgentMessage) error
}

// DefaultTunnelWindowBytes mirrors the backend's TunnelConn window.
// The agent's outbound (apiserver→backend) direction respects this
// window so a slow backend reader can't make the agent monopolize
// the bidi gRPC channel — see Sprint A.5 §0.8.
const DefaultTunnelWindowBytes uint64 = 256 * 1024

// TunnelChunkBytes is the largest KubeStreamData payload the agent
// emits in one frame. Picked to fit comfortably under gRPC's default
// max message size and small enough that ACK round-trips refresh the
// credit window before throughput stalls.
const TunnelChunkBytes = 32 * 1024

// inboundBufferDepth bounds the per-tunnel queue of KubeStreamData
// frames waiting to be written to the apiserver. Hitting saturation
// terminates the session — bytes can't be dropped silently for exec.
// In healthy operation the backend's credit-based outbound pacing
// (Sprint A.5 §0.8) keeps this far from the limit.
const inboundBufferDepth = 64

// tunnelSession is the per-request_id state for an upgrade tunnel
// in flight on the agent side. Backend → agent traffic (KubeStreamData,
// KubeStreamAck) lands here via the channel.Handler routing methods;
// dedicated goroutines (started by Handler.handleUpgrade) drain the
// chans and bidi-shovel bytes between the apiserver conn and the
// gRPC client.
type tunnelSession struct {
	requestID string

	// Backend → agent direction. inbound queues KubeStreamData frames;
	// acks queues credit refunds for the agent's outbound pacing.
	inbound chan *agentv2.KubeStreamData
	acks    chan uint64

	// Lifecycle. close() is idempotent; closing `done` wakes both
	// pump goroutines + makes future Handle* dispatch drop silently.
	done chan struct{}
	once sync.Once
}

func newTunnelSession(requestID string) *tunnelSession {
	return &tunnelSession{
		requestID: requestID,
		inbound:   make(chan *agentv2.KubeStreamData, inboundBufferDepth),
		acks:      make(chan uint64, 32),
		done:      make(chan struct{}),
	}
}

func (s *tunnelSession) close() {
	s.once.Do(func() { close(s.done) })
}

// run pumps bytes between conn (apiserver) and the backend gRPC
// stream. Returns only when both pump goroutines exit. Caller MUST
// Close conn afterwards (idempotent — run also closes it on
// teardown to wake any blocked I/O).
//
// The watcher goroutine is the key to clean shutdown: pumpToBackend
// is blocked in conn.Read for the lifetime of the session and only
// returns when the read errors. When sess.close() or ctx.Done()
// fires we Close the conn — that surfaces an error to the blocked
// Read and the pump exits.
func (s *tunnelSession) run(ctx context.Context, conn io.ReadWriteCloser, sender MessageSender, window uint64) {
	defer s.close()

	var wg sync.WaitGroup
	wg.Add(3)

	// Watcher: closes conn on session teardown / ctx cancel.
	// Closing is idempotent — the caller's defer Close is harmless.
	go func() {
		defer wg.Done()
		select {
		case <-s.done:
		case <-ctx.Done():
		}
		_ = conn.Close()
	}()

	go func() {
		defer wg.Done()
		s.pumpToBackend(ctx, conn, sender, window)
	}()
	go func() {
		defer wg.Done()
		s.pumpToApiserver(ctx, conn, sender)
	}()

	wg.Wait()
}

// pumpToBackend drains conn (apiserver bytes) and emits each chunk
// as AgentMessage{KubeStreamData} to the backend. Honors send-side
// credit window: blocks on the acks chan when local credit reaches
// zero, resumes when the backend ACKs consumed bytes.
//
// On clean EOF or read error, sends a final KubeStreamData{eof:true}
// so the backend's TunnelConn.Read wakes with io.EOF. On ctx cancel
// or session close, exits without sending eof — the matching
// StreamClosed comes from handleUpgrade's terminal send.
func (s *tunnelSession) pumpToBackend(ctx context.Context, conn io.Reader, sender MessageSender, window uint64) {
	credit := window
	buf := make([]byte, TunnelChunkBytes)
	for {
		// Wait for credits if local window is exhausted.
		for credit == 0 {
			select {
			case n := <-s.acks:
				credit += n
			case <-ctx.Done():
				return
			case <-s.done:
				return
			}
		}

		// Cap chunk by remaining credit so we never speculatively
		// send beyond the window.
		toRead := uint64(len(buf))
		if toRead > credit {
			toRead = credit
		}

		n, err := conn.Read(buf[:toRead])
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			credit -= uint64(n)
			if sendErr := sender.Send(&agentv2.AgentMessage{
				RequestId: s.requestID,
				Kind: &agentv2.AgentMessage_KubeStreamData{
					KubeStreamData: &agentv2.KubeStreamData{Data: data},
				},
			}); sendErr != nil {
				slog.Warn("agent proxy tunnel: send to backend failed",
					slog.String("request_id", s.requestID),
					slog.String("error", sendErr.Error()))
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Debug("agent proxy tunnel: apiserver read ended",
					slog.String("request_id", s.requestID),
					slog.String("error", err.Error()))
			}
			// Half-close the read-half cleanly so the backend exits
			// its Read loop with io.EOF.
			_ = sender.Send(&agentv2.AgentMessage{
				RequestId: s.requestID,
				Kind: &agentv2.AgentMessage_KubeStreamData{
					KubeStreamData: &agentv2.KubeStreamData{Eof: true},
				},
			})
			return
		}
	}
}

// pumpToApiserver drains the inbound chan (KubeStreamData from the
// backend) and writes each chunk to the apiserver conn. After every
// successful write emits a KubeStreamAck so the backend's credit
// window refreshes.
//
// EOF semantics: a KubeStreamData{eof:true} from the backend means
// "no more bytes from this side". For SPDY/WebSocket the upper
// framing layer carries its own end-of-stream signal in-band; what
// we close at the byte-tunnel layer is just our half of the
// duplex. If the underlying conn supports CloseWrite (TCP / TLS) we
// use it; otherwise we exit and let pumpToBackend's eventual conn
// teardown propagate.
func (s *tunnelSession) pumpToApiserver(ctx context.Context, conn io.WriteCloser, sender MessageSender) {
	for {
		select {
		case data, ok := <-s.inbound:
			if !ok {
				return
			}
			if eof := data.GetEof(); eof {
				// Best-effort half-close. Errors here are fine — the
				// other goroutine (pumpToBackend) will eventually exit
				// and the session winds down.
				if cw, ok := conn.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
				return
			}
			payload := data.GetData()
			if len(payload) == 0 {
				continue
			}
			if _, err := conn.Write(payload); err != nil {
				slog.Warn("agent proxy tunnel: apiserver write failed",
					slog.String("request_id", s.requestID),
					slog.String("error", err.Error()))
				return
			}
			// Credit refund: backend can send another payload of this
			// size before having to wait. Safe to drop on Send error
			// — the session is tearing down anyway.
			_ = sender.Send(&agentv2.AgentMessage{
				RequestId: s.requestID,
				Kind: &agentv2.AgentMessage_KubeStreamAck{
					KubeStreamAck: &agentv2.KubeStreamAck{BytesConsumed: uint64(len(payload))},
				},
			})
		case <-ctx.Done():
			return
		case <-s.done:
			return
		}
	}
}

// isUpgradeRequest mirrors the detection in
// apps/api/internal/agent/channel/transport.go. Headers preserved by
// the backend's flattenRequestHeaders include both Connection and
// Upgrade for upgrade attempts.
func isUpgradeRequest(req *agentv2.KubeProxyRequest) bool {
	hdrs := req.GetHeaders()
	if hdrs == nil {
		return false
	}
	conn := headerValue(hdrs, "Connection")
	upg := headerValue(hdrs, "Upgrade")
	if upg == "" {
		return false
	}
	for _, token := range strings.Split(conn, ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

// headerValue does a case-insensitive lookup. KubeProxyRequest.Headers
// is map[string]string with keys in canonical form on the wire
// (set by http.CanonicalHeaderKey on the backend), but we don't
// assume that here.
func headerValue(headers map[string]string, name string) string {
	if v, ok := headers[name]; ok {
		return v
	}
	canon := http.CanonicalHeaderKey(name)
	if v, ok := headers[canon]; ok {
		return v
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
