package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// OptionalAuth is the best-effort identity middleware mounted on public routes
// (e.g. /copilot/config) so an authenticated caller's org is resolved without
// making the route reject anonymous callers. These tests lock the two halves of
// that contract: (1) a valid token establishes claims that ResolveTenant can
// read, and (2) every "no usable token" path stays a non-rejecting pass-through.

// chains OptionalAuth → ResolveTenant → a probe that records the resolved
// tenant, mirroring how the router mounts the pair on /copilot/config.
func optionalAuthProbe(h *Handlers, seen *string, status *int) http.Handler {
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = ContextTenantID(r)
		w.WriteHeader(http.StatusOK)
	})
	chain := h.OptionalAuth(h.ResolveTenant(probe))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, r)
		*status = rec.Code
	})
}

func TestOptionalAuth_ValidTokenResolvesOrg(t *testing.T) {
	svc := testJWTService(time.Hour, 24*time.Hour)
	h := &Handlers{jwt: svc, authEnabled: true}

	tok, err := svc.GenerateAccessToken(&User{ID: "u", Username: "x", Role: RoleViewer, OrgID: "org-acme"})
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	var seen string
	var status int
	r := httptest.NewRequest(http.MethodGet, "/copilot/config", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	optionalAuthProbe(h, &seen, &status).ServeHTTP(httptest.NewRecorder(), r)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if seen != "org-acme" {
		t.Fatalf("resolved tenant = %q, want org-acme (per-org config would fall back to env baseline)", seen)
	}
}

func TestOptionalAuth_NoTokenStaysAnonymous(t *testing.T) {
	// The login-page fetch: no Authorization header. Must not 401, and must
	// resolve to the default tenant (env-baseline path).
	h := &Handlers{jwt: testJWTService(time.Hour, 24*time.Hour), authEnabled: true}

	var seen string
	var status int
	r := httptest.NewRequest(http.MethodGet, "/copilot/config", nil)
	optionalAuthProbe(h, &seen, &status).ServeHTTP(httptest.NewRecorder(), r)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (public route must stay reachable)", status)
	}
	if seen != DefaultTenantName {
		t.Fatalf("resolved tenant = %q, want %q", seen, DefaultTenantName)
	}
}

func TestOptionalAuth_InvalidTokenIsIgnoredNot401(t *testing.T) {
	// A garbage / expired bearer token must be treated as anonymous, NEVER as
	// a 401 — otherwise the public route breaks for stale-session browsers.
	h := &Handlers{jwt: testJWTService(time.Hour, 24*time.Hour), authEnabled: true}

	var seen string
	var status int
	r := httptest.NewRequest(http.MethodGet, "/copilot/config", nil)
	r.Header.Set("Authorization", "Bearer not-a-real-jwt")
	optionalAuthProbe(h, &seen, &status).ServeHTTP(httptest.NewRecorder(), r)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (invalid token must not reject)", status)
	}
	if seen != DefaultTenantName {
		t.Fatalf("resolved tenant = %q, want %q (invalid token must not be trusted)", seen, DefaultTenantName)
	}
}

func TestOptionalAuth_AuthDisabledPassesThrough(t *testing.T) {
	// Auth disabled (dev): pass-through, no claims, default tenant.
	h := &Handlers{authEnabled: false}

	var seen string
	var status int
	r := httptest.NewRequest(http.MethodGet, "/copilot/config", nil)
	optionalAuthProbe(h, &seen, &status).ServeHTTP(httptest.NewRecorder(), r)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if seen != DefaultTenantName {
		t.Fatalf("resolved tenant = %q, want %q", seen, DefaultTenantName)
	}
}
