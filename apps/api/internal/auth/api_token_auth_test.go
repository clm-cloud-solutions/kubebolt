package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func handlersWithAPIStore(t *testing.T) (*Handlers, *APITokenStore) {
	t.Helper()
	s := newTestAPIStore(t)
	h := &Handlers{}
	h.SetAPITokenStore(s)
	return h, s
}

func bearerReq(token string, edge string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/resources/pods", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if edge != "" {
		r.Header.Set(EdgeHeader, edge)
	}
	return r
}

func TestValidateAPIToken_ServiceTokenOK(t *testing.T) {
	h, s := handlersWithAPIStore(t)
	plaintext, tok, _ := s.Issue(context.Background(), TokenTypeService, RoleEditor, []string{"/api/v1/resources"}, "autopilot", "admin", nil)

	claims, p, err := h.validateAPIToken(bearerReq(plaintext, ""), plaintext)
	if err != nil {
		t.Fatalf("validateAPIToken err = %v", err)
	}
	if claims.Role != RoleEditor {
		t.Fatalf("claims.Role = %q, want editor", claims.Role)
	}
	if claims.UserID != "svc:"+tok.ID {
		t.Fatalf("claims.UserID = %q, want svc:%s", claims.UserID, tok.ID)
	}
	// The synthetic Claims must carry the token's org so ResolveTenant resolves
	// API-token callers to their real tenant (not the DefaultTenantName
	// fallback). Empty here (OSS single-tenant store), but the propagation is
	// what matters — under the EE store the token's UUID flows through.
	if claims.TenantID != tok.TenantID {
		t.Fatalf("claims.TenantID = %q, want %q (token org must reach ResolveTenant)", claims.TenantID, tok.TenantID)
	}
	if p == nil || p.Type != TokenTypeService || len(p.Scopes) != 1 {
		t.Fatalf("principal unexpected: %+v", p)
	}
}

func TestValidateAPIToken_ServiceRejectedOverPublicEdge(t *testing.T) {
	h, s := handlersWithAPIStore(t)
	plaintext, _, _ := s.Issue(context.Background(), TokenTypeService, RoleEditor, []string{ScopeAll}, "x", "admin", nil)

	_, _, err := h.validateAPIToken(bearerReq(plaintext, EdgeValuePublic), plaintext)
	if err != ErrTokenEdgeBlocked {
		t.Fatalf("err = %v, want ErrTokenEdgeBlocked", err)
	}
}

func TestValidateAPIToken_APIKeyNotEdgeBound(t *testing.T) {
	h, s := handlersWithAPIStore(t)
	// Customer API keys work over the public edge (that's the point).
	plaintext, _, _ := s.Issue(context.Background(), TokenTypeAPIKey, RoleViewer, []string{ScopeAll}, "x", "admin", nil)

	if _, _, err := h.validateAPIToken(bearerReq(plaintext, EdgeValuePublic), plaintext); err != nil {
		t.Fatalf("apikey over edge err = %v, want nil", err)
	}
}

func TestValidateAPIToken_Revoked(t *testing.T) {
	h, s := handlersWithAPIStore(t)
	plaintext, tok, _ := s.Issue(context.Background(), TokenTypeService, RoleEditor, []string{ScopeAll}, "x", "admin", nil)
	_ = s.Revoke(context.Background(), tok.ID)
	if _, _, err := h.validateAPIToken(bearerReq(plaintext, ""), plaintext); err != ErrTokenRevoked {
		t.Fatalf("err = %v, want ErrTokenRevoked", err)
	}
}

func TestValidateAPIToken_NoStore(t *testing.T) {
	h := &Handlers{} // store not wired
	if _, _, err := h.validateAPIToken(bearerReq("kbs_x", ""), "kbs_x"); err != errAPITokensUnavailable {
		t.Fatalf("err = %v, want errAPITokensUnavailable", err)
	}
}

func TestAPIScopeAllows(t *testing.T) {
	cases := []struct {
		scopes []string
		path   string
		want   bool
	}{
		{[]string{ScopeAll}, "/api/v1/users", true},
		{[]string{"/api/v1/resources"}, "/api/v1/resources/pods", true},
		{[]string{"/api/v1/resources"}, "/api/v1/users", false},
		{[]string{"/api/v1/insights", "/api/v1/events"}, "/api/v1/events", true},
		{nil, "/api/v1/resources", false}, // empty = fail-closed
	}
	for _, c := range cases {
		if got := apiScopeAllows(c.scopes, c.path); got != c.want {
			t.Errorf("apiScopeAllows(%v, %q) = %v, want %v", c.scopes, c.path, got, c.want)
		}
	}
}

func TestEnforceAPITokenScope_Middleware(t *testing.T) {
	h := &Handlers{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := h.EnforceAPITokenScope(next)

	withPrincipal := func(path string, p *APIPrincipal) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if p != nil {
			r = r.WithContext(context.WithValue(r.Context(), apiPrincipalKey, p))
		}
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, r)
		return rec.Code
	}

	// In-scope → allowed.
	if code := withPrincipal("/api/v1/resources/pods", &APIPrincipal{Scopes: []string{"/api/v1/resources"}}); code != http.StatusOK {
		t.Fatalf("in-scope code = %d, want 200", code)
	}
	// Out-of-scope → 403.
	if code := withPrincipal("/api/v1/users", &APIPrincipal{Scopes: []string{"/api/v1/resources"}}); code != http.StatusForbidden {
		t.Fatalf("out-of-scope code = %d, want 403", code)
	}
	// No principal (JWT user) → pass-through.
	if code := withPrincipal("/api/v1/users", nil); code != http.StatusOK {
		t.Fatalf("jwt-user code = %d, want 200", code)
	}
}

// A default service token (DefaultAutopilotScopes) must reach the read-only Kobi
// MCP endpoint, or the documented "create a token, point your MCP host at it"
// flow 403s. Guards the scope added for the MCP server.
func TestDefaultAutopilotScopes_AllowMCP(t *testing.T) {
	if !apiScopeAllows(DefaultAutopilotScopes, "/api/v1/mcp") {
		t.Errorf("default service-token scopes must permit /api/v1/mcp; got %v", DefaultAutopilotScopes)
	}
}
