package auth

import (
	"errors"
	"strings"
	"sync"
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

func TestIssueToken_ReturnsPlaintextWithPrefixAndStoresHash(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plaintext, tok, err := ts.IssueToken(tn.ID, "prod-east", "admin", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		t.Errorf("plaintext missing %q prefix: %q", TokenPrefix, plaintext)
	}
	if tok.Hash == plaintext || tok.Hash == "" {
		t.Errorf("token Hash must be non-empty and != plaintext, got %q", tok.Hash)
	}
	if !strings.HasPrefix(plaintext, tok.Prefix) {
		t.Errorf("Prefix %q must be a prefix of plaintext %q", tok.Prefix, plaintext)
	}
	// Persisted on the tenant
	got, _ := ts.GetTenant(tn.ID)
	if len(got.IngestTokens) != 1 || got.IngestTokens[0].ID != tok.ID {
		t.Errorf("token not persisted on tenant: %+v", got.IngestTokens)
	}
}

func TestIssueToken_TTLSetsExpiration(t *testing.T) {
	ts := newTestTenantsStore(t)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts.nowFn = func() time.Time { return fixed }
	tn, _ := ts.CreateTenant("acme", "team")
	ttl := 24 * time.Hour
	_, tok, err := ts.IssueToken(tn.ID, "short", "admin", &ttl)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(fixed.Add(ttl)) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, fixed.Add(ttl))
	}
}

func TestLookupByToken_HappyPath(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plaintext, tok, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)
	gotTenant, gotTok, err := ts.LookupByToken(plaintext)
	if err != nil {
		t.Fatalf("LookupByToken: %v", err)
	}
	if gotTenant.ID != tn.ID {
		t.Errorf("tenant mismatch: %s vs %s", gotTenant.ID, tn.ID)
	}
	if gotTok.ID != tok.ID {
		t.Errorf("token mismatch: %s vs %s", gotTok.ID, tok.ID)
	}
}

func TestLookupByToken_Malformed(t *testing.T) {
	ts := newTestTenantsStore(t)
	if _, _, err := ts.LookupByToken("nope-no-prefix"); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("expected ErrTokenMalformed, got %v", err)
	}
}

func TestLookupByToken_Unknown(t *testing.T) {
	ts := newTestTenantsStore(t)
	if _, _, err := ts.LookupByToken(TokenPrefix + "doesnotexist"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("expected ErrTokenNotFound, got %v", err)
	}
}

func TestLookupByToken_AfterRevoke(t *testing.T) {
	// Revocation deletes the index entry on purpose, so LookupByToken
	// returns ErrTokenNotFound (fail-fast). The tenant record still
	// carries the revoked token for audit. This test pins the contract.
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plaintext, tok, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)
	if err := ts.RevokeToken(tn.ID, tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, _, err := ts.LookupByToken(plaintext); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("post-revoke lookup err = %v, want ErrTokenNotFound", err)
	}
	// Audit trail still on the tenant
	got, _ := ts.GetTenant(tn.ID)
	if got.IngestTokens[0].RevokedAt == nil {
		t.Error("revoked token must have RevokedAt set on the tenant record")
	}
}

func TestLookupByToken_Expired(t *testing.T) {
	ts := newTestTenantsStore(t)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts.nowFn = func() time.Time { return fixed }
	tn, _ := ts.CreateTenant("acme", "team")
	ttl := time.Hour
	plaintext, _, _ := ts.IssueToken(tn.ID, "prod", "admin", &ttl)

	ts.nowFn = func() time.Time { return fixed.Add(2 * time.Hour) }
	if _, _, err := ts.LookupByToken(plaintext); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestLookupByToken_DisabledTenant(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plaintext, _, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)
	if _, err := ts.UpdateTenant(tn.ID, func(t *Tenant) error { t.Disabled = true; return nil }); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if _, _, err := ts.LookupByToken(plaintext); !errors.Is(err, ErrTenantDisabled) {
		t.Errorf("expected ErrTenantDisabled, got %v", err)
	}
}

func TestRotateToken_PreservesLabelAndIssuesNew(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plain1, tok1, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)

	plain2, tok2, err := ts.RotateToken(tn.ID, tok1.ID, "admin")
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if plain1 == plain2 {
		t.Error("rotated token must produce a new plaintext")
	}
	if tok1.ID == tok2.ID {
		t.Error("rotated token must have a new ID")
	}
	if tok2.Label != tok1.Label {
		t.Errorf("rotation should preserve label: %q -> %q", tok1.Label, tok2.Label)
	}
	if _, _, err := ts.LookupByToken(plain1); err == nil {
		t.Error("old plaintext must not lookup after rotation")
	}
	if _, _, err := ts.LookupByToken(plain2); err != nil {
		t.Errorf("new plaintext should lookup, got %v", err)
	}
}

func TestRotateToken_PreservesTTLWindow(t *testing.T) {
	ts := newTestTenantsStore(t)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts.nowFn = func() time.Time { return fixed }
	tn, _ := ts.CreateTenant("acme", "team")
	ttl := 7 * 24 * time.Hour
	_, tok1, _ := ts.IssueToken(tn.ID, "prod", "admin", &ttl)

	ts.nowFn = func() time.Time { return fixed.Add(time.Hour) }
	_, tok2, err := ts.RotateToken(tn.ID, tok1.ID, "admin")
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if tok2.ExpiresAt == nil {
		t.Fatal("rotated token lost expiration")
	}
	wantExp := fixed.Add(time.Hour).Add(ttl)
	if !tok2.ExpiresAt.Equal(wantExp) {
		t.Errorf("ExpiresAt = %v, want %v (creation+ttl)", tok2.ExpiresAt, wantExp)
	}
}

func TestMarkUsed_DebouncedToOncePerMinute(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	_, tok, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if err := ts.MarkUsed(tn.ID, tok.ID, base); err != nil {
		t.Fatalf("MarkUsed first: %v", err)
	}
	if err := ts.MarkUsed(tn.ID, tok.ID, base.Add(10*time.Second)); err != nil {
		t.Fatalf("MarkUsed second (debounced): %v", err)
	}
	got, _ := ts.GetTenant(tn.ID)
	if got.IngestTokens[0].LastUsedAt == nil || !got.IngestTokens[0].LastUsedAt.Equal(base) {
		t.Errorf("LastUsedAt within debounce window = %v, want %v", got.IngestTokens[0].LastUsedAt, base)
	}

	later := base.Add(2 * time.Minute)
	if err := ts.MarkUsed(tn.ID, tok.ID, later); err != nil {
		t.Fatalf("MarkUsed past debounce: %v", err)
	}
	got, _ = ts.GetTenant(tn.ID)
	if got.IngestTokens[0].LastUsedAt == nil || !got.IngestTokens[0].LastUsedAt.Equal(later) {
		t.Errorf("LastUsedAt after debounce = %v, want %v", got.IngestTokens[0].LastUsedAt, later)
	}
}

func TestConcurrentTokenIssue_NoCollisions(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")

	var wg sync.WaitGroup
	const N = 25
	plains := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, _, err := ts.IssueToken(tn.ID, "concurrent", "admin", nil)
			plains[i] = p
			errs[i] = err
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, p := range plains {
		if errs[i] != nil {
			t.Errorf("concurrent IssueToken[%d]: %v", i, errs[i])
			continue
		}
		if seen[p] {
			t.Errorf("collision: plaintext %q issued twice", p)
		}
		seen[p] = true
	}
}

func TestDeleteTenant_ClearsBothIndexes(t *testing.T) {
	ts := newTestTenantsStore(t)
	tn, _ := ts.CreateTenant("acme", "team")
	plain, _, _ := ts.IssueToken(tn.ID, "prod", "admin", nil)

	if err := ts.DeleteTenant(tn.ID); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if _, err := ts.getTenantByName("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("name index not cleared: %v", err)
	}
	if _, _, err := ts.LookupByToken(plain); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("token index not cleared: %v", err)
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

func TestActive_Helper(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rev := now.Add(-time.Hour)
	exp := now.Add(-time.Minute)
	cases := []struct {
		name string
		tok  IngestToken
		want bool
	}{
		{"fresh", IngestToken{}, true},
		{"revoked", IngestToken{RevokedAt: &rev}, false},
		{"expired", IngestToken{ExpiresAt: &exp}, false},
		{"future expiry", IngestToken{ExpiresAt: ptrTime(now.Add(time.Hour))}, true},
	}
	for _, c := range cases {
		if got := c.tok.Active(now); got != c.want {
			t.Errorf("%s: Active = %v, want %v", c.name, got, c.want)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
