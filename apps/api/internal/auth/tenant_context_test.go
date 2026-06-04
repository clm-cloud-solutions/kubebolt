package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func reqWithClaims(c *Claims) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if c != nil {
		r = r.WithContext(context.WithValue(r.Context(), claimsKey, c))
	}
	return r
}

func TestContextTenantID_DefaultsWhenNoClaims(t *testing.T) {
	// No claims at all (e.g. auth disabled / public route): must default.
	if got := ContextTenantID(reqWithClaims(nil)); got != DefaultTenantName {
		t.Fatalf("ContextTenantID with no claims = %q, want %q", got, DefaultTenantName)
	}
}

func TestContextTenantID_DefaultsWhenClaimsHaveNoTenant(t *testing.T) {
	// OSS-issued token: no TenantID → default.
	if got := ContextTenantID(reqWithClaims(&Claims{UserID: "u1"})); got != DefaultTenantName {
		t.Fatalf("ContextTenantID with empty-tenant claims = %q, want %q", got, DefaultTenantName)
	}
}

func TestContextTenantID_HonorsClaimTenant(t *testing.T) {
	// Forward-compat: an EE/SaaS token carrying a tenant is honored.
	if got := ContextTenantID(reqWithClaims(&Claims{UserID: "u1", TenantID: "acme"})); got != "acme" {
		t.Fatalf("ContextTenantID with tenant claim = %q, want %q", got, "acme")
	}
}

func TestContextTenantID_PrefersStashedValue(t *testing.T) {
	// The middleware-stamped value wins over claims (resolver authority).
	r := reqWithClaims(&Claims{TenantID: "from-claim"})
	r = r.WithContext(context.WithValue(r.Context(), tenantKey, "from-resolver"))
	if got := ContextTenantID(r); got != "from-resolver" {
		t.Fatalf("ContextTenantID = %q, want stashed %q", got, "from-resolver")
	}
}

func TestResolveTenantMiddleware_StampsDefaultInOSS(t *testing.T) {
	var seen string
	h := (&Handlers{}).ResolveTenant(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = ContextTenantID(r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), reqWithClaims(nil))
	if seen != DefaultTenantName {
		t.Fatalf("middleware stamped %q, want %q", seen, DefaultTenantName)
	}
}

func TestResolveTenantMiddleware_StampsClaimTenant(t *testing.T) {
	var seen string
	h := (&Handlers{}).ResolveTenant(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = ContextTenantID(r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), reqWithClaims(&Claims{TenantID: "acme"}))
	if seen != "acme" {
		t.Fatalf("middleware stamped %q, want %q", seen, "acme")
	}
}
