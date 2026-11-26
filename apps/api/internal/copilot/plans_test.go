package copilot

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// testPlan is a valid draft right-sizing plan with one step.
func testPlan(org, id, cluster string) kobiplan.Plan {
	created := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	return kobiplan.Plan{
		ID: id, OrgID: org, ClusterID: cluster, Title: "Right-sizing de " + cluster,
		Kind: kobiplan.KindRightsizing, Version: 1, Status: kobiplan.StatusDraft,
		Scope:     kobiplan.Scope{Namespaces: []string{}, Workloads: []string{}},
		CreatedAt: created, ExpiresAt: created.Add(7 * 24 * time.Hour),
		Source:  kobiplan.Source{Initiator: kobiplan.InitiatorChat, UserID: "u-1", ConversationID: "conv-1"},
		Exports: []kobiplan.Export{},
		Engine:  kobiplan.Engine{Name: "rightsizing", Percentile: 0.95, ComputedAt: created},
		Impact:  kobiplan.Impact{Estimated: kobiplan.Savings{CPUMilli: 380, MoneyUnavailable: kobiplan.MoneyNoCostData}},
		Steps: []kobiplan.Step{{
			ID: "s1", Order: 1, Action: kobiplan.ActionSetResources,
			Target:       kobiplan.Target{Type: "deployments", Namespace: "payments", Name: "api", Owner: &kobiplan.Owner{Kind: kobiplan.OwnerUnknown}},
			Params:       json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"120m"}}]}`),
			Precondition: kobiplan.Precondition{SpecHash: "sha256:abc"},
			Risk:         kobiplan.RiskLow, RiskReasons: []string{}, Status: kobiplan.StepPending,
		}},
		Excluded: []kobiplan.Exclusion{},
		Notes:    []kobiplan.Note{},
	}
}

func TestBoltPlanStore_Contract(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "plans.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewBoltPlanStore(db, []byte("kobi_plans"), []byte("kobi_plans_versions"))
	if err != nil {
		t.Fatal(err)
	}
	planStoreContract(t, s)
}

// planStoreContract is what every PlanStore engine must do; the Postgres store
// runs it too (plans_postgres_ee_test.go).
func planStoreContract(t *testing.T, s PlanStore) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	wantErr := func(err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}

	// Create, read back, refuse a second create.
	p := testPlan("org-a", "pln_1", "c1")
	must(s.Create(p))
	got, err := s.Get("org-a", "pln_1")
	must(err)
	if got.Title != p.Title || got.Version != 1 || got.UpdatedAt.IsZero() || got.ContentHash == "" {
		t.Fatalf("read back %+v: want the plan with updatedAt and contentHash set by the store", got)
	}
	wantErr(s.Create(p), ErrPlanExists)
	v2 := testPlan("org-a", "pln_new", "c1")
	v2.Version = 2
	wantErr(s.Create(v2), ErrPlanVersionConflict)

	// An invalid plan never reaches storage.
	bad := testPlan("org-a", "pln_bad", "c1")
	bad.Title = ""
	if err := s.Create(bad); err == nil {
		t.Fatal("an invalid plan was stored")
	}
	if _, err := s.Get("org-a", "pln_bad"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("invalid plan readable: %v", err)
	}

	// Another org sees nothing and changes nothing.
	_, err = s.Get("org-b", "pln_1")
	wantErr(err, ErrPlanNotFound)
	other := *got
	other.OrgID = "org-b"
	wantErr(s.Update(other), ErrPlanNotFound)

	// Update changes metadata in place: status, exports.
	exported := *got
	at := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	exported.Status = kobiplan.StatusExported
	exported.Exports = []kobiplan.Export{{Format: kobiplan.ExportCommands, At: at, By: "u-1"}}
	exported.ExportedAt = &at
	must(s.Update(exported))
	got, err = s.Get("org-a", "pln_1")
	must(err)
	if got.Status != kobiplan.StatusExported || got.Version != 1 || len(got.Exports) != 1 {
		t.Fatalf("after update: status %s version %d exports %d", got.Status, got.Version, len(got.Exports))
	}

	// Update never changes what the plan proposes, nor its version.
	changed := *got
	changed.Steps = append([]kobiplan.Step(nil), got.Steps...)
	changed.Steps[0].Risk = kobiplan.RiskMedium
	changed.ContentHash = ""
	wantErr(s.Update(changed), ErrPlanContentChanged)
	skipped := *got
	skipped.Version = 2
	wantErr(s.Update(skipped), ErrPlanVersionConflict)

	// Revise keeps the old version unchanged and makes the new one current.
	revised := *got
	revised.Version = 2
	revised.Steps = append([]kobiplan.Step(nil), got.Steps...)
	revised.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"150m"}}]}`)
	revised.ContentHash = ""
	revised.Notes = []kobiplan.Note{{Version: 2, At: at, Text: "80m rejected: under the P95"}}
	must(s.Revise(revised))
	cur, err := s.Get("org-a", "pln_1")
	must(err)
	if cur.Version != 2 || cur.ContentHash == got.ContentHash {
		t.Fatalf("current after revise: version %d, hash unchanged=%v", cur.Version, cur.ContentHash == got.ContentHash)
	}
	old, err := s.GetVersion("org-a", "pln_1", 1)
	must(err)
	if old.Version != 1 || old.ContentHash != got.ContentHash || string(old.Steps[0].Params) != string(got.Steps[0].Params) {
		t.Fatalf("version 1 changed after the revision: %+v", old)
	}
	if again, err := s.GetVersion("org-a", "pln_1", 2); err != nil || again.Version != 2 {
		t.Fatalf("GetVersion of the current one: %v, %v", again, err)
	}
	_, err = s.GetVersion("org-a", "pln_1", 7)
	wantErr(err, ErrPlanNotFound)
	_, err = s.GetVersion("org-b", "pln_1", 1)
	wantErr(err, ErrPlanNotFound)
	jump := *cur
	jump.Version = 4
	jump.ContentHash = ""
	wantErr(s.Revise(jump), ErrPlanVersionConflict)

	// List: per cluster, per status, newest first, capped.
	time.Sleep(5 * time.Millisecond)
	must(s.Create(testPlan("org-a", "pln_2", "c1")))
	time.Sleep(5 * time.Millisecond)
	must(s.Create(testPlan("org-a", "pln_3", "c2")))
	must(s.Create(testPlan("org-b", "pln_4", "c1")))
	all, err := s.List("org-a", PlanFilter{})
	must(err)
	if len(all) != 3 || all[0].ID != "pln_3" {
		t.Fatalf("list = %v, want 3 plans with pln_3 first", ids(all))
	}
	c1, err := s.List("org-a", PlanFilter{ClusterID: "c1"})
	must(err)
	if len(c1) != 2 {
		t.Fatalf("cluster c1 = %v, want pln_1 and pln_2", ids(c1))
	}
	drafts, err := s.List("org-a", PlanFilter{Statuses: []kobiplan.Status{kobiplan.StatusDraft}})
	must(err)
	if len(drafts) != 2 {
		t.Fatalf("drafts = %v, want pln_2 and pln_3", ids(drafts))
	}
	capped, err := s.List("org-a", PlanFilter{Limit: 1})
	must(err)
	if len(capped) != 1 {
		t.Fatalf("limit 1 gave %d", len(capped))
	}

	// Live plans: drafts and exported ones count; an expired one does not.
	live, err := s.LiveForCluster("org-a", "c1")
	must(err)
	if len(live) != 2 {
		t.Fatalf("live on c1 = %v, want pln_1 (exported) and pln_2 (draft)", ids(live))
	}
	gone, err := s.Get("org-a", "pln_2")
	must(err)
	gone.Status = kobiplan.StatusExpired
	gone.ExpiredReason = kobiplan.ExpiredDiscarded
	must(s.Update(*gone))
	live, err = s.LiveForCluster("org-a", "c1")
	must(err)
	if len(live) != 1 || live[0].ID != "pln_1" {
		t.Fatalf("live on c1 after a discard = %v, want pln_1", ids(live))
	}

	// PruneOrg drops the org's old plans with every version, and nothing else.
	n, err := s.PruneOrg("org-a", time.Now().Add(time.Hour))
	must(err)
	if n != 3 {
		t.Fatalf("pruned %d, want the 3 plans of org-a", n)
	}
	_, err = s.GetVersion("org-a", "pln_1", 1)
	wantErr(err, ErrPlanNotFound)
	if _, err := s.Get("org-b", "pln_4"); err != nil {
		t.Fatalf("another org's plan was pruned: %v", err)
	}
	if n, _ := s.PruneOrg("org-b", time.Now().Add(-time.Hour)); n != 0 {
		t.Fatalf("a recent plan was pruned (%d)", n)
	}
}

func ids(ps []kobiplan.Plan) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}
