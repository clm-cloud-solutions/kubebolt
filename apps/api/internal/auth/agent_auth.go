// Package auth — agent_auth.go hosts the identity model and dispatch
// machinery used by the kubebolt-agent gRPC interceptor (Sprint A).
//
// The interceptor lives in apps/api/internal/agent (commit 5); this file
// stays in package auth so it can call into TenantsStore and reuse the
// existing user/cluster persistence layer.
//
// Wire format:
//
//	metadata:
//	  authorization: Bearer <token>
//	  kubebolt-auth-mode: tokenreview | ingest-token
//
// CompositeAuth dispatches to the right AgentAuthenticator based on the
// mode header. A short-TTL in-memory cache memoizes successful lookups
// so we do not hammer BoltDB / TokenReview on every RPC.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// AgentAuthMode names the credential family the agent presents.
type AgentAuthMode string

const (
	ModeTokenReview AgentAuthMode = "tokenreview"
	ModeIngestToken AgentAuthMode = "ingest-token"
)

const (
	// MetadataAuthMode is the gRPC metadata key the agent sets to declare
	// its credential type. Explicit > inferred so an ingest token is
	// never accidentally forwarded to TokenReview.
	MetadataAuthMode = "kubebolt-auth-mode"
	// MetadataAuthorization is the standard Bearer-token header.
	MetadataAuthorization = "authorization"
)

// AgentIdentity is the authenticated subject behind a gRPC stream from
// kubebolt-agent. The interceptor injects it into the request context.
//
// Sprint A leaves ClusterID, NodeName, and AgentID partially populated:
// the authenticator only knows TenantID at handshake time. The
// interceptor fills the rest from RegisterRequest and computes AgentID
// via DeriveAgentID — Sprint B replaces that derivation with a ULID
// stored in the agents bucket.
type AgentIdentity struct {
	Mode        AgentAuthMode
	TenantID    string
	ClusterID   string
	AgentID     string
	SAName      string // tokenreview only
	SANamespace string // tokenreview only
	NodeName    string
	TLSVerified bool
	AuthedAt    time.Time
}

// AgentAuthenticator validates a single auth mode. CompositeAuth fans
// out to the right one based on the mode metadata header.
type AgentAuthenticator interface {
	Mode() AgentAuthMode
	Authenticate(ctx context.Context, md metadata.MD, p *peer.Peer) (*AgentIdentity, error)
}

// Sentinel errors. The interceptor maps them to gRPC status codes:
//
//	ErrMissingToken / ErrTokenInvalid / ErrTokenMalformed / ErrTokenExpired -> Unauthenticated
//	ErrTokenRevoked / ErrTenantDisabled                                     -> PermissionDenied
//	ErrUnknownMode                                                          -> InvalidArgument
//	ErrTLSRequired                                                          -> Unauthenticated
//
// ErrTokenMalformed / ErrTokenExpired / ErrTokenRevoked / ErrTenantDisabled
// / ErrTokenNotFound are defined in tenants_store.go and reused as-is.
var (
	ErrMissingToken = errors.New("agent auth: missing bearer token")
	ErrTokenInvalid = errors.New("agent auth: token invalid")
	ErrUnknownMode  = errors.New("agent auth: unknown mode")
	ErrTLSRequired  = errors.New("agent auth: client cert required")
)

// extractBearer pulls "Bearer <token>" from the gRPC metadata. Returns
// ErrMissingToken on absence or malformed scheme.
func extractBearer(md metadata.MD) (string, error) {
	vals := md.Get(MetadataAuthorization)
	if len(vals) == 0 {
		return "", ErrMissingToken
	}
	parts := strings.SplitN(vals[0], " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", ErrMissingToken
	}
	return strings.TrimSpace(parts[1]), nil
}

// extractMode pulls the kubebolt-auth-mode metadata header. Missing or
// unrecognized values both surface as ErrUnknownMode — the wire result
// is identical and we do not want to leak whether a mode is configured.
func extractMode(md metadata.MD) (AgentAuthMode, error) {
	vals := md.Get(MetadataAuthMode)
	if len(vals) == 0 {
		return "", ErrUnknownMode
	}
	m := AgentAuthMode(strings.ToLower(strings.TrimSpace(vals[0])))
	switch m {
	case ModeTokenReview, ModeIngestToken:
		return m, nil
	default:
		return "", ErrUnknownMode
	}
}

// peerHasVerifiedClientCert reports whether the gRPC peer presented an
// mTLS client cert that the server has already validated. We rely on
// VerifiedChains, which is populated only after a successful chain build
// against the configured client CA — so a peer that merely sends a cert
// without server-side verification reads as false.
func peerHasVerifiedClientCert(p *peer.Peer) bool {
	if p == nil {
		return false
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return false
	}
	return len(info.State.VerifiedChains) > 0
}

// DeriveAgentID computes the Sprint A stable agent identifier. It is
// deterministic, so reconnects produce the same ID without persistent
// state. Sprint B replaces this with a ULID stored in the agents bucket.
//
// 64 bits is enough — collisions only matter inside a single tenant, and
// a tenant operating 2^32 nodes is not on the roadmap.
func DeriveAgentID(tenantID, clusterID, nodeName string) string {
	sum := sha256.Sum256([]byte(tenantID + "|" + clusterID + "|" + nodeName))
	return hex.EncodeToString(sum[:8])
}

// ─── Cache ───────────────────────────────────────────────────────────
//
// authCache memoizes (token hash → identity) for a short TTL so the
// hot path stays out of BoltDB / TokenReview on repeat RPCs.
//
// Sprint A semantics:
//   - Lazy expiry on get; no background sweeper.
//   - Invalidate() clears every entry. The admin handlers (commit 7)
//     call this after revoke / rotate / tenant-disable.
//
// Sprint B can refine to per-tenant invalidation if cache churn becomes
// observable.

type authCache struct {
	ttl   time.Duration
	nowFn func() time.Time
	mu    sync.RWMutex
	items map[string]authCacheEntry
}

type authCacheEntry struct {
	identity  *AgentIdentity
	expiresAt time.Time
}

func newAuthCache(ttl time.Duration) *authCache {
	return &authCache{
		ttl:   ttl,
		nowFn: func() time.Time { return time.Now() },
		items: map[string]authCacheEntry{},
	}
}

func (c *authCache) get(key string) (*AgentIdentity, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if c.nowFn().After(e.expiresAt) {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return nil, false
	}
	return e.identity, true
}

func (c *authCache) put(key string, identity *AgentIdentity) {
	c.mu.Lock()
	c.items[key] = authCacheEntry{
		identity:  identity,
		expiresAt: c.nowFn().Add(c.ttl),
	}
	c.mu.Unlock()
}

func (c *authCache) invalidate() {
	c.mu.Lock()
	c.items = map[string]authCacheEntry{}
	c.mu.Unlock()
}

// ─── Composite ───────────────────────────────────────────────────────

// CompositeAuth dispatches to the right AgentAuthenticator based on the
// "kubebolt-auth-mode" metadata header. Each member is registered once
// at startup; lookup is a map check, no fan-out.
type CompositeAuth struct {
	authers map[AgentAuthMode]AgentAuthenticator
}

// NewCompositeAuth builds the dispatcher. Order does not matter — the
// last registered authenticator wins for a given mode if duplicates are
// passed (a misconfiguration we surface neither way; tests catch it).
func NewCompositeAuth(authers ...AgentAuthenticator) *CompositeAuth {
	m := make(map[AgentAuthMode]AgentAuthenticator, len(authers))
	for _, a := range authers {
		m[a.Mode()] = a
	}
	return &CompositeAuth{authers: m}
}

// Authenticate selects the implementation by metadata header and
// delegates. Missing / unknown / unconfigured modes all return
// ErrUnknownMode — the interceptor maps that to InvalidArgument.
func (c *CompositeAuth) Authenticate(ctx context.Context, md metadata.MD, p *peer.Peer) (*AgentIdentity, error) {
	mode, err := extractMode(md)
	if err != nil {
		return nil, err
	}
	auther, ok := c.authers[mode]
	if !ok {
		return nil, fmt.Errorf("%w: %q not configured", ErrUnknownMode, mode)
	}
	return auther.Authenticate(ctx, md, p)
}
