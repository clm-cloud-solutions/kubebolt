package findings

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// collect captures every delta a sweep publishes.
func collect(into *[]SweepDelta) DeltaFunc {
	return func(d SweepDelta) { *into = append(*into, d) }
}

// The first pass for a (tenant, cluster, source) must be marked as seeding.
// Everything a scanner reports the day it is installed is "new" only in the
// trivial sense, and a consumer that treats it as news sends hundreds of
// messages in one afternoon — the failure this whole design exists to avoid.
func TestSweepDelta_FirstPassIsSeeding(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	owner := &fakeOwner{rsToDeploy: map[string]string{"demo-load-dd5fd8dd5": "demo-load"}}
	dyn := fakeDyn(t, trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"))

	var got []SweepDelta
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", dyn, owner), time.Hour).
		WithDelta(collect(&got)).SweepOnce(context.Background())

	if len(got) != 1 {
		t.Fatalf("published %d deltas, want 1", len(got))
	}
	d := got[0]
	if !d.Seeding {
		t.Error("Seeding = false on the first pass ever for this cluster+source")
	}
	if len(d.New) != 1 || d.Active != 1 {
		t.Errorf("New=%d Active=%d, want 1 and 1", len(d.New), d.Active)
	}
	if d.New[0].ResourceName != "demo-load" {
		t.Errorf("the delta carries %q — it must carry the record as STORED, "+
			"collapsed to its workload, not the scanner's raw resource",
			d.New[0].ResourceName)
	}
}

// A pass where nothing moved publishes nothing. A sink that hears from the
// sweep every ten minutes regardless learns to ignore it, and the caller
// cannot tell "no change" from "change" without re-deriving it.
func TestSweepDelta_QuietPassPublishesNothing(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	owner := &fakeOwner{rsToDeploy: map[string]string{"demo-load-dd5fd8dd5": "demo-load"}}
	dyn := fakeDyn(t, trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"))
	iter := staticIteratorWithOwner("org", "cluster", dyn, owner)

	var got []SweepDelta
	sweep := func() {
		NewSweeper(store, []integrations.CRDSignalProvider{prov}, iter, time.Hour).
			WithDelta(collect(&got)).SweepOnce(context.Background())
	}
	sweep() // seeds
	got = nil
	sweep() // identical cluster state

	if len(got) != 0 {
		t.Errorf("published %d deltas on an unchanged pass, want none: %+v", len(got), got)
	}
}

// After the baseline exists, a finding that appears is news — and NOT seeding.
func TestSweepDelta_LaterFindingIsNewNotSeeding(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	owner := &fakeOwner{rsToDeploy: map[string]string{
		"demo-load-dd5fd8dd5": "demo-load",
		"shop-api-84db7586fc": "shop-api",
	}}

	var got []SweepDelta
	run := func(dyn dynamic.Interface) {
		NewSweeper(store, []integrations.CRDSignalProvider{prov},
			staticIteratorWithOwner("org", "cluster", dyn, owner), time.Hour).
			WithDelta(collect(&got)).SweepOnce(context.Background())
	}
	run(fakeDyn(t, trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519")))
	got = nil
	run(fakeDyn(t,
		trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"),
		trivyReplicaSetReport("shop-api-84db7586fc", "CVE-2025-99999"),
	))

	if len(got) != 1 {
		t.Fatalf("published %d deltas, want 1", len(got))
	}
	if got[0].Seeding {
		t.Error("Seeding = true on a cluster that already had stored findings")
	}
	if len(got[0].New) != 1 || got[0].New[0].ResourceName != "shop-api" {
		t.Errorf("New = %+v, want only shop-api", got[0].New)
	}
}

// A finding that stops being reported arrives in Resolved carrying the record
// as it was ACTIVE. MarkResolved has already flipped the stored copy, so a
// consumer writing "these closed" would otherwise describe an empty shell.
func TestSweepDelta_ResolvedCarriesTheRecordThatClosed(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	owner := &fakeOwner{rsToDeploy: map[string]string{"demo-load-dd5fd8dd5": "demo-load"}}

	var got []SweepDelta
	run := func(dyn dynamic.Interface) {
		NewSweeper(store, []integrations.CRDSignalProvider{prov},
			staticIteratorWithOwner("org", "cluster", dyn, owner), time.Hour).
			WithDelta(collect(&got)).SweepOnce(context.Background())
	}
	run(fakeDyn(t, trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519")))
	got = nil
	run(fakeDyn(t)) // the image was patched: nothing reported

	if len(got) != 1 || len(got[0].Resolved) != 1 {
		t.Fatalf("deltas=%d, want one carrying a resolved finding: %+v", len(got), got)
	}
	r := got[0].Resolved[0]
	if r.ResourceName != "demo-load" || r.Status != StatusActive {
		t.Errorf("resolved record = %s/%s, want the active copy of demo-load", r.Status, r.ResourceName)
	}
	if got[0].Active != 0 {
		t.Errorf("Active = %d after everything closed, want 0", got[0].Active)
	}
}

// The guarantee the whole feature rests on: a pass that did not see the full
// picture publishes NOTHING. Here the owner cache cannot answer, which is the
// same condition that already forbids resolving — and it must equally forbid
// claiming anything is new, or an agent-proxy reconnect turns into an alert
// about findings that were already there.
func TestSweepDelta_PartialPassPublishesNothing(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	// TWO reports, and only one of them unresolvable. Without the second the
	// test proves nothing: a pass where every finding is skipped emits no delta
	// whatever the gate does, because there is nothing to report. Here one
	// finding IS stored, so removing the gate WOULD publish it.
	dyn := fakeDyn(t,
		trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"),
		trivyReplicaSetReport("shop-api-84db7586fc", "CVE-2025-99999"),
	)
	cold := &fakeOwner{
		rsToDeploy:   map[string]string{"shop-api-84db7586fc": "shop-api"},
		unresolvable: map[string]bool{"demo-load-dd5fd8dd5": true},
	}

	var got []SweepDelta
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", dyn, cold), time.Hour).
		WithDelta(collect(&got)).SweepOnce(context.Background())

	if len(got) != 0 {
		t.Errorf("a partial pass published %d deltas, want none: %+v", len(got), got)
	}
}

// The same finding reported by two objects is ONE finding. Trivy emits a report
// per ReplicaSet and the owner resolver collapses them onto one Deployment, so
// a CVE on a workload with several revisions arrives repeatedly with the same
// fingerprint. `seen` is a map and has always absorbed that; `fresh` is a slice
// and did not.
//
// Measured on the first live baseline: the summary announced 1857 findings over
// a store holding 963 — a 1.9x inflation in the one message whose entire job is
// to state the size of the problem. Unit tests missed it because every fixture
// so far had a single report per fingerprint.
func TestSweepDelta_CountsARepeatedFindingOnce(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	// Two ReplicaSets of the SAME Deployment, same CVE — one finding.
	owner := &fakeOwner{rsToDeploy: map[string]string{
		"demo-load-dd5fd8dd5":  "demo-load",
		"demo-load-585fb9c95d": "demo-load",
	}}
	dyn := fakeDyn(t,
		trivyReplicaSetReport("demo-load-dd5fd8dd5", "CVE-2025-26519"),
		trivyReplicaSetReport("demo-load-585fb9c95d", "CVE-2025-26519"),
	)

	var got []SweepDelta
	NewSweeper(store, []integrations.CRDSignalProvider{prov},
		staticIteratorWithOwner("org", "cluster", dyn, owner), time.Hour).
		WithDelta(collect(&got)).SweepOnce(context.Background())

	if len(got) != 1 {
		t.Fatalf("published %d deltas, want 1", len(got))
	}
	if len(got[0].New) != 1 {
		t.Errorf("New carries %d entries for one finding reported twice: %+v", len(got[0].New), got[0].New)
	}
	// And the delta must agree with what was actually stored.
	stored, _ := store.List(Query{TenantID: "org", Status: StatusActive})
	if len(stored) != len(got[0].New) || got[0].Active != len(stored) {
		t.Errorf("delta says New=%d Active=%d, store holds %d — a summary built on this "+
			"would quote a number the dashboard contradicts",
			len(got[0].New), got[0].Active, len(stored))
	}
}
