package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// mdWith builds a metadata.MD from "k1, v1, k2, v2, …" pairs. Bare
// helper to keep the tests legible.
func mdWith(kv ...string) metadata.MD {
	md := metadata.MD{}
	for i := 0; i+1 < len(kv); i += 2 {
		md.Append(kv[i], kv[i+1])
	}
	return md
}

func TestExtractBearer(t *testing.T) {
	cases := []struct {
		name string
		md   metadata.MD
		want string
		err  error
	}{
		{"happy path", mdWith("authorization", "Bearer kb_abc"), "kb_abc", nil},
		{"case insensitive scheme", mdWith("authorization", "bearer kb_abc"), "kb_abc", nil},
		{"missing header", mdWith(), "", ErrMissingToken},
		{"empty token", mdWith("authorization", "Bearer "), "", ErrMissingToken},
		{"wrong scheme", mdWith("authorization", "Basic abc"), "", ErrMissingToken},
		{"no scheme", mdWith("authorization", "abc"), "", ErrMissingToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extractBearer(c.md)
			if !errors.Is(err, c.err) {
				t.Errorf("err = %v, want %v", err, c.err)
			}
			if got != c.want {
				t.Errorf("token = %q, want %q", got, c.want)
			}
		})
	}
}

func TestExtractMode(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  AgentAuthMode
		err   error
	}{
		{"tokenreview", "tokenreview", ModeTokenReview, nil},
		{"ingest-token", "ingest-token", ModeIngestToken, nil},
		{"upper case normalized", "TOKENREVIEW", ModeTokenReview, nil},
		{"missing", "", "", ErrUnknownMode},
		{"unknown value", "oauth", "", ErrUnknownMode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			md := metadata.MD{}
			if c.value != "" {
				md.Append(MetadataAuthMode, c.value)
			}
			got, err := extractMode(md)
			if !errors.Is(err, c.err) {
				t.Errorf("err = %v, want %v", err, c.err)
			}
			if got != c.want {
				t.Errorf("mode = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDeriveAgentID_DeterministicAndDistinguishing(t *testing.T) {
	a := DeriveAgentID("t1", "c1", "node-a")
	b := DeriveAgentID("t1", "c1", "node-a")
	if a != b {
		t.Errorf("derive must be deterministic, got %s vs %s", a, b)
	}
	if same := DeriveAgentID("t1", "c1", "node-b"); same == a {
		t.Errorf("different node should yield different id, got collision %s", same)
	}
	if same := DeriveAgentID("t2", "c1", "node-a"); same == a {
		t.Errorf("different tenant should yield different id, got collision %s", same)
	}
	if len(a) != 16 {
		t.Errorf("agent id length = %d, want 16 hex chars (64 bits)", len(a))
	}
}

func TestPeerHasVerifiedClientCert(t *testing.T) {
	if peerHasVerifiedClientCert(nil) {
		t.Error("nil peer should read as unverified")
	}
	if peerHasVerifiedClientCert(&peer.Peer{}) {
		t.Error("peer without AuthInfo should read as unverified")
	}
	// TLSInfo with empty State.VerifiedChains — the gRPC server has not
	// finished cert chain validation, so we must NOT treat it as verified.
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{}}
	if peerHasVerifiedClientCert(p) {
		t.Error("TLSInfo without VerifiedChains must not flip to verified")
	}
}

// ─── Cache ────────────────────────────────────────────────────────────

func TestAuthCache_HitAndExpire(t *testing.T) {
	c := newAuthCache(time.Minute)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.nowFn = func() time.Time { return base }

	id := &AgentIdentity{TenantID: "t1"}
	c.put("k", id)

	if got, ok := c.get("k"); !ok || got != id {
		t.Errorf("expected immediate hit, got ok=%v id=%v", ok, got)
	}

	c.nowFn = func() time.Time { return base.Add(2 * time.Minute) }
	if _, ok := c.get("k"); ok {
		t.Error("expected miss past TTL")
	}
	c.mu.RLock()
	_, present := c.items["k"]
	c.mu.RUnlock()
	if present {
		t.Error("expired entry must be evicted lazily on get")
	}
}

func TestAuthCache_Invalidate(t *testing.T) {
	c := newAuthCache(time.Hour)
	c.put("a", &AgentIdentity{})
	c.put("b", &AgentIdentity{})
	c.invalidate()
	if _, ok := c.get("a"); ok {
		t.Error("invalidate should clear key a")
	}
	if _, ok := c.get("b"); ok {
		t.Error("invalidate should clear key b")
	}
}

// ─── BearerIngestAuth ─────────────────────────────────────────────────

func TestBearerIngestAuth_HappyPath(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plain, _, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)

	auth := NewBearerIngestAuth(ts, time.Minute)
	md := mdWith(MetadataAuthorization, "Bearer "+plain)
	id, err := auth.Authenticate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.TenantID != tn.ID {
		t.Errorf("TenantID = %s, want %s", id.TenantID, tn.ID)
	}
	if id.Mode != ModeIngestToken {
		t.Errorf("Mode = %s, want %s", id.Mode, ModeIngestToken)
	}
	if id.TLSVerified {
		t.Error("nil peer should yield TLSVerified=false")
	}
	if id.AuthedAt.IsZero() {
		t.Error("AuthedAt must be stamped")
	}
}

func TestBearerIngestAuth_MissingToken(t *testing.T) {
	ts := newTestTenantsStore(t)
	auth := NewBearerIngestAuth(ts, time.Minute)
	if _, err := auth.Authenticate(context.Background(), mdWith(), nil); !errors.Is(err, ErrMissingToken) {
		t.Errorf("expected ErrMissingToken, got %v", err)
	}
}

func TestBearerIngestAuth_MalformedToken(t *testing.T) {
	ts := newTestTenantsStore(t)
	auth := NewBearerIngestAuth(ts, time.Minute)
	md := mdWith(MetadataAuthorization, "Bearer not-a-kb-token")
	if _, err := auth.Authenticate(context.Background(), md, nil); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("expected ErrTokenMalformed, got %v", err)
	}
}

func TestBearerIngestAuth_UnknownToken(t *testing.T) {
	ts := newTestTenantsStore(t)
	auth := NewBearerIngestAuth(ts, time.Minute)
	md := mdWith(MetadataAuthorization, "Bearer "+TokenPrefix+"deadbeef")
	if _, err := auth.Authenticate(context.Background(), md, nil); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("expected ErrTokenNotFound, got %v", err)
	}
}

func TestBearerIngestAuth_RevokeRequiresInvalidateCache(t *testing.T) {
	// Pin the cache contract: revoking on the store does NOT push through
	// to the cache automatically. The admin handler is responsible for
	// calling InvalidateCache after a RevokeToken mutation.
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plain, tok, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)

	auth := NewBearerIngestAuth(ts, time.Minute)
	md := mdWith(MetadataAuthorization, "Bearer "+plain)

	if _, err := auth.Authenticate(context.Background(), md, nil); err != nil {
		t.Fatalf("initial auth: %v", err)
	}
	if err := ts.RevokeToken(tn.ID, tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := auth.Authenticate(context.Background(), md, nil); err != nil {
		t.Errorf("expected stale cache hit before InvalidateCache, got %v", err)
	}
	auth.InvalidateCache()
	if _, err := auth.Authenticate(context.Background(), md, nil); err == nil {
		t.Error("expected lookup failure post-invalidation for revoked token")
	}
}

func TestBearerIngestAuth_DisabledTenant(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plain, _, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)

	auth := NewBearerIngestAuth(ts, 0) // cache disabled — every call hits store
	md := mdWith(MetadataAuthorization, "Bearer "+plain)
	if _, err := auth.Authenticate(context.Background(), md, nil); err != nil {
		t.Fatalf("baseline auth: %v", err)
	}
	if _, err := ts.UpdateTenant(tn.ID, func(t *Tenant) error { t.Disabled = true; return nil }); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if _, err := auth.Authenticate(context.Background(), md, nil); !errors.Is(err, ErrTenantDisabled) {
		t.Errorf("expected ErrTenantDisabled, got %v", err)
	}
}

func TestBearerIngestAuth_TLSVerifiedFromPeer(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plain, _, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)

	auth := NewBearerIngestAuth(ts, time.Minute)
	md := mdWith(MetadataAuthorization, "Bearer "+plain)

	// Peer with TLSInfo but no VerifiedChains → still unverified.
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{}}
	id, err := auth.Authenticate(context.Background(), md, p)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.TLSVerified {
		t.Error("TLSInfo without VerifiedChains must not flip TLSVerified")
	}
}

func TestBearerIngestAuth_ConcurrentAuth(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plain, _, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)
	auth := NewBearerIngestAuth(ts, time.Minute)
	md := mdWith(MetadataAuthorization, "Bearer "+plain)

	var wg sync.WaitGroup
	const N = 50
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = auth.Authenticate(context.Background(), md, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent auth[%d]: %v", i, err)
		}
	}
}

// ─── CompositeAuth ────────────────────────────────────────────────────

type stubAuther struct {
	mode    AgentAuthMode
	called  bool
	returns *AgentIdentity
	err     error
}

func (s *stubAuther) Mode() AgentAuthMode { return s.mode }
func (s *stubAuther) Authenticate(ctx context.Context, md metadata.MD, p *peer.Peer) (*AgentIdentity, error) {
	s.called = true
	return s.returns, s.err
}

func TestCompositeAuth_DispatchesByMode(t *testing.T) {
	tr := &stubAuther{mode: ModeTokenReview, returns: &AgentIdentity{Mode: ModeTokenReview}}
	bg := &stubAuther{mode: ModeIngestToken, returns: &AgentIdentity{Mode: ModeIngestToken}}
	c := NewCompositeAuth(tr, bg)

	md := mdWith(MetadataAuthMode, "tokenreview", MetadataAuthorization, "Bearer x")
	id, err := c.Authenticate(context.Background(), md, nil)
	if err != nil || id == nil || id.Mode != ModeTokenReview {
		t.Errorf("tokenreview dispatch: id=%v err=%v", id, err)
	}
	if !tr.called || bg.called {
		t.Errorf("only tokenreview should be invoked, got tr=%v bg=%v", tr.called, bg.called)
	}

	tr.called, bg.called = false, false
	md = mdWith(MetadataAuthMode, "ingest-token", MetadataAuthorization, "Bearer x")
	id, err = c.Authenticate(context.Background(), md, nil)
	if err != nil || id == nil || id.Mode != ModeIngestToken {
		t.Errorf("ingest dispatch: id=%v err=%v", id, err)
	}
	if tr.called || !bg.called {
		t.Errorf("only ingest should be invoked, got tr=%v bg=%v", tr.called, bg.called)
	}
}

func TestCompositeAuth_UnconfiguredMode(t *testing.T) {
	c := NewCompositeAuth(&stubAuther{mode: ModeIngestToken})
	md := mdWith(MetadataAuthMode, "tokenreview", MetadataAuthorization, "Bearer x")
	if _, err := c.Authenticate(context.Background(), md, nil); !errors.Is(err, ErrUnknownMode) {
		t.Errorf("expected ErrUnknownMode for unconfigured mode, got %v", err)
	}
}

func TestCompositeAuth_MissingModeHeader(t *testing.T) {
	c := NewCompositeAuth(&stubAuther{mode: ModeIngestToken})
	md := mdWith(MetadataAuthorization, "Bearer x")
	if _, err := c.Authenticate(context.Background(), md, nil); !errors.Is(err, ErrUnknownMode) {
		t.Errorf("expected ErrUnknownMode for missing header, got %v", err)
	}
}
