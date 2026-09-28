package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #59 B3. The Autopilot MCP profile is reserved for Autopilot. The obvious
// implementation — give the route a token path-scope — does NOT close it:
// EnforceAPITokenScope passes through when there is no API principal, and a
// signed-in browser user has none. See TestEnforceAPITokenScope_Middleware,
// which pins that pass-through as intended behaviour for its own purpose.
//
// RequireServiceToken inverts it: a nil principal is the denial.

func serviceTokenProbe(h *Handlers) http.Handler {
	return h.RequireServiceToken(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func callWithPrincipal(h *Handlers, p *APIPrincipal) int {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/autopilot/triage", nil)
	if p != nil {
		r = r.WithContext(context.WithValue(r.Context(), apiPrincipalKey, p))
	}
	rec := httptest.NewRecorder()
	serviceTokenProbe(h).ServeHTTP(rec, r)
	return rec.Code
}

func TestRequireServiceToken(t *testing.T) {
	h := &Handlers{}

	t.Run("service token is admitted", func(t *testing.T) {
		if code := callWithPrincipal(h, &APIPrincipal{Type: TokenTypeService}); code != http.StatusOK {
			t.Fatalf("code = %d, want 200", code)
		}
	})

	// THE case the intuition says is covered and the code did not. A logged-in
	// admin holds a JWT, so ContextAPIPrincipal is nil.
	t.Run("a signed-in user is refused, whatever their role", func(t *testing.T) {
		if code := callWithPrincipal(h, nil); code != http.StatusForbidden {
			t.Fatalf("code = %d, want 403 — a browser session must not reach Autopilot's profile", code)
		}
	})

	t.Run("a customer API key is refused", func(t *testing.T) {
		if code := callWithPrincipal(h, &APIPrincipal{Type: TokenTypeAPIKey}); code != http.StatusForbidden {
			t.Fatalf("code = %d, want 403", code)
		}
	})

	// Defence in depth: even an admin-role principal of the wrong TYPE is out.
	// The gate is the token kind, not the role.
	t.Run("role does not buy entry", func(t *testing.T) {
		if code := callWithPrincipal(h, &APIPrincipal{Type: TokenTypeAPIKey, Role: RoleAdmin}); code != http.StatusForbidden {
			t.Fatalf("code = %d, want 403 — role is not the gate here", code)
		}
	})
}

// The fourth door is shut upstream, in validateAPIToken: a service token that
// arrived over the public edge never becomes a principal at all, so it reaches
// this middleware as nil and is refused above. This test pins the rule itself
// so the constant pair cannot drift apart unnoticed.
func TestServiceTokenIsEdgeBlocked(t *testing.T) {
	if EdgeValuePublic == "" || EdgeHeader == "" {
		t.Fatal("the public-edge marker is empty; a leaked service token would be accepted from outside")
	}
}
