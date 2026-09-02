package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// fakeFindingsStore records the Query it was asked for and replays a fixed
// set. Only List is exercised — the handler is read-only.
type fakeFindingsStore struct {
	records  []findings.Record
	gotQuery findings.Query
}

func (f *fakeFindingsStore) Upsert(*findings.Record) error { return nil }
func (f *fakeFindingsStore) MarkResolved(string, string, string, time.Time) error {
	return nil
}
func (f *fakeFindingsStore) Get(string, string, string) (*findings.Record, bool, error) {
	return nil, false, nil
}
func (f *fakeFindingsStore) Prune(time.Time) (int, error)            { return 0, nil }
func (f *fakeFindingsStore) PruneOrg(string, time.Time) (int, error) { return 0, nil }

// List applies ONLY the scope dimensions, the way the real stores do — so a
// handler bug that leaks a facet into the query shows up as a changed row set.
func (f *fakeFindingsStore) List(q findings.Query) ([]findings.Record, error) {
	f.gotQuery = q
	out := make([]findings.Record, 0, len(f.records))
	for _, r := range f.records {
		if q.Status != "" && r.Status != q.Status {
			continue
		}
		if q.ClusterID != "" && r.ClusterID != q.ClusterID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func mkFinding(source string, kind integrations.FindingKind, sev integrations.FindingSeverity, firstSeen time.Time) findings.Record {
	return findings.Record{
		ClusterID: "c1",
		Status:    findings.StatusActive,
		FirstSeen: firstSeen,
		Finding: integrations.Finding{
			Source:   source,
			Kind:     kind,
			Severity: sev,
		},
	}
}

// TestListFindingsSummaryIsFacetIndependent is the regression lock for the two
// bugs the as-built handler shipped with: it computed the summary AFTER
// filtering, so (a) the source pills — rendered from BySource's keys —
// erased themselves the moment you clicked one, and (b) the KPIs silently
// re-scoped to the active filter. Uncapped never meant unfiltered.
func TestListFindingsSummaryIsFacetIndependent(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeFindingsStore{records: []findings.Record{
		mkFinding("trivy", integrations.FindingCVE, integrations.SeverityCritical, now.Add(-1*time.Hour)),
		mkFinding("trivy", integrations.FindingCVE, integrations.SeverityHigh, now.Add(-48*time.Hour)),
		mkFinding("kyverno", integrations.FindingKind("policy_violation"), integrations.SeverityMedium, now.Add(-2*time.Hour)),
	}}
	h := &handlers{findingsStore: store}

	get := func(rawQuery string) findingsResponse {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/findings?"+rawQuery, nil)
		w := httptest.NewRecorder()
		h.handleListFindings(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var resp findingsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	unfiltered := get("")
	if len(unfiltered.Findings) != 3 || unfiltered.Total != 3 {
		t.Fatalf("unfiltered rows = %d (total %d), want 3", len(unfiltered.Findings), unfiltered.Total)
	}
	if len(unfiltered.BySource) != 2 {
		t.Fatalf("BySource keys = %d, want 2 (trivy, kyverno)", len(unfiltered.BySource))
	}

	filtered := get("source=trivy")

	// Rows DO narrow.
	if filtered.Total != 2 {
		t.Errorf("filtered Total = %d, want 2", filtered.Total)
	}
	// Summary does NOT. This is the pills bug: both sources must survive so
	// the user can switch without going back through "all".
	if len(filtered.BySource) != 2 {
		t.Errorf("BySource collapsed to %d keys under a facet — the pills erase themselves: %v",
			len(filtered.BySource), filtered.BySource)
	}
	if filtered.ScopeTotal != 3 {
		t.Errorf("ScopeTotal = %d, want 3 (the KPI denominator must not follow the facet)", filtered.ScopeTotal)
	}
	if filtered.BySeverity["medium"] != 1 {
		t.Errorf("BySeverity lost the kyverno row under a trivy facet: %v", filtered.BySeverity)
	}
	if filtered.ByKind[string(integrations.FindingCVE)] != 2 {
		t.Errorf("ByKind = %v, want 2 CVEs", filtered.ByKind)
	}

	// The facet must never reach the store — it is applied in memory so a
	// single read can serve both the scope-wide summary and the narrowed rows.
	if store.gotQuery.Source != "" {
		t.Errorf("facet leaked into the store query: Source=%q", store.gotQuery.Source)
	}
	if store.gotQuery.Status != findings.StatusActive {
		t.Errorf("scope Status = %q, want the active default", store.gotQuery.Status)
	}
}

// TestListFindingsNewLast24h pins the KPI that cannot be derived client-side:
// the table caps at 200 rows sorted by LastSeen, so the newest arrivals may
// not even be in the payload the browser receives.
func TestListFindingsNewLast24h(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeFindingsStore{records: []findings.Record{
		mkFinding("trivy", integrations.FindingCVE, integrations.SeverityCritical, now.Add(-1*time.Hour)),
		mkFinding("trivy", integrations.FindingCVE, integrations.SeverityHigh, now.Add(-23*time.Hour)),
		mkFinding("trivy", integrations.FindingCVE, integrations.SeverityHigh, now.Add(-25*time.Hour)),
	}}
	h := &handlers{findingsStore: store}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/findings", nil)
	w := httptest.NewRecorder()
	h.handleListFindings(w, r)

	var resp findingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NewLast24h != 2 {
		t.Errorf("NewLast24h = %d, want 2 (the 25h-old one is outside the window)", resp.NewLast24h)
	}
}

// TestListFindingsUnavailableWithoutStore keeps the nil-store 503 explicit:
// persistence is optional, and the dashboard must get a clear signal rather
// than an empty success that reads as "your fleet is clean".
func TestListFindingsUnavailableWithoutStore(t *testing.T) {
	h := &handlers{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/findings", nil)
	w := httptest.NewRecorder()
	h.handleListFindings(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// Home focuses one team and must show ITS numbers, not the whole entitlement's.
// The lens lives in localStorage and never reaches the server, so the API has to
// ship a per-cluster tally the page can fold — the same shape Fleet uses.
func TestFindings_PerClusterTallyLetsTheCallerFold(t *testing.T) {
	recs := []findings.Record{}
	add := func(cluster string, sev integrations.FindingSeverity, n int) {
		for i := 0; i < n; i++ {
			r := findings.Record{Status: findings.StatusActive, ClusterID: cluster}
			r.Kind = integrations.FindingCVE
			r.Source = "trivy"
			r.Severity = sev
			r.ResourceName = "web"
			recs = append(recs, r)
		}
	}
	add("team-a-cluster", integrations.SeverityCritical, 3)
	add("team-b-cluster", integrations.SeverityCritical, 7)

	h := &handlers{findingsStore: &fakeFindingsStore{records: recs}}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/findings", nil)
	w := httptest.NewRecorder()
	h.handleListFindings(w, r)
	var out findingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The org-wide number stays what it was — nothing about the summary changed.
	if got := out.BySeverity["critical"]; got != 10 {
		t.Errorf("BySeverity[critical] = %d, want 10", got)
	}
	// And the breakdown lets a focused page arrive at 3 without a second query.
	if got := out.BySeverityCluster["team-a-cluster"]["critical"]; got != 3 {
		t.Errorf("team-a fold = %d, want 3 — without this Home shows 10 over a list of 3", got)
	}
	if got := out.BySeverityCluster["team-b-cluster"]["critical"]; got != 7 {
		t.Errorf("team-b fold = %d, want 7", got)
	}
}
