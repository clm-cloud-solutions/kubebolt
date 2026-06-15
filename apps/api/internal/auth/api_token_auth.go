package auth

import (
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
}

const apiPrincipalKey contextKey = "auth-api-principal"

// ContextAPIPrincipal returns the API-token principal for the request, or
// nil when the caller authenticated via a user-session JWT (or is
// unauthenticated).
func ContextAPIPrincipal(r *http.Request) *APIPrincipal {
	p, _ := r.Context().Value(apiPrincipalKey).(*APIPrincipal)
	return p
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
	}
	p := &APIPrincipal{
		TokenID:   tok.ID,
		Type:      tok.Type,
		Role:      tok.Role,
		Scopes:    tok.Scopes,
		TenantID:  tok.TenantID,
		ClusterID: tok.ClusterID,
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
		if apiScopeAllows(p.Scopes, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, `{"error":"token scope does not permit this path"}`, http.StatusForbidden)
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

// DefaultAutopilotScopes are the REST path prefixes a service token gets when
// created without explicit scopes — what Autopilot needs (read of
// cluster/resources/insights/events) plus the read-only Kobi MCP endpoint, so a
// token minted via Admin → API Tokens "just works" against POST /api/v1/mcp
// without the operator having to hand-craft scopes. MCP is read-only and a
// strict subset of the resource reads already granted here, so this widens
// nothing in practice. A token scoped to "*" also covers it.
var DefaultAutopilotScopes = []string{
	"/api/v1/cluster/overview",
	"/api/v1/insights",
	"/api/v1/events",
	"/api/v1/resources",
	"/api/v1/mcp",
}
