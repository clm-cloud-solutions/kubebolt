package auth

import (
	"context"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// BearerIngestAuth validates long-lived ingest tokens issued by the
// backend and stored in TenantsStore. Used in SaaS / cross-cluster
// deployments where TokenReview is not viable (the backend cannot reach
// the agent's origin apiserver).
//
// Cache contract: after a successful lookup the identity is cached for
// cacheTTL (typically 5 min). Revocation on the store does NOT push
// through automatically — admin handlers must call InvalidateCache after
// any RevokeToken / RotateToken / disable-tenant mutation. This is the
// trade we accept to keep the hot path lock-free.
type BearerIngestAuth struct {
	tokens  IngestTokenStore // token validity (revoked/expired)
	tenants TenantStore      // owning tenant lookup + Disabled check
	cache   *authCache
	nowFn   func() time.Time
}

// NewBearerIngestAuth wires the authenticator with the ingest-token store (for
// token validity) and the tenants store (to resolve + gate the owning tenant).
// cacheTTL=0 disables caching (every call hits BoltDB) — useful in tests; in
// production set 5*time.Minute.
func NewBearerIngestAuth(tokens IngestTokenStore, tenants TenantStore, cacheTTL time.Duration) *BearerIngestAuth {
	return &BearerIngestAuth{
		tokens:  tokens,
		tenants: tenants,
		cache:   newAuthCache(cacheTTL),
		nowFn:   func() time.Time { return time.Now().UTC() },
	}
}

// Mode satisfies AgentAuthenticator.
func (a *BearerIngestAuth) Mode() AgentAuthMode { return ModeIngestToken }

// Authenticate validates the bearer token against the tenants store.
// On success the identity carries TenantID and TLSVerified; ClusterID,
// NodeName, and AgentID are filled in by the interceptor once the agent
// sends RegisterRequest.
func (a *BearerIngestAuth) Authenticate(ctx context.Context, md metadata.MD, p *peer.Peer) (*AgentIdentity, error) {
	plaintext, err := extractBearer(md)
	if err != nil {
		return nil, err
	}
	hash := hashToken(plaintext)
	if cached, ok := a.cache.get(hash); ok {
		return cached, nil
	}
	tok, err := a.tokens.Lookup(ctx, plaintext)
	if err != nil {
		return nil, err
	}
	// Token is valid; gate on the owning tenant (Disabled is the TenantStore's
	// concern now that tokens no longer live inside the tenant record).
	tenant, err := a.tenants.GetTenant(tok.TenantID)
	if err != nil {
		return nil, err
	}
	if tenant.Disabled {
		return nil, ErrTenantDisabled
	}
	now := a.nowFn()
	identity := &AgentIdentity{
		Mode:        ModeIngestToken,
		TenantID:    tenant.ID,
		TLSVerified: peerHasVerifiedClientCert(p),
		AuthedAt:    now,
	}
	a.cache.put(hash, identity)
	// Best-effort touch — the store debounces persistence to once per
	// minute per token, so we can fire-and-forget every RPC.
	_ = a.tokens.MarkUsed(ctx, tok.TenantID, tok.ID, now)
	return identity, nil
}

// InvalidateCache clears every cached identity. Admin handlers call
// this after RevokeToken / RotateToken / disable-tenant so a previously
// cached entry cannot keep a revoked agent connected past the cache TTL.
func (a *BearerIngestAuth) InvalidateCache() { a.cache.invalidate() }
