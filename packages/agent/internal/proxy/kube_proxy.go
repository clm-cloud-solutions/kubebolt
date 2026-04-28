// Package proxy is the agent-side half of the K8s API proxy. It receives
// KubeProxyRequest payloads on the AgentChannel, executes them against
// the local in-cluster apiserver using the agent's ServiceAccount
// credentials, and sends KubeProxyResponse / KubeProxyWatchEvent back
// to the backend.
//
// One KubeAPIProxy per agent process; stateless beyond the configured
// transport. The agent's identity (SA token + cluster CA) is embedded
// in the http.RoundTripper built from rest.Config — the backend does
// NOT supply credentials in KubeProxyRequest.Headers, and any
// Authorization that comes through is stripped to prevent token
// substitution attacks.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/packages/agent/internal/channel"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// MaxBodyBytes caps the size of unary request and response bodies the
// proxy will materialize in memory. 10 MiB is comfortable for any
// regular K8s payload (a Pod with status sub-resource is ~5-15 KiB; a
// list of 1000 pods is ~3-5 MiB) while preventing OOM if a misbehaving
// caller streams something huge through.
const MaxBodyBytes = 10 * 1024 * 1024

// KubeAPIProxy executes KubeProxyRequest against the local apiserver.
type KubeAPIProxy struct {
	transport http.RoundTripper
	baseURL   string
}

// New builds a KubeAPIProxy from a rest.Config — typically
// rest.InClusterConfig() so the agent runs against its own cluster.
func New(cfg *rest.Config) (*KubeAPIProxy, error) {
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube proxy: build transport: %w", err)
	}
	return &KubeAPIProxy{
		transport: transport,
		baseURL:   strings.TrimRight(cfg.Host, "/"),
	}, nil
}

// HandleRequest executes a unary kube call and returns a
// KubeProxyResponse. HTTP-level errors (4xx / 5xx) are NOT errors here
// — they ride back as StatusCode + Body; the Error field is reserved
// for network / serialization failures the backend can't infer
// otherwise.
func (p *KubeAPIProxy) HandleRequest(ctx context.Context, req *agentv2.KubeProxyRequest) *agentv2.KubeProxyResponse {
	if int64(len(req.GetBody())) > MaxBodyBytes {
		return &agentv2.KubeProxyResponse{Error: fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes)}
	}

	httpReq, err := p.buildRequest(ctx, req)
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: err.Error()}
	}

	resp, err := p.transport.RoundTrip(httpReq)
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: fmt.Sprintf("read body: %v", err)}
	}
	if int64(len(body)) > MaxBodyBytes {
		return &agentv2.KubeProxyResponse{Error: fmt.Sprintf("response body exceeds %d bytes", MaxBodyBytes)}
	}

	return &agentv2.KubeProxyResponse{
		StatusCode: uint32(resp.StatusCode),
		Headers:    flattenHeaders(resp.Header),
		Body:       body,
	}
}

// HandleUpgrade performs a protocol upgrade against the local
// apiserver (SPDY/3.1 today, WebSocket on K8s 1.30+). On success
// returns the upgraded io.ReadWriteCloser — Go's net/http transport
// hands back the live conn as resp.Body for any 101 Switching
// Protocols response (since Go 1.12).
//
// The caller is responsible for Close()-ing the conn. On non-101 OR
// transport-level failure the conn is nil and resp carries the
// failure shape — backend's awaitTunnelHandshake forwards non-101
// as a regular *http.Response, so the caller's SPDY library sees
// the upgrade failure on its normal path.
func (p *KubeAPIProxy) HandleUpgrade(ctx context.Context, req *agentv2.KubeProxyRequest) (resp *agentv2.KubeProxyResponse, conn io.ReadWriteCloser) {
	httpReq, err := p.buildRequest(ctx, req)
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: err.Error()}, nil
	}

	httpResp, err := p.transport.RoundTrip(httpReq)
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: err.Error()}, nil
	}

	if httpResp.StatusCode != 101 {
		// Non-101: surface the apiserver's response as-is. Body capped
		// at MaxBodyBytes — same shape as a unary response so client-go
		// on the backend handles the failure on its normal error path.
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, MaxBodyBytes))
		_ = httpResp.Body.Close()
		return &agentv2.KubeProxyResponse{
			StatusCode: uint32(httpResp.StatusCode),
			Headers:    flattenHeaders(httpResp.Header),
			Body:       body,
		}, nil
	}

	// 101 Switching Protocols. Go's transport guarantees Body is a
	// live ReadWriteCloser (since Go 1.12) but the static type is
	// only io.ReadCloser — type-assert. If the assertion fails we
	// can't tunnel writes (very unlikely in practice; would mean
	// some transport wrapper stripped the WriteCloser interface).
	rwc, ok := httpResp.Body.(io.ReadWriteCloser)
	if !ok {
		_ = httpResp.Body.Close()
		return &agentv2.KubeProxyResponse{
			Error: "agent proxy: 101 response body is not a ReadWriteCloser — transport stripped Write?",
		}, nil
	}
	return &agentv2.KubeProxyResponse{
		StatusCode: 101,
		Headers:    flattenHeaders(httpResp.Header),
	}, rwc
}

// HandleWatch opens a watch stream against the apiserver and returns a
// channel that emits each event as a KubeProxyWatchEvent until the
// stream ends (apiserver closes the connection) or ctx is cancelled.
//
// The wire format K8s ships on a watch endpoint is newline-delimited
// JSON of shape {"type": "...", "object": <raw>}; we decode incrementally
// and emit one KubeProxyWatchEvent per line. The raw object bytes pass
// through unchanged — the backend's client-go reflector parses it.
func (p *KubeAPIProxy) HandleWatch(ctx context.Context, req *agentv2.KubeProxyRequest) (<-chan *agentv2.KubeProxyWatchEvent, error) {
	httpReq, err := p.buildRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, err := p.transport.RoundTrip(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("watch HTTP %d: %s", resp.StatusCode, string(body))
	}

	out := make(chan *agentv2.KubeProxyWatchEvent, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			var raw struct {
				Type   string          `json:"type"`
				Object json.RawMessage `json:"object"`
			}
			if err := dec.Decode(&raw); err != nil {
				return // EOF or parse error; either way the stream is done
			}
			ev := &agentv2.KubeProxyWatchEvent{
				EventType: raw.Type,
				Object:    raw.Object,
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// buildRequest constructs the http.Request the proxy sends to the
// apiserver.
//
// Header filtering: any "Authorization" the backend supplied is stripped
// — the agent uses ITS OWN SA token (carried by the transport built from
// rest.Config). Allowing the backend's Authorization through would
// invert the trust model: the apiserver would see the backend's
// credential rather than the agent's, opening a path for the backend to
// impersonate other principals if it ever became compromised.
//
// Hop-by-hop headers (Connection / Transfer-Encoding / Content-Length)
// are stripped because http.NewRequest synthesizes its own — EXCEPT
// for upgrade requests (Sprint A.5 §0.7), where Connection: Upgrade
// + Upgrade: SPDY/3.1|websocket are exactly what the apiserver needs
// to enter the protocol-switch state. Without the exception the
// apiserver responds 400 Bad Request because it sees `Upgrade: ...`
// in isolation (no companion Connection token). Pinned by the
// terminal/portforward smoke test.
func (p *KubeAPIProxy) buildRequest(ctx context.Context, req *agentv2.KubeProxyRequest) (*http.Request, error) {
	method := req.GetMethod()
	if method == "" {
		method = "GET"
	}
	url := p.baseURL + req.GetPath()
	var body io.Reader
	if len(req.GetBody()) > 0 {
		body = bytes.NewReader(req.GetBody())
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	upgradeAttempt := isUpgradeRequest(req)
	for k, v := range req.GetHeaders() {
		if isStrippedHeader(k) {
			if upgradeAttempt && (strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Upgrade")) {
				httpReq.Header.Set(k, v)
			}
			continue
		}
		httpReq.Header.Set(k, v)
	}
	return httpReq, nil
}

func isStrippedHeader(k string) bool {
	switch strings.ToLower(k) {
	case "authorization",
		"host",
		"connection",
		"transfer-encoding",
		"content-length":
		return true
	}
	return false
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

// ─── channel.Handler adapter ─────────────────────────────────────────

// Handler is the channel.Handler implementation that delegates
// kube_request dispatch to the KubeAPIProxy. Other Handler methods are
// no-ops; the agent's metrics + heartbeat code keeps living in the
// shipper / channel layer.
//
// Tunnel sessions (Sprint A.5 §0.7-§0.9) are tracked in the
// tunnels map so subsequent KubeStreamData / KubeStreamAck messages
// for the same request_id route to the right session.
type Handler struct {
	proxy *KubeAPIProxy

	// TunnelWindowBytes overrides DefaultTunnelWindowBytes for the
	// agent's send-side credit window. 0 means use the default.
	// Operators can tune via env var (KUBEBOLT_AGENT_TUNNEL_WINDOW_BYTES,
	// see Sprint A.5 §0.9).
	TunnelWindowBytes uint64

	tunnelsMu sync.RWMutex
	tunnels   map[string]*tunnelSession
}

// NewHandler wires a KubeAPIProxy as a channel.Handler.
func NewHandler(p *KubeAPIProxy) *Handler {
	return &Handler{
		proxy:   p,
		tunnels: make(map[string]*tunnelSession),
	}
}

func (h *Handler) HandleHeartbeatAck(*agentv2.HeartbeatAck)     {}
func (h *Handler) HandleConfigUpdate(*agentv2.ConfigUpdate)     {}
func (h *Handler) HandleDisconnect(*agentv2.Disconnect) error   { return nil }

// HandleKubeStreamData routes a backend→agent KubeStreamData payload
// to the matching tunnel session. Called from the channel.Client's
// read loop — MUST return quickly. If the session's inbound buffer
// is saturated we close the session (bytes can't be silently
// dropped for exec). Stale messages (session already gone) are
// dropped.
func (h *Handler) HandleKubeStreamData(requestID string, data *agentv2.KubeStreamData) {
	h.tunnelsMu.RLock()
	sess, ok := h.tunnels[requestID]
	h.tunnelsMu.RUnlock()
	if !ok {
		return
	}
	select {
	case sess.inbound <- data:
	case <-sess.done:
	default:
		slog.Warn("agent proxy tunnel: inbound saturated, terminating session",
			slog.String("request_id", requestID))
		sess.close()
	}
}

// HandleKubeStreamAck routes a backend→agent KubeStreamAck (credit
// refund) to the matching tunnel session.
func (h *Handler) HandleKubeStreamAck(requestID string, ack *agentv2.KubeStreamAck) {
	h.tunnelsMu.RLock()
	sess, ok := h.tunnels[requestID]
	h.tunnelsMu.RUnlock()
	if !ok {
		return
	}
	select {
	case sess.acks <- ack.GetBytesConsumed():
	case <-sess.done:
	default:
		// ack chan is small (32). Saturated = backend bursting ACKs
		// faster than we send. Dropping is OK — the next ACK will
		// carry up-to-date totals (semantics are bytes-since-last-ack
		// and we accumulate locally).
	}
}

// HandleKubeRequest is invoked by the channel.Client in a fresh
// goroutine for every BackendMessage_KubeRequest. Replies travel back
// through client.Send.
//
// Lifecycle:
//   - Unary: one HandleRequest call, one KubeProxyResponse send. Done.
//   - Watch: HandleWatch opens an event chan; for each event we send
//     KubeProxyWatchEvent. When the apiserver closes the stream OR
//     ctx is cancelled (Run() exited), we send StreamClosed as the
//     terminal marker so the backend's transport can clean up its
//     pending entry without waiting for a timeout.
//
// Send errors after the first one drop the rest of the watch on the
// floor — the stream is already broken; logging once is enough.
func (h *Handler) HandleKubeRequest(ctx context.Context, client *channel.Client, requestID string, req *agentv2.KubeProxyRequest) {
	if req.GetWatch() {
		h.handleWatch(ctx, client, requestID, req)
		return
	}
	if isUpgradeRequest(req) {
		h.handleUpgrade(ctx, client, requestID, req)
		return
	}
	resp := h.proxy.HandleRequest(ctx, req)
	if err := client.Send(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind:      &agentv2.AgentMessage_KubeResponse{KubeResponse: resp},
	}); err != nil {
		slog.Warn("agent proxy: send kube_response failed",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()))
	}
}

// handleUpgrade orchestrates a tunnel session for a SPDY/WebSocket
// upgrade request:
//
//   1. HandleUpgrade dials the apiserver and performs the upgrade.
//   2. Send the response back: 101 → bytes phase begins; non-101 →
//      forward as a regular response and exit.
//   3. Register the session in `h.tunnels` so subsequent
//      KubeStreamData / KubeStreamAck route to it.
//   4. Run the bidi pump until either side closes.
//   5. Send a final StreamClosed so the backend's TunnelConn / slot
//      cleans up promptly instead of waiting for a timeout.
func (h *Handler) handleUpgrade(ctx context.Context, client *channel.Client, requestID string, req *agentv2.KubeProxyRequest) {
	sess := newTunnelSession(requestID)

	// Register early so any KubeStreamData that races the response
	// (it shouldn't — backend waits for 101 first — but defending
	// against reorder via fast network is cheap) finds the session.
	h.tunnelsMu.Lock()
	h.tunnels[requestID] = sess
	h.tunnelsMu.Unlock()
	defer func() {
		h.tunnelsMu.Lock()
		delete(h.tunnels, requestID)
		h.tunnelsMu.Unlock()
		sess.close()
	}()

	resp, conn := h.proxy.HandleUpgrade(ctx, req)

	// Always send the response (101 or non-101 or error). Backend's
	// awaitTunnelHandshake matches on this first message.
	if err := client.Send(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind:      &agentv2.AgentMessage_KubeResponse{KubeResponse: resp},
	}); err != nil {
		slog.Warn("agent proxy upgrade: send handshake reply failed",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()))
		if conn != nil {
			_ = conn.Close()
		}
		return
	}

	if resp.GetStatusCode() != 101 || conn == nil {
		// Upgrade rejected by apiserver (auth, RBAC, pod gone, etc.)
		// or transport-level error. Backend forwards the response
		// shape to client-go's SPDY library — nothing more to do here.
		return
	}
	defer func() { _ = conn.Close() }()

	window := h.TunnelWindowBytes
	if window == 0 {
		window = DefaultTunnelWindowBytes
	}
	sess.run(ctx, conn, client, window)

	// Final terminator so the backend's TunnelConn slot cleans up
	// even if neither pump emitted a KubeStreamData{eof:true} (e.g.
	// agent ctx cancelled before any byte flowed).
	reason := "agent_tunnel_ended"
	if ctx.Err() != nil {
		reason = "agent_ctx_cancelled"
	}
	_ = client.Send(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind: &agentv2.AgentMessage_StreamClosed{
			StreamClosed: &agentv2.StreamClosed{Reason: reason},
		},
	})
}

func (h *Handler) handleWatch(ctx context.Context, client *channel.Client, requestID string, req *agentv2.KubeProxyRequest) {
	events, err := h.proxy.HandleWatch(ctx, req)
	if err != nil {
		_ = client.Send(&agentv2.AgentMessage{
			RequestId: requestID,
			Kind: &agentv2.AgentMessage_KubeResponse{
				KubeResponse: &agentv2.KubeProxyResponse{Error: err.Error()},
			},
		})
		return
	}
	for ev := range events {
		if err := client.Send(&agentv2.AgentMessage{
			RequestId: requestID,
			Kind:      &agentv2.AgentMessage_KubeEvent{KubeEvent: ev},
		}); err != nil {
			slog.Warn("agent proxy: send watch event failed",
				slog.String("request_id", requestID),
				slog.String("error", err.Error()))
			return
		}
	}
	// Stream ended naturally (apiserver closed the body) OR ctx was
	// cancelled. Either way, signal terminal to the backend so its
	// transport.Multiplexor releases the pending slot.
	reason := "agent_watch_ended"
	if ctx.Err() != nil {
		reason = "agent_ctx_cancelled"
	}
	_ = client.Send(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind: &agentv2.AgentMessage_StreamClosed{
			StreamClosed: &agentv2.StreamClosed{Reason: reason},
		},
	})
}
