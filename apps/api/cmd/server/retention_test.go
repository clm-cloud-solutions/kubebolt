package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
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

// A single-tenant install keys Kobi's transcripts and ratings by the default
// org's name, while the org list carries the org by its generated id. The pass
// used to prune by the id alone and never matched one: a self-hosted install
// kept every conversation and every 👎 comment forever.
func TestRetentionPass_PrunesTheDefaultOrgsConversationsAndRatings(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "kb.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenants, err := auth.NewTenantsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucketIfNotExists([]byte("conv")); return e }); err != nil {
		t.Fatal(err)
	}
	conv := copilot.NewBoltConversationStore(db, []byte("conv"), 0, 0)
	fb, err := copilot.NewBoltFeedbackStore(db, []byte("feedback"))
	if err != nil {
		t.Fatal(err)
	}
	// Written the way the chat and feedback handlers write them.
	if err := conv.Upsert(&copilot.ConversationRecord{ID: "c1", UserID: "u1", TenantID: copilot.DefaultConversationTenant}); err != nil {
		t.Fatal(err)
	}
	if _, err := fb.Set(copilot.Feedback{TenantID: auth.DefaultTenantName, UserID: "u1", ConversationID: "c1", TurnID: "t1", Rating: copilot.FeedbackDown, Comment: "wrong pod"}); err != nil {
		t.Fatal(err)
	}

	// Past the conversation horizon (90 days by default).
	runRetentionPass(retentionDeps{tenants: tenants, conversations: conv, feedback: fb}, time.Now().Add(200*24*time.Hour))

	if _, ok, _ := conv.Get(copilot.DefaultConversationTenant, "u1", "c1"); ok {
		t.Error("the default org's conversation survived the pass")
	}
	if left, _ := fb.ForConversation(auth.DefaultTenantName, "u1", "c1"); len(left) != 0 {
		t.Errorf("the default org's ratings survived the pass: %d left", len(left))
	}
}

func TestConversationTenants(t *testing.T) {
	def := auth.Tenant{ID: "6b1c…", Name: auth.DefaultTenantName}
	if got := conversationTenants(def); len(got) != 2 || got[1] != copilot.DefaultConversationTenant {
		t.Errorf("default org = %v, want its id and %q", got, copilot.DefaultConversationTenant)
	}
	if got := conversationTenants(auth.Tenant{ID: "o1", Name: "acme"}); len(got) != 1 {
		t.Errorf("another org = %v, want its id only", got)
	}
	withMultiTenantMain(t)
	if got := conversationTenants(def); len(got) != 1 {
		t.Errorf("multi-tenant default org = %v, want its id only: there the handlers key by id", got)
	}
}

func withMultiTenantMain(t *testing.T) {
	t.Helper()
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = true
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })
}
