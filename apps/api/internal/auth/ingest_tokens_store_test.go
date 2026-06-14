package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestIngestTokenStore(t *testing.T) *BoltIngestTokenStore {
	t.Helper()
	s := newTestStore(t)
	its, err := NewIngestTokenStore(s.DB())
	if err != nil {
		t.Fatalf("NewIngestTokenStore: %v", err)
	}
	return its
}

func TestIngestIssue_ReturnsPlaintextWithPrefixAndStoresHash(t *testing.T) {
	its := newTestIngestTokenStore(t)
	plaintext, tok, err := its.Issue("tenant-1", "", "prod-east", "admin", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
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
	if tok.TenantID != "tenant-1" {
		t.Errorf("TenantID = %q, want tenant-1", tok.TenantID)
	}
	// Persisted under the tenant
	got, _ := its.ListByTenant("tenant-1")
	if len(got) != 1 || got[0].ID != tok.ID {
		t.Errorf("token not persisted: %+v", got)
	}
}

func TestIngestIssue_TTLSetsExpiration(t *testing.T) {
	its := newTestIngestTokenStore(t)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	its.nowFn = func() time.Time { return fixed }
	ttl := 24 * time.Hour
	_, tok, err := its.Issue("tenant-1", "", "short", "admin", &ttl)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(fixed.Add(ttl)) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, fixed.Add(ttl))
	}
}

func TestIngestLookup_HappyPath(t *testing.T) {
	its := newTestIngestTokenStore(t)
	plaintext, tok, _ := its.Issue("tenant-1", "", "prod", "admin", nil)
	gotTok, err := its.Lookup(plaintext)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if gotTok.ID != tok.ID {
		t.Errorf("token mismatch: %s vs %s", gotTok.ID, tok.ID)
	}
	if gotTok.TenantID != "tenant-1" {
		t.Errorf("TenantID = %q, want tenant-1", gotTok.TenantID)
	}
}

func TestIngestLookup_Malformed(t *testing.T) {
	its := newTestIngestTokenStore(t)
	if _, err := its.Lookup("nope-no-prefix"); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("expected ErrTokenMalformed, got %v", err)
	}
}

func TestIngestLookup_Unknown(t *testing.T) {
	its := newTestIngestTokenStore(t)
	if _, err := its.Lookup(TokenPrefix + "doesnotexist"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("expected ErrTokenNotFound, got %v", err)
	}
}

func TestIngestLookup_AfterRevoke(t *testing.T) {
	// Revocation deletes the index entry on purpose, so Lookup returns
	// ErrTokenNotFound (fail-fast). The token record is kept (RevokedAt set)
	// for audit. This test pins the contract.
	its := newTestIngestTokenStore(t)
	plaintext, tok, _ := its.Issue("tenant-1", "", "prod", "admin", nil)
	if err := its.Revoke("tenant-1", tok.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := its.Lookup(plaintext); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("post-revoke lookup err = %v, want ErrTokenNotFound", err)
	}
	// Audit trail still present
	got, _ := its.ListByTenant("tenant-1")
	if len(got) != 1 || got[0].RevokedAt == nil {
		t.Errorf("revoked token must keep RevokedAt set: %+v", got)
	}
}

func TestIngestLookup_Expired(t *testing.T) {
	its := newTestIngestTokenStore(t)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	its.nowFn = func() time.Time { return fixed }
	ttl := time.Hour
	plaintext, _, _ := its.Issue("tenant-1", "", "prod", "admin", &ttl)

	its.nowFn = func() time.Time { return fixed.Add(2 * time.Hour) }
	if _, err := its.Lookup(plaintext); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestIngestRotate_PreservesLabelAndIssuesNew(t *testing.T) {
	its := newTestIngestTokenStore(t)
	plain1, tok1, _ := its.Issue("tenant-1", "", "prod", "admin", nil)

	plain2, tok2, err := its.Rotate("tenant-1", tok1.ID, "admin")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
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
	if _, err := its.Lookup(plain1); err == nil {
		t.Error("old plaintext must not lookup after rotation")
	}
	if _, err := its.Lookup(plain2); err != nil {
		t.Errorf("new plaintext should lookup, got %v", err)
	}
}

func TestIngestRotate_PreservesTTLWindow(t *testing.T) {
	its := newTestIngestTokenStore(t)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	its.nowFn = func() time.Time { return fixed }
	ttl := 7 * 24 * time.Hour
	_, tok1, _ := its.Issue("tenant-1", "", "prod", "admin", &ttl)

	its.nowFn = func() time.Time { return fixed.Add(time.Hour) }
	_, tok2, err := its.Rotate("tenant-1", tok1.ID, "admin")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if tok2.ExpiresAt == nil {
		t.Fatal("rotated token lost expiration")
	}
	wantExp := fixed.Add(time.Hour).Add(ttl)
	if !tok2.ExpiresAt.Equal(wantExp) {
		t.Errorf("ExpiresAt = %v, want %v (creation+ttl)", tok2.ExpiresAt, wantExp)
	}
}

func TestIngestMarkUsed_DebouncedToOncePerMinute(t *testing.T) {
	its := newTestIngestTokenStore(t)
	_, tok, _ := its.Issue("tenant-1", "", "prod", "admin", nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if err := its.MarkUsed("tenant-1", tok.ID, base); err != nil {
		t.Fatalf("MarkUsed first: %v", err)
	}
	if err := its.MarkUsed("tenant-1", tok.ID, base.Add(10*time.Second)); err != nil {
		t.Fatalf("MarkUsed second (debounced): %v", err)
	}
	got, _ := its.ListByTenant("tenant-1")
	if got[0].LastUsedAt == nil || !got[0].LastUsedAt.Equal(base) {
		t.Errorf("LastUsedAt within debounce window = %v, want %v", got[0].LastUsedAt, base)
	}

	later := base.Add(2 * time.Minute)
	if err := its.MarkUsed("tenant-1", tok.ID, later); err != nil {
		t.Fatalf("MarkUsed past debounce: %v", err)
	}
	got, _ = its.ListByTenant("tenant-1")
	if got[0].LastUsedAt == nil || !got[0].LastUsedAt.Equal(later) {
		t.Errorf("LastUsedAt after debounce = %v, want %v", got[0].LastUsedAt, later)
	}
}

func TestConcurrentIngestIssue_NoCollisions(t *testing.T) {
	its := newTestIngestTokenStore(t)

	var wg sync.WaitGroup
	const N = 25
	plains := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, _, err := its.Issue("tenant-1", "", "concurrent", "admin", nil)
			plains[i] = p
			errs[i] = err
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, p := range plains {
		if errs[i] != nil {
			t.Errorf("concurrent Issue[%d]: %v", i, errs[i])
			continue
		}
		if seen[p] {
			t.Errorf("collision: plaintext %q issued twice", p)
		}
		seen[p] = true
	}
}

func TestIngestActive_Helper(t *testing.T) {
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
