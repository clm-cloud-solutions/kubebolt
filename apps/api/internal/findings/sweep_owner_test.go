package findings

import (
	"context"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// fakeOwner maps a ReplicaSet to its Deployment the way the connector's
// informer cache does.
type fakeOwner struct {
	rsToDeploy map[string]string
	// unresolvable names report an UNSETTLED answer — the cache-miss case,
	// which the real resolver hits on a cold or incomplete informer.
	unresolvable map[string]bool
	calls        int
}

func (f *fakeOwner) WorkloadOwner(namespace, kind, name string) (string, string, bool) {
	f.calls++
	if kind != "ReplicaSet" {
		return kind, name, true
	}
	if f.unresolvable[name] {
		return kind, name, false
	}
	if d, ok := f.rsToDeploy[name]; ok {
		return "Deployment", d, true
	}
	return kind, name, true
}

// The defect this locks, reproduced on a live cluster on 2026-08-04: a no-op
// `rollout restart` of demo-load moved all 8 of its findings to a new
// ReplicaSet name 20 minutes later — the old ones resolved, the new ones came
// back with firstSeen reset to now. Same image, same CVEs, same posture, but
// "open for 40 days" silently became "found a minute ago". On a cluster that
// deploys daily nothing ever ages enough to look alarming.
//
// The fix is only correct if it lands BEFORE the fingerprint is computed, since
// the resource name is part of the identity.
func TestSweep_RolloutDoesNotChurnFindings(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	ctx := context.Background()
	owner := &fakeOwner{rsToDeploy: map[string]string{
		"demo-load-dd5fd8dd5":  "demo-load",
		"demo-load-585fb9c95d": "demo-load",
	}}

	// Sweep 1: the workload runs on the first ReplicaSet.
	before := fakeDyn(t, trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"))
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", before, owner), time.Hour).SweepOnce(ctx)

	first, _ := store.List(Query{TenantID: "org"})
	if len(first) != 1 {
		t.Fatalf("sweep 1 stored %d findings, want 1", len(first))
	}
	if first[0].ResourceName != "demo-load" || first[0].ResourceKind != "Deployment" {
		t.Fatalf("stored resource = %s/%s, want Deployment/demo-load",
			first[0].ResourceKind, first[0].ResourceName)
	}
	fpBefore, firstSeen := first[0].Fingerprint, first[0].FirstSeen

	// Sweep 2: a rollout replaced the ReplicaSet. Nothing about the image or
	// its vulnerabilities changed.
	after := fakeDyn(t, trivyReplicaSetReport("demo-load-585fb9c95d", "CVE-2025-26519"))
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", after, owner), time.Hour).SweepOnce(ctx)

	all, _ := store.List(Query{TenantID: "org"})
	if len(all) != 1 {
		t.Fatalf("after the rollout there are %d findings, want 1 — the rollout "+
			"forked the identity instead of updating it", len(all))
	}
	if all[0].Fingerprint != fpBefore {
		t.Errorf("fingerprint changed across the rollout (%s → %s)", fpBefore, all[0].Fingerprint)
	}
	if !all[0].FirstSeen.Equal(firstSeen) {
		t.Errorf("firstSeen moved %v → %v: the age of the CVE was lost, which is "+
			"exactly what breaks prioritising by how long something has been open",
			firstSeen, all[0].FirstSeen)
	}
	if all[0].Status != StatusActive {
		t.Errorf("status = %s, want active — the finding never went away", all[0].Status)
	}
}

// Without a resolver the sweep must still work, keeping whatever the scanner
// reported. A permission-gated SA has no ReplicaSet informer, and that is a
// degraded read, not a broken sweep.
func TestSweep_NilOwnerResolverKeepsScannerResource(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)

	dyn := fakeDyn(t, trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"))
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", dyn, nil), time.Hour).SweepOnce(context.Background())

	got, _ := store.List(Query{TenantID: "org"})
	if len(got) != 1 {
		t.Fatalf("stored %d findings, want 1", len(got))
	}
	if got[0].ResourceName != "demo-load-dd5fd8dd5" {
		t.Errorf("resource = %q, want the scanner's own value", got[0].ResourceName)
	}
}

// A resource the resolver does not recognise (a bare ReplicaSet with no
// Deployment owner, or one already evicted from the cache) must pass through
// untouched rather than be guessed at by trimming the name.
func TestSweep_UnknownReplicaSetPassesThrough(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	owner := &fakeOwner{rsToDeploy: map[string]string{}}

	dyn := fakeDyn(t, trivyReplicaSetReport("orphan-rs-abc123", "CVE-2025-26519"))
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", dyn, owner), time.Hour).SweepOnce(context.Background())

	got, _ := store.List(Query{TenantID: "org"})
	if len(got) != 1 || got[0].ResourceName != "orphan-rs-abc123" {
		t.Errorf("orphan ReplicaSet was rewritten: %+v", got)
	}
}

// The churn the test above prevents comes back through a second door: not a
// rollout, but a resolver that cannot answer. Measured on dev between 2026-08-04
// and 2026-08-11 — 500 of 565 "resolved" findings had an identical active twin
// under a different resource name, in batches on the days the API restarted.
// An agent-proxy informer reports synced while its cache is still filling
// (WaitForCacheSync lies there), so the first pass after a reconnect collapses
// nothing, fingerprints on the ReplicaSet name, and the next pass resolves what
// it just wrote.
//
// Skipping is safe precisely because the condition is transient: the finding is
// picked up whole on the following pass. What must NOT happen is a write on a
// name that is about to change, or a resolve computed from a partial view.
func TestSweep_UnresolvedOwnerNeitherWritesNorResolves(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	ctx := context.Background()
	warm := &fakeOwner{rsToDeploy: map[string]string{"demo-load-dd5fd8dd5": "demo-load"}}

	// Pass 1, warm cache: the finding lands once, under its Deployment.
	dyn := fakeDyn(t, trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"))
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", dyn, warm), time.Hour).SweepOnce(ctx)
	first, _ := store.List(Query{TenantID: "org"})
	if len(first) != 1 || first[0].ResourceName != "demo-load" {
		t.Fatalf("pass 1 stored %d findings (%+v), want 1 under demo-load", len(first), first)
	}

	// Pass 2, cache cannot answer — an API restart against an agent-proxy
	// cluster. Same report, same everything.
	cold := &fakeOwner{unresolvable: map[string]bool{"demo-load-dd5fd8dd5": true}}
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", dyn, cold), time.Hour).SweepOnce(ctx)

	all, _ := store.List(Query{TenantID: "org"})
	if len(all) != 1 {
		t.Fatalf("stored %d findings after the cold pass, want the original 1 — a second identity was minted", len(all))
	}
	if all[0].Status != StatusActive {
		t.Errorf("finding went %s on a pass that saw nothing conclusive; a partial view must never resolve", all[0].Status)
	}
	if all[0].Fingerprint != first[0].Fingerprint {
		t.Errorf("fingerprint moved from %s to %s", first[0].Fingerprint, all[0].Fingerprint)
	}
	if !all[0].FirstSeen.Equal(first[0].FirstSeen) {
		t.Errorf("firstSeen reset from %s to %s — the age the operator reads is the whole point",
			first[0].FirstSeen, all[0].FirstSeen)
	}
}
