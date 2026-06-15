package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func newTestAPIStore(t *testing.T) *APITokenStore {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "api.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("open bolt: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewAPITokenStore(db)
	if err != nil {
		t.Fatalf("NewAPITokenStore: %v", err)
	}
	return s
}

func TestAPIToken_IssueAndLookup(t *testing.T) {
	s := newTestAPIStore(t)
	scopes := []string{"/api/v1/resources", "/api/v1/insights"}
	plaintext, tok, err := s.Issue(context.Background(), TokenTypeService, RoleEditor, scopes, "autopilot", "admin-1", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(plaintext, ServiceTokenPrefix) {
		t.Fatalf("plaintext %q lacks prefix %q", plaintext, ServiceTokenPrefix)
	}
	if !strings.HasPrefix(tok.Prefix, ServiceTokenPrefix) || len(tok.Prefix) != len(ServiceTokenPrefix)+8 {
		t.Fatalf("display prefix %q unexpected", tok.Prefix)
	}

	got, err := s.Lookup(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.ID != tok.ID || got.Role != RoleEditor || got.Type != TokenTypeService {
		t.Fatalf("looked-up token mismatch: %+v", got)
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("scopes = %v, want 2", got.Scopes)
	}
}

func TestAPIToken_LookupRejectsWrongPrefix(t *testing.T) {
	s := newTestAPIStore(t)
	// Ingest-token prefix (kb_) must NOT validate against the REST store.
	if _, err := s.Lookup(context.Background(), "kb_deadbeef"); err != ErrTokenMalformed {
		t.Fatalf("kb_ lookup err = %v, want ErrTokenMalformed", err)
	}
	if _, err := s.Lookup(context.Background(), "not-a-token"); err != ErrTokenMalformed {
		t.Fatalf("garbage lookup err = %v, want ErrTokenMalformed", err)
	}
	// Well-formed prefix but unknown secret.
	if _, err := s.Lookup(context.Background(), ServiceTokenPrefix + "unknownsecret"); err != ErrTokenNotFound {
		t.Fatalf("unknown kbs_ lookup err = %v, want ErrTokenNotFound", err)
	}
}

func TestAPIToken_Revoke(t *testing.T) {
	s := newTestAPIStore(t)
	plaintext, tok, _ := s.Issue(context.Background(), TokenTypeService, RoleEditor, []string{ScopeAll}, "x", "admin", nil)
	if err := s.Revoke(context.Background(), tok.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := s.Lookup(context.Background(), plaintext); err != ErrTokenRevoked {
		t.Fatalf("post-revoke lookup err = %v, want ErrTokenRevoked", err)
	}
	if err := s.Revoke(context.Background(), "no-such-id"); err != ErrTokenNotFound {
		t.Fatalf("revoke unknown err = %v, want ErrTokenNotFound", err)
	}
}

func TestAPIToken_Expired(t *testing.T) {
	s := newTestAPIStore(t)
	base := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return base }
	ttl := time.Hour
	plaintext, _, _ := s.Issue(context.Background(), TokenTypeService, RoleEditor, []string{ScopeAll}, "x", "admin", &ttl)

	// Within window: valid.
	if _, err := s.Lookup(context.Background(), plaintext); err != nil {
		t.Fatalf("in-window lookup err = %v, want nil", err)
	}
	// After expiry: rejected.
	s.nowFn = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := s.Lookup(context.Background(), plaintext); err != ErrTokenExpired {
		t.Fatalf("expired lookup err = %v, want ErrTokenExpired", err)
	}
}

func TestAPIToken_List(t *testing.T) {
	s := newTestAPIStore(t)
	_, _, _ = s.Issue(context.Background(), TokenTypeService, RoleEditor, nil, "a", "admin", nil)
	_, _, _ = s.Issue(context.Background(), TokenTypeAPIKey, RoleViewer, nil, "b", "admin", nil)
	toks, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(toks) != 2 {
		t.Fatalf("List len = %d, want 2", len(toks))
	}
}

func TestIsAPIToken(t *testing.T) {
	cases := map[string]bool{
		"kbs_abc": true,
		"kbk_abc": true,
		"kb_abc":  false, // ingest token
		"eyJhbG":  false, // JWT-ish
		"":        false,
	}
	for in, want := range cases {
		if got := IsAPIToken(in); got != want {
			t.Errorf("IsAPIToken(%q) = %v, want %v", in, got, want)
		}
	}
}
