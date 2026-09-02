package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

type fakeOrgs struct {
	orgs []auth.Tenant
	err  error
}

func (f *fakeOrgs) ListTenants() ([]auth.Tenant, error) { return f.orgs, f.err }

// fakePruner records the cutoff it was handed per org — the cutoff IS the
// behavior under test, since that is where a horizon becomes a delete.
type fakePruner struct {
	cutoffs map[string]time.Time
	failOn  string
	calls   int
}

func newFakePruner() *fakePruner { return &fakePruner{cutoffs: map[string]time.Time{}} }

func (f *fakePruner) PruneOrg(orgID string, before time.Time) (int, error) {
	f.calls++
	if orgID == f.failOn {
		return 0, errors.New("boom")
	}
	f.cutoffs[orgID] = before
	return 1, nil
}

func (f *fakePruner) PruneEventsOrg(orgID string, before time.Time) (int, error) {
	return f.PruneOrg(orgID, before)
}

var passNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func daysBefore(t *testing.T, now, got time.Time) float64 {
	t.Helper()
	return now.Sub(got).Hours() / 24
}

// Each store has its own window, and the operator's env value wins over the
// default — read on every pass, so a change needs no restart.
func TestRetentionPass_EachStoreGetsItsOwnHorizon(t *testing.T) {
	t.Setenv("KUBEBOLT_INSIGHTS_RETENTION_HORIZON", "")
	t.Setenv("KUBEBOLT_AUDIT_RETENTION_HORIZON", "")
	t.Setenv("KUBEBOLT_FINDINGS_RETENTION_HORIZON", "")
	t.Setenv("KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON", "")
	ins, aud, fin, ev, conv := newFakePruner(), newFakePruner(), newFakePruner(), newFakePruner(), newFakePruner()
	runRetentionPass(retentionDeps{
		tenants:       &fakeOrgs{orgs: []auth.Tenant{{ID: "org"}}},
		insights:      ins,
		audit:         aud,
		findings:      fin,
		events:        ev,
		conversations: conv,
	}, passNow)

	for name, want := range map[string]float64{"insights": 7, "audit": 90, "findings": 30, "events": 30, "conversations": 90} {
		p := map[string]*fakePruner{"insights": ins, "audit": aud, "findings": fin, "events": ev, "conversations": conv}[name]
		if got := daysBefore(t, passNow, p.cutoffs["org"]); got != want {
			t.Errorf("%s cutoff = %v days back, want %v", name, got, want)
		}
	}
}

func TestRetentionPass_EnvOverridesTheDefault(t *testing.T) {
	t.Setenv("KUBEBOLT_INSIGHTS_RETENTION_HORIZON", "72h")
	pruner := newFakePruner()
	runRetentionPass(retentionDeps{
		tenants:  &fakeOrgs{orgs: []auth.Tenant{{ID: "org"}}},
		insights: pruner,
	}, passNow)
	if got := daysBefore(t, passNow, pruner.cutoffs["org"]); got != 3 {
		t.Errorf("insights cutoff = %v days back, want 3 (from env)", got)
	}
}

// One org's failure must not abort the pass — otherwise a single bad org
// silently stops retention for every org after it in the list.
func TestRetentionPass_OneOrgFailingDoesNotStopTheRest(t *testing.T) {
	pruner := newFakePruner()
	pruner.failOn = "org-a"
	runRetentionPass(retentionDeps{
		tenants: &fakeOrgs{orgs: []auth.Tenant{
			{ID: "org-a"},
			{ID: "org-b"},
			{ID: "org-c"},
		}},
		insights: pruner,
	}, passNow)

	if _, ok := pruner.cutoffs["org-c"]; !ok {
		t.Error("org-c was never pruned — a failure on org-a aborted the pass")
	}
	if pruner.calls != 3 {
		t.Errorf("prune calls = %d, want 3 (one per org)", pruner.calls)
	}
}

// Retention runs with whichever stores exist; a nil one is skipped, not a panic.
func TestRetentionPass_NilStoresAreSkipped(t *testing.T) {
	runRetentionPass(retentionDeps{
		tenants: &fakeOrgs{orgs: []auth.Tenant{{ID: "org"}}},
	}, passNow)
}

func TestRetentionPass_ListFailureIsNotFatal(t *testing.T) {
	pruner := newFakePruner()
	runRetentionPass(retentionDeps{
		tenants:  &fakeOrgs{err: errors.New("db down")},
		insights: pruner,
	}, passNow)
	if pruner.calls != 0 {
		t.Errorf("pruned %d times despite an unusable org list", pruner.calls)
	}
}

// signalPruner reports its first call on a channel — a plain counter would be
// read by the test while the retention goroutine writes it, which is a data race
// in the TEST, not in the code under test.
type signalPruner struct{ fired chan struct{} }

func (s *signalPruner) PruneOrg(string, time.Time) (int, error) {
	select {
	case s.fired <- struct{}{}:
	default:
	}
	return 0, nil
}

// A ticker-only job never fires on a process that restarts more often than its
// interval. The first pass must not wait for the hourly tick.
func TestStartRetention_RunsAFirstPassBeforeTheInterval(t *testing.T) {
	orig := retentionStartDelay
	retentionStartDelay = time.Millisecond
	defer func() { retentionStartDelay = orig }()

	pruner := &signalPruner{fired: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startRetention(ctx, retentionDeps{
		tenants:  &fakeOrgs{orgs: []auth.Tenant{{ID: "org"}}},
		insights: pruner,
	})

	select {
	case <-pruner.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("no first pass ran — retention would wait a full interval, and a " +
			"pod restarting more often than that would never prune at all")
	}
}
