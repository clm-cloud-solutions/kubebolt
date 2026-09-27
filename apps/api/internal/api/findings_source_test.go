package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// findingSource is what stands between Kobi's get_findings tool and the raw
// store. cluster_scope.go records that forgetting to narrow per-cluster rows
// shipped THREE times, and that all three passed review because the person
// testing was an admin, whom nothing narrows — so these tests are written from
// the narrowed side first.

type fakeFindingStore struct {
	gotQuery findings.Query
	recs     []findings.Record
	// stored answers Get, keyed cluster|fingerprint.
	stored    map[string]*findings.Record
	gotTenant string
}

func (f *fakeFindingStore) List(q findings.Query) ([]findings.Record, error) {
	f.gotQuery = q
	return append([]findings.Record(nil), f.recs...), nil
}
func (f *fakeFindingStore) Upsert(*findings.Record) error { return nil }
func (f *fakeFindingStore) MarkResolved(string, string, string, time.Time) error {
	return nil
}
func (f *fakeFindingStore) Get(tenant, clusterID, fingerprint string) (*findings.Record, bool, error) {
	f.gotTenant = tenant
	if rec, ok := f.stored[clusterID+"|"+fingerprint]; ok {
		return rec, true, nil
	}
	return nil, false, nil
}
func (f *fakeFindingStore) Prune(time.Time) (int, error)              { return 0, nil }
func (f *fakeFindingStore) PruneOrg(string, time.Time) (int, error)   { return 0, nil }
func (f *fakeFindingStore) DeleteCluster(string, string) (int, error) { return 0, nil }
func (f *fakeFindingStore) ClusterIDs(string) ([]string, error)       { return nil, nil }

func fsRec(clusterID string) findings.Record {
	return findings.Record{
		ClusterID: clusterID,
		Finding:   integrations.Finding{Title: "CVE", Severity: "high", Source: "trivy"},
		Status:    findings.StatusActive,
	}
}

func ctxWithScope(s ClusterScope) context.Context {
	return context.WithValue(context.Background(), clusterScopeKey, s)
}

// The one that matters. A narrowed caller must not receive another team's rows
// through the chat, and the tool has no other place to learn the entitlement.
func TestFindingSource_NarrowedDropsForeignClusters(t *testing.T) {
	store := &fakeFindingStore{recs: []findings.Record{
		fsRec("mine-1"), fsRec("other-team"), fsRec("mine-2"), fsRec("other-team"),
	}}
	src := findingSource{store: store}

	got, err := src.List(ctxWithScope(scopeWith("", []string{"mine-1", "mine-2"}, true)),
		findings.Query{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("returned %d records, want only the two owned ones", len(got))
	}
	for _, r := range got {
		if r.ClusterID == "other-team" {
			t.Error("served another team's finding")
		}
	}
}

// The zero scope does not narrow, and that is deliberate: OSS has one org and
// nothing to filter. A "fix" that blanks every single-tenant install is not a
// fix — the protection is mounting the middleware, which the router test pins.
func TestFindingSource_UnnarrowedReadsEverything(t *testing.T) {
	store := &fakeFindingStore{recs: []findings.Record{fsRec("a"), fsRec("b"), fsRec("c")}}
	got, err := findingSource{store: store}.List(context.Background(), findings.Query{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("returned %d, want all 3 — OSS narrows nothing", len(got))
	}
}

// An entitled-to-nothing caller reads nothing, rather than everything.
func TestFindingSource_EntitledToNothingReadsNothing(t *testing.T) {
	store := &fakeFindingStore{recs: []findings.Record{fsRec("a"), fsRec("b")}}
	got, _ := findingSource{store: store}.List(ctxWithScope(scopeWith("", nil, true)),
		findings.Query{}, true)
	if len(got) != 0 {
		t.Errorf("returned %d records to a caller entitled to none", len(got))
	}
}

// Without allClusters the read is pinned to the request's cluster BEFORE the
// store call, so a narrowed caller does not pull rows it may not see.
func TestFindingSource_PinsTheRequestedClusterUnlessAllAsked(t *testing.T) {
	store := &fakeFindingStore{}
	ctx := ctxWithScope(scopeWith("cluster-7", []string{"cluster-7"}, true))

	findingSource{store: store}.List(ctx, findings.Query{}, false)
	if store.gotQuery.ClusterID != "cluster-7" {
		t.Errorf("ClusterID = %q, want the request's cluster", store.gotQuery.ClusterID)
	}

	// "All clusters" for a NARROWED caller spans its readable clusters one by
	// one — never an org-wide page filtered afterwards, which could come back
	// full of other clusters' rows and leave the caller with nothing.
	findingSource{store: store}.List(ctx, findings.Query{}, true)
	if store.gotQuery.ClusterID != "cluster-7" {
		t.Errorf("ClusterID = %q, want the caller's one readable cluster", store.gotQuery.ClusterID)
	}

	// Unnarrowed, "all" is the org: one query with no cluster.
	findingSource{store: store}.List(context.Background(), findings.Query{}, true)
	if store.gotQuery.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty so the org is spanned", store.gotQuery.ClusterID)
	}
}

// nil store must yield a nil interface, not a typed nil in a non-nil
// interface — the executor's == nil check is what produces "I cannot see
// findings" instead of a clean-looking zero posture.
func TestFindingSourceFor_NilStoreYieldsNilInterface(t *testing.T) {
	if src := (&handlers{}).findingSourceFor(); src != nil {
		t.Errorf("got %#v, want a nil interface", src)
	}
}

// The executor names the cluster — the request's active one, resolved the way
// its connector is. Neither the chat nor /mcp sends ?cluster=, so pinning to
// scope.Requested() alone answered "this cluster" with the whole org.
func TestFindingSource_TheExecutorsClusterWinsOverTheQueryString(t *testing.T) {
	store := &fakeFindingStore{}
	ctx := ctxWithScope(scopeWith("", nil, false)) // what the chat carries: no ?cluster=

	findingSource{store: store}.List(ctx, findings.Query{ClusterID: "active-uid"}, false)
	if store.gotQuery.ClusterID != "active-uid" {
		t.Errorf("ClusterID = %q, want the cluster the executor resolved", store.gotQuery.ClusterID)
	}

	findingSource{store: store}.List(ctx, findings.Query{ClusterID: "active-uid"}, true)
	if store.gotQuery.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty: all clusters means all clusters", store.gotQuery.ClusterID)
	}
}

// The store keys the default org as "", the way activeTenantID maps it on the
// REST side. Passing the raw "default" would look the rows up under a key the
// sweep never wrote — a clean-looking empty posture on every OSS install.
func TestFindingSource_DefaultTenantKeysLikeTheRESTSide(t *testing.T) {
	store := &fakeFindingStore{}
	ctx := auth.WithTenantID(context.Background(), auth.DefaultTenantName)
	findingSource{store: store}.List(ctx, findings.Query{}, true)
	if store.gotQuery.TenantID != "" {
		t.Errorf("TenantID = %q, want \"\" for the default org", store.gotQuery.TenantID)
	}
}

func detailStore() *fakeFindingStore {
	rec := fsRec("other-team")
	rec.Fingerprint = "fp1"
	mine := fsRec("mine-1")
	mine.Fingerprint = "fp1"
	return &fakeFindingStore{stored: map[string]*findings.Record{
		"other-team|fp1": &rec,
		"mine-1|fp1":     &mine,
	}}
}

// The drill-down applies the SAME entitlement as the list next to it. It used
// to check the org only: a narrowed user holding a fingerprint and a cluster id
// could open a finding of a cluster their teams do not own.
func TestFindingDetail_NarrowedCallerCannotOpenAForeignFinding(t *testing.T) {
	ctx := ctxWithScope(scopeWith("", []string{"mine-1"}, true))
	src := findingSource{store: detailStore()}

	if _, found, err := src.Detail(ctx, "other-team", "fp1"); err != nil || found {
		t.Fatalf("found=%v err=%v — another team's finding must read as not found", found, err)
	}
	d, found, err := src.Detail(ctx, "mine-1", "fp1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v — the caller's own finding must open", found, err)
	}
	if d.ClusterID != "mine-1" {
		t.Errorf("served cluster %q", d.ClusterID)
	}
}

// Same rule through the route the Security page calls.
func TestFindingDetailRoute_NarrowedCallerGets404(t *testing.T) {
	h := &handlers{findingsStore: detailStore()}
	for _, tc := range []struct {
		cluster string
		want    int
	}{{"other-team", http.StatusNotFound}, {"mine-1", http.StatusOK}} {
		req := httptest.NewRequest("GET", "/findings/fp1?cluster="+tc.cluster, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("fingerprint", "fp1")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = context.WithValue(ctx, clusterScopeKey, scopeWith(tc.cluster, []string{"mine-1"}, true))
		rr := httptest.NewRecorder()
		h.handleFindingDetail(rr, req.WithContext(ctx))
		if rr.Code != tc.want {
			t.Errorf("cluster %s: status %d, want %d", tc.cluster, rr.Code, tc.want)
		}
	}
}

// With no way to reach the cluster the stored row still comes back, marked as
// not live — the drill-down degrades, it does not fail.
func TestFindingDetail_NoManagerDegradesToTheStoredRow(t *testing.T) {
	d, found, err := findingSource{store: detailStore()}.Detail(context.Background(), "mine-1", "fp1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if d.Live || d.LiveError == "" {
		t.Errorf("live=%v liveError=%q — want the stored row, marked not live, with the reason", d.Live, d.LiveError)
	}
}

// Entitled to no cluster is an error, never an empty posture: an empty
// posture reads as "no findings", and that is what let a plan call an image
// with a critical CVE clean.
func TestFindingSource_EntitledToNothingIsAnErrorNotClean(t *testing.T) {
	store := &fakeFindingStore{recs: []findings.Record{fsRec("a")}}
	_, err := findingSource{store: store}.List(ctxWithScope(scopeWith("", nil, true)), findings.Query{}, true)
	if err == nil || !strings.Contains(err.Error(), "NOT a clean result") {
		t.Errorf("err = %v, want the could-not-look error", err)
	}
	if _, err := (findingSource{store: store}).List(ctxWithScope(scopeWith("", []string{"a"}, true)), findings.Query{}, true); err != nil {
		t.Errorf("an entitled caller got %v", err)
	}
}

type fakeEventStore struct {
	gotQuery findings.EventQuery
	evs      []findings.EventRecord
}

func (f *fakeEventStore) Append(*findings.EventRecord) error { return nil }
func (f *fakeEventStore) ListEvents(q findings.EventQuery) ([]findings.EventRecord, error) {
	f.gotQuery = q
	return append([]findings.EventRecord(nil), f.evs...), nil
}
func (f *fakeEventStore) PruneEvents(time.Time) (int, error)              { return 0, nil }
func (f *fakeEventStore) PruneEventsOrg(string, time.Time) (int, error)   { return 0, nil }
func (f *fakeEventStore) DeleteEventsCluster(string, string) (int, error) { return 0, nil }
func (f *fakeEventStore) EventClusterIDs(string) ([]string, error)        { return nil, nil }

// Runtime events carry command lines: the same entitlement as findings, and
// "entitled to nothing" is an error, never a quiet feed.
func TestRuntimeEventSource_EntitlementAndNothingIsAnError(t *testing.T) {
	ev := func(c string) findings.EventRecord { return findings.EventRecord{ClusterID: c} }
	store := &fakeEventStore{evs: []findings.EventRecord{ev("mine-1"), ev("other-team")}}
	got, err := runtimeEventSource{store: store}.List(ctxWithScope(scopeWith("", []string{"mine-1"}, true)), findings.EventQuery{}, true)
	if err != nil || len(got) != 1 || got[0].ClusterID != "mine-1" {
		t.Errorf("got %v err %v — want only the caller's cluster", got, err)
	}
	if _, err := (runtimeEventSource{store: store}).List(ctxWithScope(scopeWith("", nil, true)), findings.EventQuery{}, true); err == nil {
		t.Error("a caller entitled to nothing got a quiet feed instead of an error")
	}
}
