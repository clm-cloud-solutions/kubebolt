package flows

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestHubbleClient_MTLSHandshake stands up a real gRPC server
// implementing the Observer API, wrapped in TLS with client-cert
// verification. The HubbleClient is pointed at it with full mTLS env
// vars so the exercised code path is identical to a production Cilium
// install that demands mTLS — except for running in-process.
//
// Verifies:
//   - Credentials built by buildRelayCredentials() successfully
//     complete a TLS handshake against a server that requires client
//     authentication.
//   - ServerStatus succeeds (proving the connection is live past the
//     handshake).
//   - Stream() delivers flows end-to-end.
//
// Not verified here: that the wire format the real Cilium relay speaks
// is exactly what our proto-bindings expect — but that's a concern of
// the proto version pinning in go.mod, not of this client's transport
// layer.
func TestHubbleClient_MTLSHandshake(t *testing.T) {
	ca := newTestCert(t, "mtls-test-ca", nil)
	server := newTestCert(t, "hubble-relay.test", &ca)
	client := newTestCert(t, "kubebolt-agent", &ca)

	// Server-side TLS config: require the client to present a cert
	// signed by our test CA, the same way Cilium's Hubble Relay does
	// when mTLS is on.
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(ca.cert)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{server.cert.Raw},
			PrivateKey:  server.key,
		}},
		ClientCAs:  clientCAs,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}

	// Listen on 127.0.0.1:0 — the kernel picks a free port and returns
	// it, so the test can run alongside any other Hubble / gRPC server.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	observerpb.RegisterObserverServer(grpcServer, &mockObserver{
		flowsToSend: []*flowpb.Flow{
			// One forwarded pod-to-pod flow — minimum the aggregator
			// accepts (has source + destination + pod names).
			testFlow("demo", "demo-load-1", "demo", "demo-web-1", flowpb.Verdict_FORWARDED),
		},
	})
	go func() {
		// Blocks until the listener closes; cleanup below triggers
		// that path.
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.GracefulStop)

	// Client config: point env vars at the fixture certs we just
	// generated, then let buildRelayCredentials() do its job. Writing
	// PEMs to tempfiles mirrors the real pod setup (Secret mount).
	dir := t.TempDir()
	caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
	certPath := writePEM(t, dir, "client.crt", client.certPEM)
	keyPath := writePEM(t, dir, "client.key", client.keyPEM)
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", caPath)
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_CERT_FILE", certPath)
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_KEY_FILE", keyPath)
	// Server cert's CN is "hubble-relay.test"; override the verify
	// hostname because we dial 127.0.0.1:N, not that name.
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_SERVER_NAME", "hubble-relay.test")

	hc, err := NewHubble(lis.Addr().String())
	if err != nil {
		t.Fatalf("NewHubble: %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })

	// ServerStatus is a cheap round-trip that forces the TLS handshake
	// to happen — NewHubble itself is lazy with grpc.NewClient.
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := hc.Ping(pingCtx); err != nil {
		t.Fatalf("Ping (mTLS handshake) failed: %v", err)
	}

	// Stream one flow to prove the bidirectional pipe works past the
	// handshake.
	streamCtx, streamCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer streamCancel()
	flowsCh := make(chan *flowpb.Flow, 4)
	streamErr := make(chan error, 1)
	go func() { streamErr <- hc.Stream(streamCtx, flowsCh) }()

	select {
	case got := <-flowsCh:
		if got == nil {
			t.Fatal("received nil flow")
		}
		if src := got.GetSource().GetPodName(); src != "demo-load-1" {
			t.Fatalf("expected source pod demo-load-1, got %q", src)
		}
	case err := <-streamErr:
		t.Fatalf("stream errored before delivering a flow: %v", err)
	case <-streamCtx.Done():
		t.Fatalf("timed out waiting for flow: %v", streamCtx.Err())
	}
}

// TestHubbleClient_MTLSHandshake_WrongCA verifies that presenting a
// client cert signed by an unrelated CA is rejected by the server —
// i.e. that our TLS config actually enforces authentication rather
// than silently accepting anyone with any cert.
func TestHubbleClient_MTLSHandshake_WrongCA(t *testing.T) {
	trustedCA := newTestCert(t, "trusted-ca", nil)
	server := newTestCert(t, "hubble-relay.test", &trustedCA)
	strangerCA := newTestCert(t, "stranger-ca", nil)
	strangerClient := newTestCert(t, "impostor", &strangerCA)

	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(trustedCA.cert)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{server.cert.Raw},
			PrivateKey:  server.key,
		}},
		ClientCAs:  clientCAs,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	observerpb.RegisterObserverServer(grpcServer, &mockObserver{})
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.GracefulStop)

	dir := t.TempDir()
	// Client trusts the correct server CA but presents a cert the
	// server doesn't know about — server must reject during handshake.
	caPath := writePEM(t, dir, "ca.pem", trustedCA.certPEM)
	certPath := writePEM(t, dir, "client.crt", strangerClient.certPEM)
	keyPath := writePEM(t, dir, "client.key", strangerClient.keyPEM)
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", caPath)
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_CERT_FILE", certPath)
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_KEY_FILE", keyPath)
	t.Setenv("KUBEBOLT_HUBBLE_RELAY_SERVER_NAME", "hubble-relay.test")

	hc, err := NewHubble(lis.Addr().String())
	if err != nil {
		t.Fatalf("NewHubble: %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })

	// Handshake happens inside Ping; it must fail with a TLS-layer
	// error. We don't assert on the exact error text because the
	// server vs client side of an mTLS rejection phrases it
	// differently (connection reset vs unknown authority) across
	// Go versions.
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := hc.Ping(pingCtx); err == nil {
		t.Fatal("expected handshake to be rejected by server, got success")
	}
}

// mockObserver is the minimum slice of observerpb.ObserverServer our
// client exercises: ServerStatus for liveness and GetFlows for stream.
// Everything else returns Unimplemented which is fine — the real
// HubbleClient never calls those methods.
type mockObserver struct {
	observerpb.UnimplementedObserverServer
	flowsToSend []*flowpb.Flow
}

func (m *mockObserver) ServerStatus(ctx context.Context, req *observerpb.ServerStatusRequest) (*observerpb.ServerStatusResponse, error) {
	return &observerpb.ServerStatusResponse{
		Version:  "hubble-relay mock-1.0",
		NumFlows: uint64(len(m.flowsToSend)),
	}, nil
}

func (m *mockObserver) GetFlows(req *observerpb.GetFlowsRequest, stream observerpb.Observer_GetFlowsServer) error {
	for _, f := range m.flowsToSend {
		if err := stream.Send(&observerpb.GetFlowsResponse{
			ResponseTypes: &observerpb.GetFlowsResponse_Flow{Flow: f},
			Time:          timestamppb.Now(),
		}); err != nil {
			return err
		}
	}
	// Hold the stream open until the client disconnects. Real Hubble
	// Relay behaves this way with Follow=true; closing early would
	// make our client retry with backoff and obscure the test intent.
	<-stream.Context().Done()
	return nil
}

// testFlow builds a minimal Flow event that passes the aggregator's
// "pod-to-pod + EGRESS + not reply" filter. Only what's needed for
// the client to demonstrate end-to-end delivery.
func testFlow(srcNs, srcPod, dstNs, dstPod string, verdict flowpb.Verdict) *flowpb.Flow {
	return &flowpb.Flow{
		Verdict:          verdict,
		TrafficDirection: flowpb.TrafficDirection_EGRESS,
		Source:           &flowpb.Endpoint{Namespace: srcNs, PodName: srcPod},
		Destination:      &flowpb.Endpoint{Namespace: dstNs, PodName: dstPod},
	}
}
