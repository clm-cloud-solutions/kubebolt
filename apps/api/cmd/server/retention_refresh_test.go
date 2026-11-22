package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

type fakeRefreshPruner struct {
	calls  int
	before time.Time
}

func (f *fakeRefreshPruner) PruneExpiredRefreshTokens(_ context.Context, before time.Time) (int, error) {
	f.calls++
	f.before = before
	return 3, nil
}

// Expired refresh tokens are pruned once per pass, across all orgs, with the
// pass's own clock — and even when the org listing fails.
func TestRetentionPass_PrunesExpiredRefreshTokensOncePerPass(t *testing.T) {
	passNow := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	p := &fakeRefreshPruner{}
	runRetentionPass(retentionDeps{
		tenants:       &fakeOrgs{orgs: []auth.Tenant{{ID: "org-a", Plan: auth.PlanFree}, {ID: "org-b", Plan: auth.PlanTeam}}},
		refreshTokens: p,
	}, passNow)
	if p.calls != 1 {
		t.Fatalf("pruned %d times in one pass, want 1 (tokens are not per org)", p.calls)
	}
	if !p.before.Equal(passNow) {
		t.Errorf("pruned before %v, want the pass's now %v", p.before, passNow)
	}

	p2 := &fakeRefreshPruner{}
	runRetentionPass(retentionDeps{tenants: &fakeOrgs{err: errors.New("tenants table unreadable")}, refreshTokens: p2}, passNow)
	if p2.calls != 1 {
		t.Errorf("an org listing failure skipped the refresh-token prune")
	}
}
