// Package flows contains the agent-side Cilium Hubble adapter. This is
// Phase 2.1 Level 2 of the Traffic Observability ladder, but implemented
// as an agent responsibility rather than a backend-side collector so
// KubeBolt's SaaS deployment model works: the agent bridges cluster-internal
// sources (Hubble Relay here, potentially Prometheus / mesh metrics later)
// and pushes normalized samples out over the existing StreamMetrics gRPC
// channel. The customer never exposes Hubble itself.
//
// Only one instance of this collector runs per cluster at a time — see
// leader.go — because Hubble Relay is cluster-wide and having every
// agent pod scrape it would duplicate data.
package flows

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"os"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// HubbleClient is a thin gRPC wrapper around Observer.GetFlows.
type HubbleClient struct {
	addr   string
	conn   *grpc.ClientConn
	client observerpb.ObserverClient
}

// NewHubble dials the relay at addr. Transport credentials are selected
// at runtime based on env vars:
//
//   - KUBEBOLT_HUBBLE_RELAY_CA_FILE set → TLS, verify relay cert
//     against the given CA.
//   - + KUBEBOLT_HUBBLE_RELAY_CERT_FILE and _KEY_FILE → mutual TLS,
//     present our client cert to the relay.
//   - KUBEBOLT_HUBBLE_RELAY_SERVER_NAME overrides SNI / verification
//     hostname (useful when the cert was issued to a name other than
//     the Service DNS we dial).
//   - Nothing set → insecure, which is fine for default Cilium installs
//     where Hubble Relay is reached over an in-cluster Service.
//
// Invalid / unreadable cert files fail fast with an explicit error so
// misconfigurations don't silently fall through to insecure mode.
func NewHubble(addr string) (*HubbleClient, error) {
	creds, err := buildRelayCredentials()
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial hubble relay %s: %w", addr, err)
	}
	return &HubbleClient{
		addr:   addr,
		conn:   conn,
		client: observerpb.NewObserverClient(conn),
	}, nil
}

func buildRelayCredentials() (credentials.TransportCredentials, error) {
	caFile := os.Getenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE")
	certFile := os.Getenv("KUBEBOLT_HUBBLE_RELAY_CERT_FILE")
	keyFile := os.Getenv("KUBEBOLT_HUBBLE_RELAY_KEY_FILE")
	serverName := os.Getenv("KUBEBOLT_HUBBLE_RELAY_SERVER_NAME")

	// No CA file → no TLS. Bare insecure keeps compatibility with
	// vanilla Cilium installs that don't turn on Hubble Relay TLS.
	if caFile == "" {
		slog.Debug("hubble: using insecure transport (no CA configured)")
		return insecure.NewCredentials(), nil
	}

	caPem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read hubble CA %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPem) {
		return nil, fmt.Errorf("hubble CA %s: no valid certs", caFile)
	}

	tlsCfg := &tls.Config{
		RootCAs:    pool,
		ServerName: serverName, // empty is fine — gRPC uses the dial target
	}

	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("hubble mTLS: both CERT_FILE and KEY_FILE must be set")
		}
		clientCert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load hubble client cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{clientCert}
		slog.Info("hubble: mTLS enabled",
			slog.String("ca", caFile),
			slog.String("cert", certFile),
			slog.String("server_name", serverName))
	} else {
		slog.Info("hubble: TLS enabled (server-auth only)",
			slog.String("ca", caFile),
			slog.String("server_name", serverName))
	}

	return credentials.NewTLS(tlsCfg), nil
}

func (h *HubbleClient) Ping(ctx context.Context) (*observerpb.ServerStatusResponse, error) {
	return h.client.ServerStatus(ctx, &observerpb.ServerStatusRequest{})
}

// Stream opens a follow=true GetFlows stream and pushes every flow into
// out. Blocks until ctx is cancelled or the stream errors. The channel is
// never closed by this method — the caller controls its lifecycle.
func (h *HubbleClient) Stream(ctx context.Context, out chan<- *flowpb.Flow) error {
	req := &observerpb.GetFlowsRequest{Follow: true}
	stream, err := h.client.GetFlows(ctx, req)
	if err != nil {
		return fmt.Errorf("open flows stream: %w", err)
	}
	slog.Info("hubble: streaming flows", slog.String("relay", h.addr))
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv flow: %w", err)
		}
		f := resp.GetFlow()
		if f == nil {
			continue
		}
		select {
		case out <- f:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *HubbleClient) Close() error {
	return h.conn.Close()
}
