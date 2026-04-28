package cluster

import (
	"fmt"
	"net"
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

// RoundTrip is just the AgentProxyTransport's RoundTrip — agent-proxy
// already detects upgrade requests in commit 8d. The 101 response
// carries TunnelConn as Body.
func (u *agentProxyUpgrader) RoundTrip(req *http.Request) (*http.Response, error) {
	return u.transport.RoundTrip(req)
}

// NewConnection wraps the tunnel as a SPDY-framed httpstream.Connection.
// The upper layer (remotecommand executor, portforward) treats this
// like any other SPDY connection — multiplexes stdin/stdout/stderr/error
// streams over it, just like it would over a direct apiserver SPDY
// conn. The bytes flowing through the SPDY framing layer are tunneled
// over our gRPC channel transparently.
func (u *agentProxyUpgrader) NewConnection(resp *http.Response) (httpstream.Connection, error) {
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("agent-proxy SPDY: expected 101 Switching Protocols, got %d", resp.StatusCode)
	}
	conn, ok := resp.Body.(net.Conn)
	if !ok {
		return nil, fmt.Errorf("agent-proxy SPDY: response body is %T, want net.Conn", resp.Body)
	}
	return utilspdy.NewClientConnection(conn)
}
