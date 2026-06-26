package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
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

// ─── DefaultTenantID fallback (added after yagan rate-limit investigation) ───
//
// When TenantsStore is wired at the app layer, AuthConfig.DefaultTenantID
// gets populated at startup so disabled/permissive-fallback identities
// stamp it on the AgentIdentity. The downstream rate limiter then keys
// against that real tenant UUID instead of bypassing on empty TenantID.
//
// These tests pin both halves of the matrix:
//
//   Enforcement | DefaultTenantID | expected identity TenantID
//   ------------|-----------------|---------------------------
//   disabled    | "uuid-X"        | "uuid-X" (new)
//   disabled    | ""              | "" (legacy bypass preserved)
//   permissive  | "uuid-X" + fail | "uuid-X" (new)
//   permissive  | "" + fail       | "" (legacy bypass preserved)
//   permissive  | "uuid-X" + ok   | from authenticator (regression guard)
//   enforced    | "uuid-X" + fail | rejected (regression guard — no change)

func TestAuthenticateMD_Disabled_WithDefaultTenantID_StampsIt(t *testing.T) {
	const want = "uuid-default-tenant"
	id, err := authenticateMD(context.Background(), AuthConfig{
		Enforcement:     EnforcementDisabled,
		DefaultTenantID: want,
	})
	if err != nil {
		t.Fatalf("disabled mode should never error, got %v", err)
	}
	if id == nil {
		t.Fatalf("expected dummy identity, got nil")
	}
	if id.TenantID != want {
		t.Errorf("expected dummy identity to carry default tenant %q, got %q", want, id.TenantID)
	}
}

func TestAuthenticateMD_Disabled_WithoutDefaultTenantID_StampsEmpty(t *testing.T) {
	// Regression guard: when TenantsStore unwired at the app layer
	// (KUBEBOLT_AUTH_ENABLED=false), DefaultTenantID is empty and the
	// rate limiter bypasses on empty key — legacy behavior preserved.
	id, err := authenticateMD(context.Background(), AuthConfig{
		Enforcement: EnforcementDisabled,
	})
	if err != nil {
		t.Fatalf("disabled mode should never error, got %v", err)
	}
	if id == nil {
		t.Fatalf("expected dummy identity, got nil")
	}
	if id.TenantID != "" {
		t.Errorf("expected empty TenantID for legacy bypass, got %q", id.TenantID)
	}
}

func TestAuthenticateMD_PermissiveFallback_WithDefaultTenantID_StampsIt(t *testing.T) {
	const want = "uuid-default-tenant"
	a := &stubAuther{mode: auth.ModeIngestToken, err: auth.ErrTokenInvalid}
	id, err := authenticateMD(context.Background(), AuthConfig{
		Enforcement:     EnforcementPermissive,
		Authenticator:   a,
		DefaultTenantID: want,
	})
	if err != nil {
		t.Fatalf("permissive should not error, got %v", err)
	}
	if id == nil {
		t.Fatalf("expected dummy identity, got nil")
	}
	if id.TenantID != want {
		t.Errorf("expected fallback identity to carry default tenant %q, got %q", want, id.TenantID)
	}
}

func TestAuthenticateMD_PermissiveFallback_WithoutDefaultTenantID_StampsEmpty(t *testing.T) {
	// Regression guard for the legacy bypass path.
	a := &stubAuther{mode: auth.ModeIngestToken, err: auth.ErrTokenInvalid}
	id, err := authenticateMD(context.Background(), AuthConfig{
		Enforcement:   EnforcementPermissive,
		Authenticator: a,
	})
	if err != nil {
		t.Fatalf("permissive should not error, got %v", err)
	}
	if id == nil {
		t.Fatalf("expected dummy identity, got nil")
	}
	if id.TenantID != "" {
		t.Errorf("expected empty TenantID for legacy bypass, got %q", id.TenantID)
	}
}

func TestAuthenticateMD_PermissiveSuccess_DefaultTenantIDIgnored(t *testing.T) {
	// Regression guard: when auth succeeds, the resolved identity
	// wins — the DefaultTenantID field MUST NOT clobber a real
	// authenticated tenant.
	resolved := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "real-tenant-from-token"}
	a := &stubAuther{mode: auth.ModeIngestToken, returns: resolved}
	id, err := authenticateMD(context.Background(), AuthConfig{
		Enforcement:     EnforcementPermissive,
		Authenticator:   a,
		DefaultTenantID: "uuid-default-tenant",
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id != resolved {
		t.Errorf("expected forwarded identity, got %+v", id)
	}
}

func TestAuthenticateMD_Enforced_DefaultTenantIDDoesNotRelaxRejection(t *testing.T) {
	// Regression guard: setting DefaultTenantID MUST NOT make enforced
	// mode accept invalid tokens. Default tenant is for unauthenticated
	// fallback, not a bypass of enforcement.
	a := &stubAuther{mode: auth.ModeIngestToken, err: auth.ErrTokenInvalid}
	_, err := authenticateMD(context.Background(), AuthConfig{
		Enforcement:     EnforcementEnforced,
		Authenticator:   a,
		DefaultTenantID: "uuid-default-tenant",
	})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %s", st.Code())
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

// captureWriter trivially absorbs Write calls so the metrics path does
// not fail on a nil writer when tests don't exercise it.
type captureWriter struct{ batches int }

func (c *captureWriter) Write(_ context.Context, samples []*agentv2.Sample) error {
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
	agentv2.RegisterAgentChannelServer(srv, NewServer(&captureWriter{}))
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

// helloAndWait dials the AgentChannel, sends Hello, and waits for the
// first BackendMessage. Returns Welcome on success, or the auth error
// when the interceptor rejects the stream (which surfaces on the first
// Recv, not the Send — gRPC streams open optimistically).
func helloAndWait(ctx context.Context, conn *grpc.ClientConn, nodeName string) (*agentv2.Welcome, error) {
	client := agentv2.NewAgentChannelClient(conn)
	stream, err := client.Channel(ctx)
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&agentv2.AgentMessage{
		Kind: &agentv2.AgentMessage_Hello{
			Hello: &agentv2.Hello{NodeName: nodeName, AgentVersion: "test"},
		},
	}); err != nil {
		// Send may succeed before the server's auth rejection lands; if it
		// errors immediately though, propagate.
		return nil, err
	}
	msg, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	w := msg.GetWelcome()
	if w == nil {
		return nil, fmt.Errorf("expected Welcome, got %T", msg.Kind)
	}
	return w, nil
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

	w, err := helloAndWait(ctx, conn, "node-a")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if w.GetAgentId() == "" {
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

	_, err = helloAndWait(ctx, conn, "node-a")
	if err == nil {
		t.Fatal("expected handshake to fail, got nil error (auth was bypassed)")
	}
	// gRPC's bufconn transport occasionally surfaces a stream rejected
	// in the StreamServerInterceptor as io.EOF on the client's first
	// Recv instead of the typed status frame the interceptor returned.
	// Repro is environment-sensitive: full-package runs on macOS get
	// the proper Unauthenticated; isolated runs and ubuntu-latest CI
	// get EOF because the trailers race with the connection close.
	// Both manifestations satisfy what this test verifies — that a
	// missing-credentials request DOES NOT succeed. Accept either as
	// a valid rejection signal so the test isn't flake-prone across
	// environments.
	if errors.Is(err, io.EOF) {
		return
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error or io.EOF, got %v", err)
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

	// Inject metadata so the interceptor's FromIncomingContext path is
	// exercised even though our stub ignores the values.
	md := metadata.New(map[string]string{
		auth.MetadataAuthMode:      string(auth.ModeIngestToken),
		auth.MetadataAuthorization: "Bearer kb_dummy",
	})
	ctx = metadata.NewOutgoingContext(ctx, md)

	w, err := helloAndWait(ctx, conn, "node-a")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// agent_id must be the deterministic derive from identity, not a UUID.
	// helloAndWait sends a Hello with empty Capabilities, so the resolved
	// role is "metrics" (no kube-proxy capability advertised).
	derived := auth.DeriveAgentID(want.TenantID, want.ClusterID, "node-a", "metrics")
	if w.GetAgentId() != derived {
		t.Errorf("agent_id = %s, want derived %s", w.GetAgentId(), derived)
	}
	if w.GetClusterId() != want.ClusterID {
		t.Errorf("cluster_id = %s, want %s", w.GetClusterId(), want.ClusterID)
	}
}

// ─── resolveAgentID ───────────────────────────────────────────────────

func TestResolveAgentID(t *testing.T) {
	t.Run("nil identity derives a stable id (no per-connect UUID)", func(t *testing.T) {
		id, cluster := resolveAgentID(nil, "node-a", "", "metrics")
		if cluster != "local" {
			t.Errorf("cluster = %s, want local", cluster)
		}
		// Stable 16-hex derive from ("", "local", node, role) — NOT a fresh UUID
		// per connect, which leaked a permanent "connected" ghost record on every
		// reconnect when auth is disabled.
		want := auth.DeriveAgentID("", "local", "node-a", "metrics")
		if id != want {
			t.Errorf("id = %s, want stable derive %s", id, want)
		}
		if again, _ := resolveAgentID(nil, "node-a", "", "metrics"); again != id {
			t.Errorf("id must be stable across reconnects, got %s then %s", id, again)
		}
	})
	t.Run("disabled mode derives a stable id (no per-connect UUID)", func(t *testing.T) {
		id, _ := resolveAgentID(&auth.AgentIdentity{Mode: auth.ModeDisabled}, "node-a", "", "metrics")
		want := auth.DeriveAgentID("", "local", "node-a", "metrics")
		if id != want {
			t.Errorf("id = %s, want stable derive %s", id, want)
		}
	})
	t.Run("authenticated identity yields stable derived id", func(t *testing.T) {
		identity := &auth.AgentIdentity{
			Mode: auth.ModeIngestToken, TenantID: "t1", ClusterID: "c1",
		}
		id1, _ := resolveAgentID(identity, "node-a", "", "metrics")
		id2, _ := resolveAgentID(identity, "node-a", "", "metrics")
		if id1 != id2 {
			t.Errorf("derived id must be stable, got %s vs %s", id1, id2)
		}
		if len(id1) != 16 {
			t.Errorf("derived id = %s (len=%d), want 16 hex chars", id1, len(id1))
		}
	})
	t.Run("identity with empty cluster and empty hint falls back to local", func(t *testing.T) {
		identity := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1"}
		_, cluster := resolveAgentID(identity, "node-a", "", "metrics")
		if cluster != "local" {
			t.Errorf("cluster = %s, want local fallback", cluster)
		}
	})

	// BUG-1 regression cases (internal/cluster-validation/sessions/00-humo-test/09)
	// — pre-fix the backend dropped Hello.cluster_hint and collapsed all
	// agents without an auth-set ClusterID to "local". These guard the
	// precedence id.ClusterID > clusterHint > "local" in both branches.
	t.Run("disabled mode honors non-empty cluster_hint", func(t *testing.T) {
		// No auth identity but the agent reported a cluster_id —
		// preserve it so multi-cluster OSS deployments work.
		_, cluster := resolveAgentID(nil, "node-a", "cluster-A", "metrics")
		if cluster != "cluster-A" {
			t.Errorf("cluster = %s, want cluster-A (from hint)", cluster)
		}
	})
	t.Run("ModeDisabled identity honors non-empty cluster_hint", func(t *testing.T) {
		// Same as above but with an explicit ModeDisabled identity —
		// exercises the second arm of the disabled-path predicate.
		_, cluster := resolveAgentID(&auth.AgentIdentity{Mode: auth.ModeDisabled}, "node-a", "cluster-B", "metrics")
		if cluster != "cluster-B" {
			t.Errorf("cluster = %s, want cluster-B (from hint)", cluster)
		}
	})
	t.Run("ingest-token with empty ClusterID falls back to cluster_hint", func(t *testing.T) {
		// BearerIngestAuth doesn't populate ClusterID, so the hint
		// must take over — otherwise SaaS multi-cluster (the topology
		// that drove the bug escalation) collapses to "local".
		identity := &auth.AgentIdentity{Mode: auth.ModeIngestToken, TenantID: "t1"}
		agentID, cluster := resolveAgentID(identity, "node-a", "cluster-C", "metrics")
		if cluster != "cluster-C" {
			t.Errorf("cluster = %s, want cluster-C (from hint)", cluster)
		}
		// And the derived agent_id must vary by cluster so two
		// clusters with the same node name don't collide.
		_, clusterD := resolveAgentID(identity, "node-a", "cluster-D", "metrics")
		agentIDD, _ := resolveAgentID(identity, "node-a", "cluster-D", "metrics")
		if clusterD != "cluster-D" || agentIDD == agentID {
			t.Errorf("agent_id must vary by cluster_id; got %s for both cluster-C and cluster-D", agentID)
		}
	})
	t.Run("auth ClusterID wins over cluster_hint", func(t *testing.T) {
		// tokenreview/mTLS bind the cluster at startup — the operator
		// configuration trumps anything the client claims, otherwise
		// the security guarantee evaporates.
		identity := &auth.AgentIdentity{
			Mode: auth.ModeTokenReview, TenantID: "t1", ClusterID: "auth-cluster",
		}
		_, cluster := resolveAgentID(identity, "node-a", "spoofed-cluster", "metrics")
		if cluster != "auth-cluster" {
			t.Errorf("cluster = %s, want auth-cluster (identity must win)", cluster)
		}
	})

	// Session 11-A regression: pre-fix, the DS pod (Mode A,
	// capabilities include kube-proxy → role "proxy") and the
	// Deployment promread pod (Mode C, capabilities=[metrics] →
	// role "metrics") derived the SAME agent_id when scheduled on
	// the same node, causing the registry to evict them in a ~30s
	// loop. Role must change the derivation.
	t.Run("same node different role yields distinct agent_id", func(t *testing.T) {
		identity := &auth.AgentIdentity{
			Mode: auth.ModeIngestToken, TenantID: "t1", ClusterID: "c1",
		}
		proxyID, _ := resolveAgentID(identity, "node-a", "", "proxy")
		metricsID, _ := resolveAgentID(identity, "node-a", "", "metrics")
		if proxyID == metricsID {
			t.Errorf("proxy and metrics roles must yield distinct agent_ids on same node; got %s for both", proxyID)
		}
	})
}

func TestAgentRoleFromHello(t *testing.T) {
	cases := []struct {
		name  string
		hello *agentv2.Hello
		want  string
	}{
		{"nil hello → metrics fallback", nil, "metrics"},
		// Label path (1.13+ agents) — authoritative.
		{
			"explicit mode label daemonset",
			&agentv2.Hello{Labels: map[string]string{"kubebolt.io/agent-mode": "daemonset"}},
			"daemonset",
		},
		{
			"explicit mode label promread",
			&agentv2.Hello{Labels: map[string]string{"kubebolt.io/agent-mode": "promread"}},
			"promread",
		},
		{
			"mode label wins over capability classifier",
			&agentv2.Hello{
				Labels:       map[string]string{"kubebolt.io/agent-mode": "promread"},
				Capabilities: []string{"metrics", "kube-proxy"},
			},
			"promread",
		},
		// Capability fallback (pre-1.13 agents) — no label.
		{"no label + kube-proxy capability → proxy", &agentv2.Hello{Capabilities: []string{"metrics", "kube-proxy"}}, "proxy"},
		{"no label + metrics only → metrics", &agentv2.Hello{Capabilities: []string{"metrics"}}, "metrics"},
		{"no label + empty capabilities → metrics", &agentv2.Hello{}, "metrics"},
		// Defensive: pathological-but-possible inputs.
		{"empty mode label falls through to capabilities", &agentv2.Hello{
			Labels:       map[string]string{"kubebolt.io/agent-mode": ""},
			Capabilities: []string{"kube-proxy"},
		}, "proxy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentRoleFromHello(tc.hello); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
