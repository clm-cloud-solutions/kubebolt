package main

import (
	"errors"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// fakeClusterStore holds per-org findings/events cluster sets and records
// which (org, cluster) pairs were deleted — the deletion set IS the behavior
// under test.
type fakeClusterStore struct {
	clusters map[string][]string // org → cluster ids holding rows
	listErr  error
	deleted  []string // "org/cluster"
}

func (f *fakeClusterStore) ClusterIDs(org string) ([]string, error) {
	return f.clusters[org], f.listErr
}
func (f *fakeClusterStore) DeleteCluster(org, cluster string) (int, error) {
	f.deleted = append(f.deleted, org+"/"+cluster)
	return 3, nil
}
func (f *fakeClusterStore) EventClusterIDs(org string) ([]string, error) {
	return f.ClusterIDs(org)
}
func (f *fakeClusterStore) DeleteEventsCluster(org, cluster string) (int, error) {
	return f.DeleteCluster(org, cluster)
}

// The rule under test (in-vivo 2026-09-15): registered — even disconnected —
// keeps its history; removed from the tenant is swept. The registry answers
// membership, never liveness.
func TestRetentionPass_SweepsSecurityOrphans(t *testing.T) {
	store := &fakeClusterStore{clusters: map[string][]string{
		"org-a": {"registered-uid", "ghost-uid"},
	}}
	runRetentionPass(retentionDeps{
		tenants:          &fakeOrgs{orgs: []auth.Tenant{{ID: "org-a", Plan: auth.PlanFree}}},
		findingsClusters: store,
		eventClusters:    store,
		registered: func(orgID string) (map[string]struct{}, bool) {
			return map[string]struct{}{"registered-uid": {}, "in-cluster": {}}, true
		},
	}, passNow)

	// ghost-uid swept in BOTH stores (findings + events share the fake),
	// registered-uid untouched.
	if len(store.deleted) != 2 {
		t.Fatalf("deleted = %v, want ghost-uid swept twice (findings + events)", store.deleted)
	}
	for _, d := range store.deleted {
		if d != "org-a/ghost-uid" {
			t.Fatalf("swept a registered cluster: %v", store.deleted)
		}
	}
}

// An unreadable registry must SKIP the sweep, never guess — over-deleting is
// the unrecoverable direction.
func TestRetentionPass_OrphanSweepFailsClosed(t *testing.T) {
	store := &fakeClusterStore{clusters: map[string][]string{"org-a": {"ghost-uid"}}}
	runRetentionPass(retentionDeps{
		tenants:          &fakeOrgs{orgs: []auth.Tenant{{ID: "org-a", Plan: auth.PlanFree}}},
		findingsClusters: store,
		eventClusters:    store,
		registered:       func(orgID string) (map[string]struct{}, bool) { return nil, false },
	}, passNow)
	if len(store.deleted) != 0 {
		t.Fatalf("unreadable registry must skip the sweep, deleted %v", store.deleted)
	}
}

// A store listing error skips that store's sweep without touching the rest of
// the pass, and without deleting on a guess.
func TestRetentionPass_OrphanSweepToleratesListErrors(t *testing.T) {
	store := &fakeClusterStore{listErr: errors.New("boom")}
	runRetentionPass(retentionDeps{
		tenants:          &fakeOrgs{orgs: []auth.Tenant{{ID: "org-a", Plan: auth.PlanFree}}},
		findingsClusters: store,
		eventClusters:    store,
		registered: func(orgID string) (map[string]struct{}, bool) {
			return map[string]struct{}{"in-cluster": {}}, true
		},
	}, passNow)
	if len(store.deleted) != 0 {
		t.Fatalf("listing error must not delete anything, deleted %v", store.deleted)
	}
}
