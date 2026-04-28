package cluster

import (
	"fmt"
	"log/slog"
	"net/http"

	"k8s.io/apimachinery/pkg/util/httpstream"
	utilspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
	clientspdy "k8s.io/client-go/transport/spdy"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// SPDYTransportsFor returns the (http.RoundTripper, spdy.Upgrader) pair
// the dashboard's exec / portforward / file-browser handlers feed into
// remotecommand.NewSPDYExecutorForTransports and
// portforward.NewOnAddresses.
//
// The K8s standard helper, client-go/transport/spdy.RoundTripperFor,
// IGNORES rest.Config.Transport and dials the apiserver directly via
// its own internal http.Transport. That works for kubeconfig clusters
// where rest.Config.Host is a real DNS name; for agent-proxy clusters
// where rest.Config.Host is the synthetic `https://<id>.agent.local`,
// the direct dial fails with "no such host" and the user sees
// terminal "host desconocido", portforward "timed out", file browser
// hanging.
//
// The fix: when rest.Config.Transport is one of our AgentProxyTransport
// instances, return a custom upgrader that:
//
//   - RoundTrip      → forwards via AgentProxyTransport.RoundTrip,
//                      which detects the upgrade headers and returns
//                      a *http.Response{StatusCode: 101, Body: TunnelConn}.
//   - NewConnection  → wraps TunnelConn with K8s' SPDY framing
//                      (utilspdy.NewClientConnection) so the upper
//                      remotecommand / portforward layers see a
//                      standard httpstream.Connection.
//
// For kubeconfig clusters the standard path is preserved unchanged.
func SPDYTransportsFor(cfg *rest.Config) (http.RoundTripper, clientspdy.Upgrader, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("spdy transports: nil rest.Config")
	}
	if at, ok := cfg.Transport.(*channel.AgentProxyTransport); ok {
		u := &agentProxyUpgrader{transport: at}
		return u, u, nil
	}
	return clientspdy.RoundTripperFor(cfg)
}

// agentProxyUpgrader implements both http.RoundTripper and
// client-go's spdy.Upgrader. It plugs the AgentProxyTransport
// into K8s' SPDY framing.
type agentProxyUpgrader struct {
	transport *channel.AgentProxyTransport
}

// RoundTrip adds the Connection: Upgrade + Upgrade: SPDY/3.1
// headers that K8s' standard spdy.SpdyRoundTripper injects
// internally — our AgentProxyTransport detects upgrades by these
// headers (commit 8d), so without them the request would slip
// through as a regular unary POST and the apiserver would reject
// /exec / /attach / /portforward with 400.
//
// We mirror K8s' SpdyRoundTripper.RoundTrip header injection. Clone
// before mutating so we don't touch the caller's req — the Negotiate
// caller may reuse it across protocol attempts.
func (u *agentProxyUpgrader) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Connection", "Upgrade")
	clone.Header.Set("Upgrade", "SPDY/3.1")

	slog.Info("agent-proxy SPDY: RoundTrip start",
		slog.String("url", clone.URL.String()),
		slog.String("method", clone.Method),
		slog.Any("headers", clone.Header),
	)
	resp, err := u.transport.RoundTrip(clone)
	if err != nil {
		slog.Warn("agent-proxy SPDY: RoundTrip failed",
			slog.String("error", err.Error()))
		return nil, err
	}
	slog.Info("agent-proxy SPDY: RoundTrip got response",
		slog.Int("status", resp.StatusCode),
		slog.Any("headers", resp.Header),
	)
	return resp, nil
}

// NewConnection extracts the raw TunnelConn from the response body
// and wraps it in K8s' SPDY framing. Going through Extract() — not a
// direct net.Conn type assertion on resp.Body — is critical because
// K8s' spdy.Negotiate calls `defer resp.Body.Close()` right after
// our return; with a naive type assertion that defer would tear down
// the tunnel before SPDY's handshake. TunnelHandshakeBody.Extract()
// flips the body's Close into a no-op (the SPDY conn now owns the
// conn lifecycle). See TunnelHandshakeBody's doc for full rationale.
func (u *agentProxyUpgrader) NewConnection(resp *http.Response) (httpstream.Connection, error) {
	if resp.StatusCode != http.StatusSwitchingProtocols {
		slog.Warn("agent-proxy SPDY: NewConnection got non-101",
			slog.Int("status", resp.StatusCode))
		return nil, fmt.Errorf("agent-proxy SPDY: expected 101 Switching Protocols, got %d", resp.StatusCode)
	}
	body, ok := resp.Body.(*channel.TunnelHandshakeBody)
	if !ok {
		slog.Warn("agent-proxy SPDY: response body is not TunnelHandshakeBody",
			slog.String("body_type", fmt.Sprintf("%T", resp.Body)))
		return nil, fmt.Errorf("agent-proxy SPDY: response body is %T, want *channel.TunnelHandshakeBody", resp.Body)
	}
	conn := body.Extract()
	slog.Info("agent-proxy SPDY: building SPDY connection over tunnel")
	return utilspdy.NewClientConnection(conn)
}
