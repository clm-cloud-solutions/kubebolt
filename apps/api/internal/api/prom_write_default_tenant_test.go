package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests for the default-tenant fallback behavior added after the yagan
// rate-limit investigation. Before the fix, every permissive /
// disabled fallback returned (nil tenant, true) — downstream code
// then bucketed the request as "anonymous", and any custom override
// the operator had set on the install's default tenant in the UI was
// dead code. After the fix, those fallbacks resolve to the default
// tenant when TenantsStore is wired, so the operator's overrides
// apply automatically to unauthenticated traffic in permissive /
// disabled modes.
//
// The matrix this file pins:
//
//   mode        | TenantsStore | Bearer            | expected tenant
//   ------------|--------------|-------------------|----------------
//   disabled    | wired        | (ignored)         | default tenant
//   disabled    | nil          | (ignored)         | nil (legacy)
//   permissive  | wired        | absent            | default tenant
//   permissive  | wired        | empty ("Bearer ") | default tenant
//   permissive  | wired        | invalid           | default tenant
//   permissive  | wired        | valid             | tenant of token
//   permissive  | nil          | absent            | nil (legacy)
//   enforced    | wired        | absent            | nil + 401 (unchanged)
//   enforced    | wired        | invalid           | nil + 401 (unchanged)
//
// The "default tenant" is the one TenantsStore.ensureDefaultTenant
// seeds on construction — every install has one as long as
// TenantsStore is wired.

// fetchDefaultTenantID returns the seeded default tenant's UUID so
// tests can assert that the fallback resolved to THAT specific tenant
// and not the test-created one (`test-tenant` from
// newTenantsStoreWithToken).
func fetchDefaultTenantID(t *testing.T, h *handlers) string {
	t.Helper()
	dt, err := h.tenantsStore.GetDefaultTenant()
	if err != nil {
		t.Fatalf("GetDefaultTenant: %v", err)
	}
	return dt.ID
}

func TestAuthenticatePromWrite_Disabled_WithStore_ReturnsDefaultTenant(t *testing.T) {
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthDisabled}
	wantID := fetchDefaultTenantID(t, h)

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if !ok {
		t.Fatalf("expected ok=true in disabled mode, got false")
	}
	if tenant == nil {
		t.Fatalf("expected default tenant, got nil — fallback regressed to anonymous")
	}
	if tenant.ID != wantID {
		t.Errorf("got tenant %q, want default tenant %q", tenant.ID, wantID)
	}
}

func TestAuthenticatePromWrite_Disabled_NoStore_ReturnsNil(t *testing.T) {
	// Edge case: KUBEBOLT_AUTH_ENABLED=false → no TenantsStore.
	// Legacy "anonymous" behavior preserved.
	h := &handlers{tenantsStore: nil, promWriteAuthMode: promWriteAuthDisabled}
	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if !ok {
		t.Fatalf("expected ok=true in disabled mode, got false")
	}
	if tenant != nil {
		t.Errorf("expected nil tenant when TenantsStore unwired, got %v", tenant)
	}
}

func TestAuthenticatePromWrite_Permissive_NoBearer_ReturnsDefaultTenant(t *testing.T) {
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthPermissive}
	wantID := fetchDefaultTenantID(t, h)

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if !ok || tenant == nil {
		t.Fatalf("expected ok=true with default tenant on permissive+no-bearer, got ok=%v tenant=%v", ok, tenant)
	}
	if tenant.ID != wantID {
		t.Errorf("got tenant %q, want default %q", tenant.ID, wantID)
	}
}

func TestAuthenticatePromWrite_Permissive_EmptyBearer_ReturnsDefaultTenant(t *testing.T) {
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthPermissive}
	wantID := fetchDefaultTenantID(t, h)

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if !ok || tenant == nil {
		t.Fatalf("expected ok=true with default tenant on permissive+empty-bearer, got ok=%v tenant=%v", ok, tenant)
	}
	if tenant.ID != wantID {
		t.Errorf("got tenant %q, want default %q", tenant.ID, wantID)
	}
}

func TestAuthenticatePromWrite_Permissive_BadBearer_ReturnsDefaultTenant(t *testing.T) {
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthPermissive}
	wantID := fetchDefaultTenantID(t, h)

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer kbtok_v1_definitely-not-real")
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if !ok || tenant == nil {
		t.Fatalf("expected ok=true with default tenant on permissive+bad-bearer, got ok=%v tenant=%v", ok, tenant)
	}
	if tenant.ID != wantID {
		t.Errorf("got tenant %q, want default %q", tenant.ID, wantID)
	}
}

func TestAuthenticatePromWrite_Permissive_ValidBearer_ReturnsTokenTenant(t *testing.T) {
	// Sanity-check that the VALID-token path still picks up the
	// token's tenant rather than defaulting. Without this, the fix
	// might accidentally clobber a real authenticated identity.
	store, its, plaintext := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthPermissive}
	defaultID := fetchDefaultTenantID(t, h)

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if !ok || tenant == nil {
		t.Fatalf("expected ok=true with token-resolved tenant, got ok=%v tenant=%v", ok, tenant)
	}
	if tenant.ID == defaultID {
		t.Errorf("token-resolved tenant collapsed onto default tenant — valid auth got clobbered")
	}
}

func TestAuthenticatePromWrite_Permissive_NoStore_ReturnsNil(t *testing.T) {
	// Edge case: permissive mode without a TenantsStore — preserve
	// the legacy nil-tenant path so downstream code still treats the
	// request as anonymous. KUBEBOLT_AUTH_ENABLED=false combined
	// with permissive remote_write is an unusual but valid mix.
	h := &handlers{tenantsStore: nil, promWriteAuthMode: promWriteAuthPermissive}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if !ok {
		t.Fatalf("expected ok=true in permissive+no-store, got false")
	}
	if tenant != nil {
		t.Errorf("expected nil tenant when no store, got %v", tenant)
	}
}

func TestAuthenticatePromWrite_Enforced_NoBearer_StillRejects(t *testing.T) {
	// Regression guard — the fix must NOT change enforced mode.
	// Missing bearer still 401s.
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthEnforced}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if ok {
		t.Fatalf("enforced+no-bearer should reject, got ok=true")
	}
	if tenant != nil {
		t.Errorf("expected nil tenant on enforced rejection, got %v", tenant)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticatePromWrite_Enforced_BadBearer_StillRejects(t *testing.T) {
	// Same regression guard for invalid bearer.
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its, promWriteAuthMode: promWriteAuthEnforced}

	req := httptest.NewRequest(http.MethodPost, "/prom/write", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer kbtok_v1_invalid")
	rec := httptest.NewRecorder()
	tenant, ok := h.authenticatePromWrite(rec, req)

	if ok {
		t.Fatalf("enforced+bad-bearer should reject, got ok=true")
	}
	if tenant != nil {
		t.Errorf("expected nil tenant on enforced rejection, got %v", tenant)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
