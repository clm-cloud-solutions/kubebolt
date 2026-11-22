package auth

import (
	"context"
	"testing"
	"time"
)

// Expired refresh tokens were never deleted: logout and password changes
// removed a user's tokens, but a session that simply lapsed kept its row.

func TestStore_PruneExpiredRefreshTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	for hash, exp := range map[string]time.Time{
		"lapsed-yesterday": now.Add(-24 * time.Hour),
		"lapsed-a-second":  now.Add(-time.Second),
		"live":             now.Add(7 * 24 * time.Hour),
	} {
		if err := s.SaveRefreshToken(ctx, &RefreshToken{TokenHash: hash, UserID: "u1", ExpiresAt: exp, CreatedAt: now.Add(-48 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.PruneExpiredRefreshTokens(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("pruned %d, want the 2 that expired", n)
	}
	for _, gone := range []string{"lapsed-yesterday", "lapsed-a-second"} {
		if _, err := s.GetRefreshToken(ctx, gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	if _, err := s.GetRefreshToken(ctx, "live"); err != nil {
		t.Errorf("a live session was pruned: %v", err)
	}
	if n, _ := s.PruneExpiredRefreshTokens(ctx, now); n != 0 {
		t.Errorf("a second pass pruned %d, want 0", n)
	}
}
