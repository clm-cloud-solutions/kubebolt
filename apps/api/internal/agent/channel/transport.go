package channel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// ErrAgentNotConnected is returned by RoundTrip when no agent is
// registered for the target cluster_id. Callers (typically a
// cluster.Manager wrapping a client-go REST client) translate this
// into a 503 for the user-facing API.
var ErrAgentNotConnected = errors.New("channel: no agent connected for cluster")

// AgentProxyTransport implements http.RoundTripper by tunneling each
// HTTP request through the bidi gRPC channel of a connected kubebolt
// agent. client-go inside the backend (informers, dynamic clients,
// REST helpers) treats this as a normal Transport — the request goes
// in as *http.Request, the response comes out as *http.Response, and
// the on-the-wire bytes are framed inside KubeProxyRequest /
// KubeProxyResponse / KubeProxyWatchEvent / KubeStreamData messages.
//
// Lookup is dynamic: every RoundTrip resolves the current Agent for
// ClusterID via the registry. That way a reconnect mid-RoundTrip just
// means the next call hits the fresh agent, with no transport-level
// reconfiguration. In-flight calls fail with ErrAgentClosed when the
// owning agent disconnects (Agent.Close cancels the Multiplexor slot).
//
// Three flow modes (Sprint A.5):
//
//   - Unary (REST): block on a single KubeProxyResponse.
//   - Watch (?watch=true): server-driven NDJSON pipe over
//     KubeProxyWatchEvent messages.
//   - Tunnel (Connection: Upgrade header): SPDY/WebSocket bytes
//     tunneled via KubeStreamData with credit-based flow control on
//     KubeStreamAck (commit 8d, §0.7-§0.9).
type AgentProxyTransport struct {
	ClusterID string
	Registry  *AgentRegistry

	// DefaultTimeout bounds non-watch RoundTrips when the caller's
	// request context has no deadline. 0 means unbounded — discouraged
	// outside tests because a hung agent would leak request_ids
	// forever. NewAgentProxyTransport sets a sensible default.
	DefaultTimeout time.Duration

	// TunnelWindowBytes overrides DefaultTunnelWindowBytes for credit
	// flow control on tunnel sessions opened via this transport. 0
	// means use the default (256 KiB). Set per-transport so different
	// clusters can run different windows when one of them is on a
	// fat pipe and another on a constrained one.
	TunnelWindowBytes uint64
}

// DefaultProxyTimeout is the unary fall-back used when neither the
// caller's context nor an explicit DefaultTimeout bounds the call.
// Matches the 30s rest.Config.Timeout the manager uses for local
// clusters so behavior is symmetric across access modes.
const DefaultProxyTimeout = 30 * time.Second

// NewAgentProxyTransport returns a transport ready to use with
// rest.Config{Transport: t}. Setting DefaultTimeout=0 keeps the value
// unbounded for tests; production callers should rely on the default.
func NewAgentProxyTransport(clusterID string, registry *AgentRegistry) *AgentProxyTransport {
	return &AgentProxyTransport{
		ClusterID:      clusterID,
		Registry:       registry,
		DefaultTimeout: DefaultProxyTimeout,
	}
}

// stripRequestHeaders names headers that MUST NOT travel from the
// backend's HTTP request to the remote apiserver. The agent presents
// its OWN ServiceAccount credentials when calling its in-cluster
// apiserver; any Authorization the backend would inject (from an
// admin kubeconfig, BYO token, etc.) is at best useless and at worst
// muddies the apiserver's audit log. Hop-by-hop headers per RFC 7230
// §6.1 are stripped for the same reason a normal reverse proxy
// strips them.
//
// EXCEPTION: for upgrade requests (Connection: Upgrade + Upgrade:
// SPDY/3.1|websocket), Connection AND Upgrade MUST be forwarded —
// they carry the protocol the agent will negotiate against its own
// apiserver. Caller decides via the isUpgrade param to flattenRequestHeaders.
var stripRequestHeaders = []string{
	"Authorization",
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// upgradeAllowedHeaders are the strip-list members that we forward
// anyway when the request is itself an upgrade attempt.
var upgradeAllowedHeaders = map[string]bool{
	"Connection": true,
	"Upgrade":    true,
}

// CancelRequest implements the legacy http.Transport-shaped
// canceler interface that client-go's tryCancelRequest probes via
// type assertion. Without this, every cancellation logs a noisy
// `Warning: unable to cancel request roundTripperType="*channel.AgentProxyTransport"`.
//
// The actual cancellation already happens via the request's
// context.Context — RoundTrip selects on req.Context().Done() and
// frees the Multiplexor slot, so no per-request bookkeeping is
// needed here. This is a no-op specifically to satisfy the
// interface and silence the warning.
func (t *AgentProxyTransport) CancelRequest(*http.Request) {}

// RoundTrip executes one HTTP request through the agent. Watch URLs
// (`?watch=true`) return immediately with an *http.Response whose
// Body is a server-driven NDJSON pipe; client-go's StreamWatcher
// consumes that exactly as if it had hit the apiserver directly.
// Unary calls block until either a KubeProxyResponse arrives, the
// caller's context expires, or the owning agent disconnects.
func (t *AgentProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Registry == nil {
		return nil, errors.New("agent-proxy: registry is nil")
	}
	agent := t.Registry.Get(t.ClusterID)
	if agent == nil {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotConnected, t.ClusterID)
	}

	isWatch := strings.EqualFold(req.URL.Query().Get("watch"), "true")
	isUpgrade := isUpgradeRequest(req)

	body, err := drainBody(req)
	if err != nil {
		return nil, fmt.Errorf("agent-proxy: read body: %w", err)
	}

	requestID := uuid.NewString()
	mode := SlotUnary
	switch {
	case isUpgrade:
		mode = SlotTunnel
	case isWatch:
		mode = SlotWatch
	}
	replies, cancel, err := agent.Pending.Register(requestID, mode)
	if err != nil {
		return nil, err
	}

	kubeReq := &agentv2.KubeProxyRequest{
		Method:         req.Method,
		Path:           req.URL.RequestURI(),
		Headers:        flattenRequestHeaders(req.Header, isUpgrade),
		Body:           body,
		Watch:          isWatch,
		TimeoutSeconds: timeoutSecondsFor(req, t.DefaultTimeout, isWatch || isUpgrade),
	}
	backendMsg := &agentv2.BackendMessage{
		RequestId: requestID,
		Kind:      &agentv2.BackendMessage_KubeRequest{KubeRequest: kubeReq},
	}

	if err := agent.Send(backendMsg); err != nil {
		cancel()
		return nil, err
	}

	if isUpgrade {
		// Block on the first reply: must be the 101 Switching Protocols
		// handshake from the agent. Anything else is a protocol error
		// (e.g. apiserver returned 403 during upgrade) and surfaces
		// as a regular non-101 *http.Response.
		return t.awaitTunnelHandshake(req, requestID, replies, cancel, agent)
	}

	if isWatch {
		return buildWatchResponse(req, replies, cancel, agent.Closed()), nil
	}

	ctx := req.Context()
	var (
		timer    *time.Timer
		timeoutC <-chan time.Time
	)
	if _, ok := ctx.Deadline(); !ok && t.DefaultTimeout > 0 {
		timer = time.NewTimer(t.DefaultTimeout)
		defer timer.Stop()
		timeoutC = timer.C
	}

	select {
	case msg, ok := <-replies:
		if !ok {
			// Slot closed without a reply — agent disconnected mid-flight.
			return nil, ErrAgentClosed
		}
		return responseFromMessage(req, msg)
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timeoutC:
		cancel()
		return nil, fmt.Errorf("agent-proxy: timeout after %s", t.DefaultTimeout)
	case <-agent.Closed():
		return nil, ErrAgentClosed
	}
}

// drainBody consumes req.Body fully so the bytes can be packed into
// the proto payload. Client-go normally hands us Body=nil for GETs
// and a *bytes.Reader for POST/PATCH; either is fine to ReadAll.
func drainBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	defer req.Body.Close()
	return io.ReadAll(req.Body)
}

func flattenRequestHeaders(h http.Header, isUpgrade bool) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if shouldStripHeader(k, isUpgrade) || len(v) == 0 {
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func shouldStripHeader(key string, isUpgrade bool) bool {
	for _, k := range stripRequestHeaders {
		if strings.EqualFold(key, k) {
			if isUpgrade && upgradeAllowedHeaders[http.CanonicalHeaderKey(key)] {
				return false
			}
			return true
		}
	}
	return false
}

// timeoutSecondsFor maps the caller's intent into the proto field the
// agent honors when calling its own apiserver. Watch returns 0 (no
// agent-imposed bound; the apiserver may still apply its own).
func timeoutSecondsFor(req *http.Request, fallback time.Duration, watch bool) uint32 {
	if watch {
		return 0
	}
	if dl, ok := req.Context().Deadline(); ok {
		d := time.Until(dl)
		if d <= 0 {
			return 1
		}
		// Round up so the agent has at least the caller's window.
		return uint32(d.Seconds() + 1)
	}
	if fallback > 0 {
		return uint32(fallback.Seconds())
	}
	return 0
}

// responseFromMessage turns a unary KubeProxyResponse into an
// *http.Response. The proto comment is explicit: HTTP status >= 400
// is NOT a transport-level error — it's a normal response the caller
// will inspect. Only KubeProxyResponse.error (network/serialization
// failure ON THE AGENT) surfaces as a Go error.
func responseFromMessage(req *http.Request, msg *agentv2.AgentMessage) (*http.Response, error) {
	resp := msg.GetKubeResponse()
	if resp == nil {
		return nil, fmt.Errorf("agent-proxy: expected kube_response, got %T", msg.GetKind())
	}
	if resp.GetError() != "" {
		return nil, fmt.Errorf("agent-proxy: %s", resp.GetError())
	}
	body := resp.GetBody()
	return &http.Response{
		Status:        http.StatusText(int(resp.GetStatusCode())),
		StatusCode:    int(resp.GetStatusCode()),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        expandHeaders(resp.GetHeaders()),
		ContentLength: int64(len(body)),
		Body:          io.NopCloser(bytes.NewReader(body)),
		Request:       req,
	}, nil
}

func expandHeaders(in map[string]string) http.Header {
	out := make(http.Header, len(in))
	for k, v := range in {
		out.Set(k, v)
	}
	return out
}

// buildWatchResponse turns the chan of incoming AgentMessages into an
// *http.Response whose Body is a stream of JSON-encoded
// metav1.WatchEvent records — exactly the format client-go's
// StreamWatcher expects when reading from a watch endpoint.
//
// The pipe writer terminates on:
//   - chan close — the Multiplexor auto-cleans on a terminal
//     KubeProxyResponse or StreamClosed.
//   - request context cancel — informer Stop, user disconnect, etc.
//     Cancel is called explicitly so the slot is freed before the
//     terminal message would have arrived.
//   - agent disconnect — Agent.Close → CancelAll → chan close.
func buildWatchResponse(req *http.Request, replies <-chan *agentv2.AgentMessage, cancel func(), agentClosed <-chan struct{}) *http.Response {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		ctx := req.Context()
		for {
			select {
			case msg, ok := <-replies:
				if !ok {
					return
				}
				if ev := msg.GetKubeEvent(); ev != nil {
					if err := writeWatchEvent(pw, ev); err != nil {
						return
					}
					continue
				}
				// kube_response or stream_closed — terminal.
				return
			case <-ctx.Done():
				cancel()
				return
			case <-agentClosed:
				return
			}
		}
	}()
	return &http.Response{
		Status:     http.StatusText(http.StatusOK),
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       pr,
		Request:    req,
	}
}

// isUpgradeRequest reports whether the HTTP request is a protocol
// upgrade attempt — `Connection: Upgrade` plus an `Upgrade:` header
// naming SPDY/3.1 (today) or websocket (K8s 1.30+ via KEP-4006).
//
// k8s.io/apimachinery sometimes lists Upgrade as a value in the
// Connection header (canonical RFC 7230 §6.1) and sometimes as a
// separate `X-Stream-Protocol-Version` header that hints at the
// upgrade target. We normalize by checking BOTH — the existing
// hop-by-hop header strip already preserves the Upgrade header
// because we filter by exact name match in stripRequestHeaders.
func isUpgradeRequest(req *http.Request) bool {
	for _, v := range req.Header.Values("Connection") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return req.Header.Get("Upgrade") != ""
			}
		}
	}
	return false
}

// awaitTunnelHandshake blocks for the agent's reply to an upgrade
// request. The first message MUST be a KubeProxyResponse:
//
//   - status=101 → handshake succeeded; build a TunnelConn-backed
//     *http.Response and return it. From here on, KubeStreamData /
//     KubeStreamAck flow in both directions until either side closes.
//   - status≠101 → upgrade failed at the apiserver (auth denied,
//     pod gone, command rejected, etc.). Surface as a regular
//     *http.Response so the caller can inspect the status code +
//     body — same shape they'd see if they'd dialed the apiserver
//     directly and gotten the same rejection.
//   - StreamClosed (premature) → agent disconnected mid-handshake.
//   - chan closed → ErrAgentClosed.
//   - ctx done → context.Canceled / DeadlineExceeded.
//   - agent.Closed() → ErrAgentClosed.
func (t *AgentProxyTransport) awaitTunnelHandshake(req *http.Request, requestID string, replies <-chan *agentv2.AgentMessage, cancel func(), agent *Agent) (*http.Response, error) {
	ctx := req.Context()
	select {
	case msg, ok := <-replies:
		if !ok {
			return nil, ErrAgentClosed
		}
		if sc := msg.GetStreamClosed(); sc != nil {
			cancel()
			return nil, fmt.Errorf("agent-proxy tunnel: peer closed before handshake (%s)", sc.GetReason())
		}
		resp := msg.GetKubeResponse()
		if resp == nil {
			cancel()
			return nil, fmt.Errorf("agent-proxy tunnel: expected kube_response handshake, got %T", msg.GetKind())
		}
		if resp.GetError() != "" {
			cancel()
			return nil, fmt.Errorf("agent-proxy tunnel handshake: %s", resp.GetError())
		}
		if resp.GetStatusCode() != 101 {
			// Non-101: upgrade failed cleanly at the apiserver.
			// Return as a regular response — the caller's SPDY
			// library will see the failure and report it through its
			// normal error path.
			cancel()
			return responseFromMessage(req, msg)
		}
		// 101 Switching Protocols. Promote to a tunnel-backed Response.
		// Body is wrapped in TunnelHandshakeBody so K8s' spdy.Negotiate
		// `defer resp.Body.Close()` doesn't tear down the tunnel
		// before the SPDY layer's handshake — see the type's doc.
		conn := newTunnelConn(requestID, t.ClusterID, agent, replies, cancel, t.TunnelWindowBytes)
		return &http.Response{
			Status:     "101 Switching Protocols",
			StatusCode: 101,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     expandHeaders(resp.GetHeaders()),
			Body:       NewTunnelHandshakeBody(conn),
			Request:    req,
		}, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-agent.Closed():
		cancel()
		return nil, ErrAgentClosed
	}
}

// writeWatchEvent emits one metav1.WatchEvent JSON object to w.
// Format matches what the apiserver writes:
//
//	{"type":"ADDED","object":<raw>}
//
// The trailing newline is cosmetic — client-go's json.Decoder reads
// sequential objects regardless — but it makes the wire dump
// readable when capturing with tcpdump / kubebolt-debug-flow.
func writeWatchEvent(w io.Writer, ev *agentv2.KubeProxyWatchEvent) error {
	if _, err := fmt.Fprintf(w, `{"type":%q,"object":`, ev.GetEventType()); err != nil {
		return err
	}
	if _, err := w.Write(ev.GetObject()); err != nil {
		return err
	}
	_, err := w.Write([]byte("}\n"))
	return err
}
