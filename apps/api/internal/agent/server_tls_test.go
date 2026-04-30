package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// startServer spins up the AgentIngest service over a real localhost
// listener (port 0 → kernel-assigned). Required for TLS — bufconn does
// not negotiate TLS handshakes.
func startServer(t *testing.T, opts ListenOptions) (addr string, stop func()) {
	t.Helper()
	if opts.Auth.Enforcement == "" {
		opts.Auth.Enforcement = EnforcementDisabled
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(UnaryAuthInterceptor(opts.Auth)),
		grpc.StreamInterceptor(StreamAuthInterceptor(opts.Auth)),
	}
	if opts.TLS != nil && opts.TLS.Config != nil {
		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(opts.TLS.Config)))
	}
	srv := grpc.NewServer(serverOpts...)
	agentv2.RegisterAgentChannelServer(srv, NewServer(&captureWriter{}))
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop(); _ = lis.Close() }
}

// clientTLSConfig builds a *tls.Config that trusts caPath as the server
// CA and (when clientCert != nil) presents the supplied client cert.
func clientTLSConfig(t *testing.T, caPath string, clientCert *testCertHandle) *tls.Config {
	t.Helper()
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("client trust pool empty")
	}
	cfg := &tls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS12,
	}
	if clientCert != nil {
		cert, err := tls.LoadX509KeyPair(clientCert.certPath, clientCert.keyPath)
		if err != nil {
			t.Fatalf("load client cert: %v", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg
}

func TestServer_TLS_ServerOnly_AcceptsTLSClient(t *testing.T) {
	server := genTestCert(t, "server", testCertOpts{})
	tlsCfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, "", false)
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := startServer(t, ListenOptions{TLS: tlsCfg})
	defer stop()

	creds := credentials.NewTLS(clientTLSConfig(t, server.certPath, nil))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := helloAndWait(ctx, conn, "tls-node"); err != nil {
		t.Fatalf("handshake over TLS: %v", err)
	}
}

func TestServer_TLS_RejectsPlaintextClient(t *testing.T) {
	server := genTestCert(t, "server", testCertOpts{})
	tlsCfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, "", false)
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := startServer(t, ListenOptions{TLS: tlsCfg})
	defer stop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// gRPC.NewClient is lazy. The error surfaces on the first RPC.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := helloAndWait(ctx, conn, "plaintext"); err == nil {
		t.Error("plaintext client must be rejected by TLS server")
	}
}

func TestServer_MTLS_RequiredButClientHasNoCert(t *testing.T) {
	ca := genTestCert(t, "ca", testCertOpts{isCA: true})
	server := genTestCert(t, "server", testCertOpts{parent: ca})
	tlsCfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, ca.certPath, true)
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := startServer(t, ListenOptions{TLS: tlsCfg})
	defer stop()

	// Trust the CA so handshake proceeds, but don't present a client cert.
	creds := credentials.NewTLS(clientTLSConfig(t, ca.certPath, nil))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := helloAndWait(ctx, conn, "no-client-cert"); err == nil {
		t.Error("mTLS server must reject client without cert")
	}
}

func TestServer_MTLS_AcceptsClientWithCASignedCert(t *testing.T) {
	ca := genTestCert(t, "ca", testCertOpts{isCA: true})
	server := genTestCert(t, "server", testCertOpts{parent: ca})
	clientCert := genTestCert(t, "client", testCertOpts{parent: ca, clientUse: true})

	tlsCfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, ca.certPath, true)
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := startServer(t, ListenOptions{TLS: tlsCfg})
	defer stop()

	creds := credentials.NewTLS(clientTLSConfig(t, ca.certPath, clientCert))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := helloAndWait(ctx, conn, "mtls-node"); err != nil {
		t.Fatalf("handshake over mTLS: %v", err)
	}
}

func TestServer_MTLS_RejectsClientWithUnknownCA(t *testing.T) {
	// Server trusts ca-A, client presents cert signed by ca-B.
	caA := genTestCert(t, "caA", testCertOpts{isCA: true})
	caB := genTestCert(t, "caB", testCertOpts{isCA: true})
	server := genTestCert(t, "server", testCertOpts{parent: caA})
	clientCert := genTestCert(t, "client", testCertOpts{parent: caB, clientUse: true})

	tlsCfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, caA.certPath, true)
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := startServer(t, ListenOptions{TLS: tlsCfg})
	defer stop()

	// Client trusts caA (server cert) and presents caB-signed cert.
	creds := credentials.NewTLS(clientTLSConfig(t, caA.certPath, clientCert))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := helloAndWait(ctx, conn, "wrong-ca"); err == nil {
		t.Error("server must reject client cert signed by an untrusted CA")
	}
}
