package agent

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

// ─── ParseEnforcement ─────────────────────────────────────────────────

func TestParseEnforcement(t *testing.T) {
	cases := []struct {
		in   string
		want AuthEnforcement
		ok   bool
	}{
		{"disabled", EnforcementDisabled, true},
		{"permissive", EnforcementPermissive, true},
		{"enforced", EnforcementEnforced, true},
		{"", EnforcementDisabled, false},
		{"strict", EnforcementDisabled, false},
		{"DISABLED", EnforcementDisabled, false}, // case-sensitive on purpose: env var should be lowercase
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok := ParseEnforcement(c.in)
			if ok != c.ok {
				t.Errorf("ok = %v, want %v", ok, c.ok)
			}
			if got != c.want {
				t.Errorf("enforcement = %s, want %s", got, c.want)
			}
		})
	}
}

// ─── mapAuthError ─────────────────────────────────────────────────────

func TestMapAuthError(t *testing.T) {
	cases := []struct {
		in   error
		code codes.Code
	}{
		{auth.ErrUnknownMode, codes.InvalidArgument},
		{auth.ErrTokenRevoked, codes.PermissionDenied},
		{auth.ErrTenantDisabled, codes.PermissionDenied},
		{auth.ErrMissingToken, codes.Unauthenticated},
		{auth.ErrTokenInvalid, codes.Unauthenticated},
		{auth.ErrTokenMalformed, codes.Unauthenticated},
		{auth.ErrTokenExpired, codes.Unauthenticated},
		{auth.ErrTokenNotFound, codes.Unauthenticated},
		{auth.ErrTLSRequired, codes.Unauthenticated},
		{errors.New("totally unmapped"), codes.Unauthenticated}, // never leak as Internal
	}
	for _, c := range cases {
		st, ok := status.FromError(mapAuthError(c.in))
		if !ok {
			t.Errorf("%v: not a status error", c.in)
			continue
		}
		if st.Code() != c.code {
			t.Errorf("%v: code = %s, want %s", c.in, st.Code(), c.code)
		}
	}
}

// ─── authenticateMD ───────────────────────────────────────────────────

type stubAuther struct {
	mode    auth.AgentAuthMode
	returns *auth.AgentIdentity
	err     error
	called  bool
}

func (s *stubAuther) Mode() auth.AgentAuthMode { return s.mode }
func (s *stubAuther) Authenticate(_ context.Context, _ metadata.MD, _ *peer.Peer) (*auth.AgentIdentity, error) {
	s.called = true
	return s.returns, s.err
}

func TestAuthenticateMD_DisabledShortCircuits(t *testing.T) {
	a := &stubAuther{mode: auth.ModeIngestToken}
	id, err := authenticateMD(context.Background(), AuthConfig{Enforcement: EnforcementDisabled, Authenticator: a})
	if err != nil {
		t.Fatalf("disabled mode should never error, got %v", err)
	}
	if id == nil || id.Mode != auth.ModeDisabled {
		t.Errorf("expected dummy identity Mode=disabled, got %+v", id)
	}
	if a.called {
		t.Error("authenticator must not be called in disabled mode")
	}
}

func TestAuthenticateMD_EnforcedHappyPath(t *testing.T) {
	want := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1"}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: want}
	id, err := authenticateMD(context.Background(), AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id != want {
		t.Errorf("expected forwarded identity, got %+v", id)
	}
}

func TestAuthenticateMD_EnforcedReturnsGRPCStatus(t *testing.T) {
	a := &stubAuther{mode: auth.ModeIngestToken, err: auth.ErrTokenRevoked}
	_, err := authenticateMD(context.Background(), AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %v", err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("code = %s, want PermissionDenied", st.Code())
	}
}

func TestAuthenticateMD_PermissiveAcceptsOnFailure(t *testing.T) {
	a := &stubAuther{mode: auth.ModeIngestToken, err: auth.ErrTokenInvalid}
	id, err := authenticateMD(context.Background(), AuthConfig{Enforcement: EnforcementPermissive, Authenticator: a})
	if err != nil {
		t.Fatalf("permissive must not return error, got %v", err)
	}
	if id == nil || id.Mode != auth.ModeDisabled {
		t.Errorf("expected dummy identity on permissive fallback, got %+v", id)
	}
}

func TestAuthenticateMD_PermissiveKeepsRealIdentityOnSuccess(t *testing.T) {
	want := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1"}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: want}
	id, err := authenticateMD(context.Background(), AuthConfig{Enforcement: EnforcementPermissive, Authenticator: a})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id != want {
		t.Errorf("permissive on success must forward real identity, got %+v", id)
	}
}

func TestAuthenticateMD_NilAuthenticatorInEnforcedFailsLoud(t *testing.T) {
	_, err := authenticateMD(context.Background(), AuthConfig{Enforcement: EnforcementEnforced, Authenticator: nil})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Errorf("nil authenticator in enforced must return Internal, got %v", err)
	}
}

func TestAuthenticateMD_RequireMTLSWithoutCertRejects(t *testing.T) {
	want := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1", TLSVerified: false}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: want}
	cfg := AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a, RequireMTLS: true}
	_, err := authenticateMD(context.Background(), cfg)
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("RequireMTLS without verified cert must return Unauthenticated, got %v", err)
	}
}

func TestAuthenticateMD_RateLimiterDeniesPastBurst(t *testing.T) {
	want := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1"}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: want}
	rl := auth.NewRateLimiter(auth.RateLimitConfig{Enabled: true, RequestsPerSec: 1, Burst: 2})
	cfg := AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a, RateLimiter: rl}

	// Burst (2) succeed.
	for i := 0; i < 2; i++ {
		if _, err := authenticateMD(context.Background(), cfg); err != nil {
			t.Fatalf("burst[%d] should succeed: %v", i, err)
		}
	}
	// Next call denied with ResourceExhausted.
	_, err := authenticateMD(context.Background(), cfg)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %v", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("code = %s, want ResourceExhausted", st.Code())
	}
	if !strings.Contains(st.Message(), "retry after") {
		t.Errorf("message should advertise retry-after, got %q", st.Message())
	}
}

func TestAuthenticateMD_RateLimiterIgnoresEmptyTenant(t *testing.T) {
	// disabled-mode synthetic identity has TenantID="" — the rate
	// limiter must not start denying those.
	rl := auth.NewRateLimiter(auth.RateLimitConfig{Enabled: true, RequestsPerSec: 1, Burst: 1})
	cfg := AuthConfig{Enforcement: EnforcementDisabled, RateLimiter: rl}
	for i := 0; i < 100; i++ {
		if _, err := authenticateMD(context.Background(), cfg); err != nil {
			t.Fatalf("disabled mode + empty tenant must always allow, denied at %d: %v", i, err)
		}
	}
}

func TestAuthenticateMD_RateLimiterDisabledIsNoop(t *testing.T) {
	want := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1"}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: want}
	rl := auth.NewRateLimiter(auth.RateLimitConfig{Enabled: false, RequestsPerSec: 1, Burst: 1})
	cfg := AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a, RateLimiter: rl}
	// Even with tiny limits, disabled limiter must allow everything.
	for i := 0; i < 50; i++ {
		if _, err := authenticateMD(context.Background(), cfg); err != nil {
			t.Fatalf("disabled limiter must always allow, denied at %d: %v", i, err)
		}
	}
}

func TestAuthenticateMD_RequireMTLSWithVerifiedCertAccepts(t *testing.T) {
	want := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1", TLSVerified: true}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: want}
	cfg := AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a, RequireMTLS: true}
	id, err := authenticateMD(context.Background(), cfg)
	if err != nil {
		t.Fatalf("verified cert + auth ok must accept, got %v", err)
	}
	if id != want {
		t.Errorf("expected forwarded identity")
	}
}

// ─── End-to-end with bufconn ──────────────────────────────────────────

// captureWriter trivially absorbs Write calls so the StreamMetrics
// handler does not fail on a nil writer.
type captureWriter struct{ batches int }

func (c *captureWriter) Write(_ context.Context, samples []*agentv1.Sample) error {
	c.batches++
	return nil
}

// startBufconnServer spins a Server with the given AuthConfig over an
// in-memory listener and returns a dial fn the test uses to build
// clients.
func startBufconnServer(t *testing.T, cfg AuthConfig) (func(ctx context.Context) (*grpc.ClientConn, error), func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(UnaryAuthInterceptor(cfg)),
		grpc.StreamInterceptor(StreamAuthInterceptor(cfg)),
	)
	agentv1.RegisterAgentIngestServer(srv, NewServer(&captureWriter{}))
	go func() { _ = srv.Serve(lis) }()

	dial := func(ctx context.Context) (*grpc.ClientConn, error) {
		return grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
	stop := func() {
		srv.Stop()
		_ = lis.Close()
	}
	return dial, stop
}

func TestInterceptor_E2E_DisabledAcceptsCallWithoutCreds(t *testing.T) {
	dial, stop := startBufconnServer(t, AuthConfig{Enforcement: EnforcementDisabled})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := agentv1.NewAgentIngestClient(conn)
	resp, err := client.Register(ctx, &agentv1.RegisterRequest{NodeName: "node-a", AgentVersion: "test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.GetAgentId() == "" {
		t.Error("expected agent_id to be populated even in disabled mode")
	}
}

func TestInterceptor_E2E_EnforcedRejectsMissingCredentials(t *testing.T) {
	a := &stubAuther{mode: auth.ModeIngestToken, err: auth.ErrMissingToken}
	dial, stop := startBufconnServer(t, AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := agentv1.NewAgentIngestClient(conn)
	_, err = client.Register(ctx, &agentv1.RegisterRequest{NodeName: "node-a"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code = %s, want Unauthenticated", st.Code())
	}
}

func TestInterceptor_E2E_EnforcedAcceptsValidCredentials(t *testing.T) {
	want := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1", ClusterID: "c1"}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: want}
	dial, stop := startBufconnServer(t, AuthConfig{Enforcement: EnforcementEnforced, Authenticator: a})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := agentv1.NewAgentIngestClient(conn)
	// Inject some metadata so the interceptor's metadata.FromIncomingContext
	// path is exercised even though our stub ignores the values.
	md := metadata.New(map[string]string{
		auth.MetadataAuthMode:      string(auth.ModeIngestToken),
		auth.MetadataAuthorization: "Bearer kb_dummy",
	})
	ctx = metadata.NewOutgoingContext(ctx, md)

	resp, err := client.Register(ctx, &agentv1.RegisterRequest{NodeName: "node-a"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// agent_id must be the deterministic derive from identity, not a UUID.
	derived := auth.DeriveAgentID(want.TenantID, want.ClusterID, "node-a")
	if resp.GetAgentId() != derived {
		t.Errorf("agent_id = %s, want derived %s", resp.GetAgentId(), derived)
	}
	if resp.GetClusterId() != want.ClusterID {
		t.Errorf("cluster_id = %s, want %s", resp.GetClusterId(), want.ClusterID)
	}
}

// ─── resolveAgentID ───────────────────────────────────────────────────

func TestResolveAgentID(t *testing.T) {
	t.Run("nil identity falls back to UUID + local cluster", func(t *testing.T) {
		id, cluster := resolveAgentID(nil, "node-a")
		if cluster != "local" {
			t.Errorf("cluster = %s, want local", cluster)
		}
		if len(id) != 36 { // UUID canonical length
			t.Errorf("expected UUID-shaped id, got %s (len=%d)", id, len(id))
		}
	})
	t.Run("disabled mode falls back to UUID", func(t *testing.T) {
		id, _ := resolveAgentID(&auth.AgentIdentity{Mode: auth.ModeDisabled}, "node-a")
		if len(id) != 36 {
			t.Errorf("expected UUID-shaped id, got %s", id)
		}
	})
	t.Run("authenticated identity yields stable derived id", func(t *testing.T) {
		identity := &auth.AgentIdentity{
			Mode: auth.ModeIngestToken, TenantID: "t1", ClusterID: "c1",
		}
		id1, _ := resolveAgentID(identity, "node-a")
		id2, _ := resolveAgentID(identity, "node-a")
		if id1 != id2 {
			t.Errorf("derived id must be stable, got %s vs %s", id1, id2)
		}
		if len(id1) != 16 {
			t.Errorf("derived id = %s (len=%d), want 16 hex chars", id1, len(id1))
		}
	})
	t.Run("identity with empty cluster falls back to local", func(t *testing.T) {
		identity := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1"}
		_, cluster := resolveAgentID(identity, "node-a")
		if cluster != "local" {
			t.Errorf("cluster = %s, want local fallback", cluster)
		}
	})
}
