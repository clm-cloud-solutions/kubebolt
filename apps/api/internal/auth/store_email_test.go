package auth

import (
	"context"
	"testing"
)

// TestGetUserByEmail covers the email-as-global-id lookup (Track D login
// identity) on the Bolt store: found by email, not-found for unknown, and
// empty email is never a match.
func TestGetUserByEmail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "alice", "alice@acme.io", "Alice", "password123", RoleEditor)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := s.GetUserByEmail(ctx, "alice@acme.io")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("GetUserByEmail returned %s, want %s", got.ID, u.ID)
	}

	if _, err := s.GetUserByEmail(ctx, "nobody@acme.io"); err == nil {
		t.Fatal("unknown email should be 'user not found'")
	}
	if _, err := s.GetUserByEmail(ctx, ""); err == nil {
		t.Fatal("empty email should never match a user")
	}
}
