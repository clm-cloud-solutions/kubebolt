package auth

import (
	"errors"
	"testing"
	"time"
)

func newTestTenantsStore(t *testing.T) *TenantsStore {
	t.Helper()
	s := newTestStore(t)
	ts, err := NewTenantsStore(s.DB())
	if err != nil {
		t.Fatalf("NewTenantsStore: %v", err)
	}
	return ts
}

func TestNewTenantsStore_SeedsDefault(t *testing.T) {
	ts := newTestTenantsStore(t)
	tenants, err := ts.ListTenants()
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("expected exactly the default tenant, got %d: %#v", len(tenants), tenants)
	}
	if tenants[0].Name != DefaultTenantName {
		t.Errorf("default tenant name = %q, want %q", tenants[0].Name, DefaultTenantName)
	}
}

func TestNewTenantsStore_DefaultIdempotent(t *testing.T) {
	s := newTestStore(t)
	ts1, err := NewTenantsStore(s.DB())
	if err != nil {
		t.Fatalf("first NewTenantsStore: %v", err)
	}
	tenants1, _ := ts1.ListTenants()

	ts2, err := NewTenantsStore(s.DB())
	if err != nil {
		t.Fatalf("second NewTenantsStore: %v", err)
	}
	tenants2, _ := ts2.ListTenants()

	if len(tenants1) != 1 || len(tenants2) != 1 {
		t.Fatalf("expected 1 tenant after re-init, got %d/%d", len(tenants1), len(tenants2))
	}
	if tenants1[0].ID != tenants2[0].ID {
		t.Errorf("default tenant ID changed across re-init: %s -> %s", tenants1[0].ID, tenants2[0].ID)
	}
}

func TestCreateTenant_DuplicateNameCaseInsensitive(t *testing.T) {
	ts := newTestTenantsStore(t)
	if _, err := ts.CreateTenant("acme", "team"); err != nil {
		t.Fatalf("first CreateTenant: %v", err)
	}
	if _, err := ts.CreateTenant("ACME", "team"); !errors.Is(err, ErrTenantExists) {
		t.Errorf("expected ErrTenantExists for case-insensitive duplicate, got %v", err)
	}
}

func TestCreateTenant_EmptyNameRejected(t *testing.T) {
	ts := newTestTenantsStore(t)
	if _, err := ts.CreateTenant("   ", "team"); err == nil {
		t.Error("expected error for blank tenant name")
	}
}

func TestDeleteTenant_ClearsNameIndex(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")

	if err := ts.DeleteTenant(tn.ID); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if _, err := ts.getTenantByName("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("name index not cleared: %v", err)
	}
}

func TestUpdateTenant_NameIndexRewrite(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	if _, err := ts.UpdateTenant(tn.ID, func(t *Tenant) error { t.Name = "Acme Corp"; return nil }); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if _, err := ts.getTenantByName("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("old name index entry should be gone, got %v", err)
	}
	got, err := ts.getTenantByName("Acme Corp")
	if err != nil {
		t.Fatalf("new name lookup: %v", err)
	}
	if got.ID != tn.ID {
		t.Errorf("new name resolves to wrong tenant: %s vs %s", got.ID, tn.ID)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
