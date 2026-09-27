package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// EdgeHeader is set by the public reverse proxy (nginx) on every request it
// proxies from the internet, and is the anti-leak control for service
// tokens: a kbs_ token presented over the public edge is rejected even if
// otherwise valid. Internal callers (Autopilot → API Service, in-cluster)
// reach the backend directly and never carry it. The proxy MUST force-set
// the header (proxy_set_header, overriding any client-supplied value).
const (
	EdgeHeader      = "X-KubeBolt-Edge"
	EdgeValuePublic = "public"
)

var (
	// ErrTokenEdgeBlocked is returned when a service token arrives via the
	// public edge (see EdgeHeader).
	ErrTokenEdgeBlocked = errors.New("service token not accepted over public edge")
	// errAPITokensUnavailable is returned when an API token is presented
	// but no store is wired.
	errAPITokensUnavailable = errors.New("api token auth not configured")
)

// APIPrincipal is the identity established by a REST API token. Stashed in
// the request context alongside the synthetic Claims so RequireRole keeps
// working (via Claims.Role) and EnforceAPITokenScope can read the scopes.
type APIPrincipal struct {
	TokenID   string
	Type      APITokenType
	Role      Role
	Scopes    []string
	TenantID  string
	ClusterID string
	// Clusters is the token's read allow-list (cluster ids); empty = every
	// cluster of its org. Read by package api's tokenClusters.
	Clusters []string
}

const apiPrincipalKey contextKey = "auth-api-principal"

// ContextAPIPrincipal returns the API-token principal for the request, or
// nil when the caller authenticated via a user-session JWT (or is
// unauthenticated).
func ContextAPIPrincipal(r *http.Request) *APIPrincipal {
	p, _ := r.Context().Value(apiPrincipalKey).(*APIPrincipal)
	return p
}

// WithAPIPrincipal stamps an API-token principal onto ctx, readable via
// ContextAPIPrincipal. Mirrors WithTenantID — lets tests simulate a kbs_/kbk_
// token caller without driving the full auth chain.
func WithAPIPrincipal(ctx context.Context, p *APIPrincipal) context.Context {
	return context.WithValue(ctx, apiPrincipalKey, p)
}

// validateAPIToken authenticates a REST API token (kbs_/kbk_). It returns a
// synthetic *Claims (so the existing role machinery works) and an
// *APIPrincipal (for scope enforcement). Service tokens are rejected when
// the request arrived via the public edge.
func (h *Handlers) validateAPIToken(r *http.Request, plaintext string) (*Claims, *APIPrincipal, error) {
	if h.apiTokens == nil {
		return nil, nil, errAPITokensUnavailable
	}
	tok, err := h.apiTokens.Lookup(r.Context(), plaintext)
	if err != nil {
		return nil, nil, err
	}
	if tok.Type == TokenTypeService && r.Header.Get(EdgeHeader) == EdgeValuePublic {
		return nil, nil, ErrTokenEdgeBlocked
	}
	// Best-effort last-used stamp (debounced in the store).
	_ = h.apiTokens.MarkUsed(r.Context(), tok.ID, time.Now())

	claims := &Claims{
		UserID:   "svc:" + tok.ID,
		Username: tok.Label,
		Role:     tok.Role,
		// Carry the token's org so ResolveTenant resolves API-token callers to
		// their real tenant instead of the DefaultTenantName fallback. Without
		// this every API-token request resolves to "default" — per-org RLS
		// reads, cluster scoping, and usage metering all attribute to the wrong
		// org. Empty in OSS (single-tenant tokens), so behavior is unchanged.
		TenantID: tok.TenantID,
	}
	p := &APIPrincipal{
		TokenID:   tok.ID,
		Type:      tok.Type,
		Role:      tok.Role,
		Scopes:    tok.Scopes,
		TenantID:  tok.TenantID,
		ClusterID: tok.ClusterID,
		Clusters:  tok.Clusters,
	}
	return claims, p, nil
}

// EnforceAPITokenScope restricts API-token callers to their granted path
// scopes. No-op for user-session JWT callers (they're governed by
// RequireRole). Mount it in the authenticated group, after RequireAuth.
func (h *Handlers) EnforceAPITokenScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := ContextAPIPrincipal(r)
		if p == nil {
			next.ServeHTTP(w, r)
			return
		}
		if msg := apiTokenForbiddenPath(p, r.Method, r.URL.Path); msg != "" {
			http.Error(w, `{"error":"`+msg+`"}`, http.StatusForbidden)
			return
		}
		if apiScopeAllows(p.Scopes, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, `{"error":"token scope does not permit this path"}`, http.StatusForbidden)
	})
}

// RequireServiceToken admits ONLY a machine principal holding a service token
// (kbs_). Everything else gets 403: a user-session JWT, a customer API key
// (kbk_), and an unauthenticated caller.
//
// It exists because EnforceAPITokenScope cannot do this job. That middleware
// PASSES THROUGH when there is no API principal — which is exactly the case for
// a signed-in browser user — so path-scoping a route and calling it closed is
// the mistake this prevents. Here a nil principal is the denial, not the
// exemption.
//
// The third door is already shut elsewhere: validateAPIToken refuses a service
// token that arrived over the public edge (ErrTokenEdgeBlocked), so a leaked
// kbs_ does not get in from outside either.
//
// Mount in the authenticated group, after RequireAuth.
func (h *Handlers) RequireServiceToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := ContextAPIPrincipal(r)
		if p == nil || p.Type != TokenTypeService {
			http.Error(w, `{"error":"this endpoint is reserved for platform service tokens"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiScopeAllows reports whether any scope grants the path. ScopeAll ("*")
// grants everything; otherwise a scope is a URL-path prefix. An empty scope
// set denies everything (fail-closed).
func apiScopeAllows(scopes []string, path string) bool {
	for _, s := range scopes {
		if s == ScopeAll {
			return true
		}
		if strings.HasPrefix(path, s) {
			return true
		}
	}
	return false
}

// DefaultAutopilotScopes are the REST path prefixes a SERVICE token (kbs_)
// gets when created without explicit scopes — what Autopilot needs (read of
// cluster/resources/insights/events) plus the read-only Kobi MCP endpoint, so
// a service token "just works" against POST /api/v1/mcp without hand-crafted
// scopes. MCP is read-only and a strict subset of the resource reads already
// granted here, so this widens nothing in practice. An API key (kbk_) gets NO
// default scopes: it reaches /mcp only with "/api/v1/mcp" (the form's
// "MCP (read-only tools)" option) or "*".
var DefaultAutopilotScopes = []string{
	"/api/v1/cluster/overview",
	"/api/v1/insights",
	"/api/v1/events",
	"/api/v1/resources",
	"/api/v1/mcp",
}

// apiTokenForbiddenPath refuses, whatever the token's scopes, the paths a
// token must not reach. Returns the reason, or "" when the path is allowed.
//
//   - Token management, for EVERY API token. A token that can issue, edit or
//     revoke tokens can mint itself a wider one, or clear its own cluster
//     list; credentials are managed by a signed-in admin.
//   - Org-wide administration, for an API key narrowed to some clusters.
//     Users, teams, the audit trail, agents, settings, account and billing,
//     and changes to clusters are the whole org's by nature, so a key limited
//     to some clusters has no business there — even with scope "*" and an
//     admin role, which would otherwise hand it everything its list withholds.
func apiTokenForbiddenPath(p *APIPrincipal, method, path string) string {
	if strings.HasPrefix(path, "/api/v1/admin/api-tokens") {
		return "API tokens cannot manage API tokens; sign in as an admin"
	}
	if p.Type != TokenTypeAPIKey || len(p.Clusters) == 0 {
		return ""
	}
	for _, prefix := range narrowedKeyForbiddenPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return "this token is limited to some clusters and cannot use organization-wide administration"
		}
	}
	if method != http.MethodGet && method != http.MethodHead &&
		(path == "/api/v1/clusters" || strings.HasPrefix(path, "/api/v1/clusters/")) {
		return "this token is limited to some clusters and cannot change clusters"
	}
	return ""
}

var narrowedKeyForbiddenPrefixes = []string{
	"/api/v1/admin",
	"/api/v1/platform",
	"/api/v1/users",
	"/api/v1/teams",
	"/api/v1/account",
}
