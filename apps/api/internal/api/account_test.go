package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/usage"
)

func newAccountTenants(t *testing.T) *auth.TenantsStore {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewStore(dir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ts, err := auth.NewTenantsStore(store.DB())
	if err != nil {
		t.Fatalf("auth.NewTenantsStore: %v", err)
	}
	return ts
}

// TestAccountPlan_DefaultTenant verifies GET /account/plan returns the org's
// tenant info. With the OSS sentinel org ("default"), the handler falls back to
// the auto-seeded default tenant.
func TestAccountPlan_DefaultTenant(t *testing.T) {
	ts := newAccountTenants(t)
	h := &handlers{tenantsStore: ts}

	req := httptest.NewRequest(http.MethodGet, "/account/plan", nil)
	req = req.WithContext(auth.WithTenantID(req.Context(), auth.DefaultTenantName))
	rec := httptest.NewRecorder()
	h.handleAccountPlan(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var resp accountPlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != auth.DefaultTenantName || resp.ID == "" {
		t.Fatalf("resp = %#v, want default tenant", resp)
	}
}

// TestAccountPlan_ResolvedOrg verifies the handler returns a specific tenant
// when context carries a real org ID (the EE multi-org path).
func TestAccountPlan_ResolvedOrg(t *testing.T) {
	ts := newAccountTenants(t)
	tn, err := ts.CreateTenant("Acme", "free")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	h := &handlers{tenantsStore: ts}

	req := httptest.NewRequest(http.MethodGet, "/account/plan", nil)
	req = req.WithContext(auth.WithTenantID(req.Context(), tn.ID))
	rec := httptest.NewRecorder()
	h.handleAccountPlan(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp accountPlanResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ID != tn.ID || resp.Name != "Acme" || resp.Plan != "free" {
		t.Fatalf("resp = %#v, want Acme/free/%s", resp, tn.ID)
	}
}

// TestAccountPlan_NoStore returns 503 when no tenant store is wired.
func TestAccountPlan_NoStore(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/account/plan", nil)
	rec := httptest.NewRecorder()
	h.handleAccountPlan(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestAccountUsage_NoopEmpty verifies GET /account/usage returns an empty list
// against the OSS Noop usage store.
func TestAccountUsage_NoopEmpty(t *testing.T) {
	h := &handlers{usage: usage.NewNoopUsageStore()}

	req := httptest.NewRequest(http.MethodGet, "/account/usage", nil)
	req = req.WithContext(auth.WithTenantID(req.Context(), "some-org"))
	rec := httptest.NewRecorder()
	h.handleAccountUsage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp accountUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage must be a non-nil (empty) array")
	}
	if len(resp.Usage) != 0 {
		t.Fatalf("usage = %d points, want 0", len(resp.Usage))
	}
}

// TestAccountUsage_NilStore degrades to an empty list when no usage store is
// wired at all (auth/persistence disabled).
func TestAccountUsage_NilStore(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/account/usage", nil)
	rec := httptest.NewRecorder()
	h.handleAccountUsage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
