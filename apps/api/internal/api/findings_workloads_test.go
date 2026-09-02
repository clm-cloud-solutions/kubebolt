package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

func wlRec(name, ns string, sev integrations.FindingSeverity, cis, remediation string) findings.Record {
	r := findings.Record{Status: findings.StatusActive}
	r.Kind = integrations.FindingCVE
	r.Source = "trivy"
	r.Severity = sev
	r.ResourceKind = "Deployment"
	r.ResourceNamespace = ns
	r.ResourceName = name
	r.CISControl = cis
	r.Remediation = remediation
	return r
}

func workloadsFor(t *testing.T, recs []findings.Record, query string) workloadsResponse {
	t.Helper()
	h := &handlers{findingsStore: &fakeFindingsStore{records: recs}}
	rr := httptest.NewRecorder()
	h.handleListFindingWorkloads(rr, httptest.NewRequest("GET", "/findings/workloads"+query, nil))
	var out workloadsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body)
	}
	return out
}

// Severity BEFORE volume: the list answers "what do I touch first", not "who
// has the biggest pile". One critical outranks fifty highs.
func TestWorkloads_WorstFirstBySeverityNotVolume(t *testing.T) {
	recs := []findings.Record{wlRec("one-critical", "a", integrations.SeverityCritical, "", "fix")}
	for i := 0; i < 50; i++ {
		recs = append(recs, wlRec("many-high", "b", integrations.SeverityHigh, "", "fix"))
	}
	out := workloadsFor(t, recs, "")
	if len(out.Workloads) != 2 {
		t.Fatalf("got %d workloads, want 2", len(out.Workloads))
	}
	if out.Workloads[0].Name != "one-critical" {
		t.Errorf("first row = %q, want one-critical — 50 highs must not outrank a critical",
			out.Workloads[0].Name)
	}
}

// A compliance control describes the CLUSTER, so it has no workload. It is
// counted and reported, never bucketed into a synthetic row — a total that does
// not add up is what erodes trust in the page.
func TestWorkloads_ControlsAreReportedNotBucketed(t *testing.T) {
	out := workloadsFor(t, []findings.Record{
		wlRec("web", "prod", integrations.SeverityHigh, "", "fix"),
		func() findings.Record {
			r := wlRec("", "", integrations.SeverityMedium, "5.2.7", "see CIS")
			r.Kind = integrations.FindingMisconfig
			return r
		}(),
	}, "")

	if len(out.Workloads) != 1 || out.Workloads[0].Name != "web" {
		t.Fatalf("workloads = %+v, want just web", out.Workloads)
	}
	if out.Unassigned != 1 {
		t.Errorf("unassigned = %d, want 1 — the control must be stated, not dropped", out.Unassigned)
	}
}

// The badges are the whole point of the row: per-severity counts, plus how many
// of them can actually be closed.
func TestWorkloads_CountsPerSeverityAndFixable(t *testing.T) {
	out := workloadsFor(t, []findings.Record{
		wlRec("web", "prod", integrations.SeverityCritical, "", "bump it"),
		wlRec("web", "prod", integrations.SeverityHigh, "", "bump it"),
		wlRec("web", "prod", integrations.SeverityHigh, "", ""),
	}, "")

	w := out.Workloads[0]
	if w.Critical != 1 || w.High != 2 || w.Total != 3 {
		t.Errorf("counts = crit %d / high %d / total %d, want 1/2/3", w.Critical, w.High, w.Total)
	}
	if w.Fixable != 2 {
		t.Errorf("fixable = %d, want 2 — the third has no remedy", w.Fixable)
	}
}

// The same workload name exists in every cluster — `coredns` in kube-system is
// the canonical case. Without the cluster in the key they merge into one row
// with summed counts, and the drill-down mixes findings from machines that have
// nothing to do with each other.
func TestWorkloads_SameNameInTwoClustersStaysTwoRows(t *testing.T) {
	a := wlRec("coredns", "kube-system", integrations.SeverityHigh, "", "fix")
	a.ClusterID = "cluster-a"
	b := wlRec("coredns", "kube-system", integrations.SeverityCritical, "", "fix")
	b.ClusterID = "cluster-b"

	out := workloadsFor(t, []findings.Record{a, b}, "")
	if len(out.Workloads) != 2 {
		t.Fatalf("got %d rows, want 2 — the two clusters' coredns merged: %+v", len(out.Workloads), out.Workloads)
	}
	seen := map[string]bool{}
	for _, w := range out.Workloads {
		if w.ClusterID == "" {
			t.Error("row carries no clusterId, so the UI cannot tell them apart")
		}
		if seen[w.ClusterID] {
			t.Errorf("two rows for cluster %s", w.ClusterID)
		}
		seen[w.ClusterID] = true
		if w.Total != 1 {
			t.Errorf("row for %s has total %d, want 1 — counts were summed across clusters",
				w.ClusterID, w.Total)
		}
	}
}

// Checks rank by WORKLOADS, not by findings: a check is one manifest pattern,
// so the number of places it must be changed IS the work. That is the opposite
// of an image, where the pile of CVEs is what a single rebuild clears.
func TestWorkloads_ChecksRankByBreadthNotVolume(t *testing.T) {
	mk := func(title, ns, name string, sev integrations.FindingSeverity) findings.Record {
		r := wlRec(name, ns, sev, "", "fix")
		r.Kind = integrations.FindingMisconfig
		r.Title = title
		r.ClusterID = "c1"
		return r
	}
	recs := []findings.Record{
		// One workload, many findings of the same check.
		mk("AVD-KSV-0001: deep", "a", "one", integrations.SeverityHigh),
		mk("AVD-KSV-0001: deep", "a", "one", integrations.SeverityHigh),
		mk("AVD-KSV-0001: deep", "a", "one", integrations.SeverityHigh),
		// Two workloads, fewer findings — but broader, so more manifests to edit.
		mk("AVD-KSV-0012: broad", "a", "x", integrations.SeverityMedium),
		mk("AVD-KSV-0012: broad", "b", "y", integrations.SeverityMedium),
	}
	out := workloadsFor(t, recs, "?group=configuration")
	if len(out.TopChecks) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(out.TopChecks), out.TopChecks)
	}
	if out.TopChecks[0].Title != "AVD-KSV-0012: broad" {
		t.Errorf("first check = %q, want the one spanning more workloads — breadth is the work",
			out.TopChecks[0].Title)
	}
	if out.TopChecks[0].Workloads != 2 {
		t.Errorf("workloads = %d, want 2", out.TopChecks[0].Workloads)
	}
	if len(out.TopChecks[0].WorkloadNames) != 2 {
		t.Errorf("workloadNames = %v, want both places named so they can be found",
			out.TopChecks[0].WorkloadNames)
	}
}

// A CVE is specific to what an image ships, so it is not a repeatable "check"
// and must not be grouped as one — that panel answers a manifest question.
func TestWorkloads_CVEsAreNotGroupedAsChecks(t *testing.T) {
	out := workloadsFor(t, []findings.Record{
		wlRec("web", "prod", integrations.SeverityHigh, "", "bump"),
	}, "?group=vulnerability")
	if len(out.TopChecks) != 0 {
		t.Errorf("CVEs leaked into the checks panel: %+v", out.TopChecks)
	}
}

// Compliance controls carry no workload, so they are skipped by the workload
// aggregation. The benchmark breakdown must be counted BEFORE that skip — it is
// the only shape this lens has, and losing it leaves the tab empty beside its
// donut.
func TestWorkloads_BenchmarksCountedDespiteHavingNoWorkload(t *testing.T) {
	mk := func(bench string, sev integrations.FindingSeverity, rollup bool) findings.Record {
		r := findings.Record{Status: findings.StatusActive}
		r.Kind = integrations.FindingMisconfig
		r.Severity = sev
		r.CISControl = "5.2.7"
		r.Benchmark = bench
		r.Rollup = rollup
		return r
	}
	out := workloadsFor(t, []findings.Record{
		mk("CIS Kubernetes Benchmarks v1.23", integrations.SeverityHigh, true),
		mk("CIS Kubernetes Benchmarks v1.23", integrations.SeverityMedium, false),
		mk("National Security Agency Hardening", integrations.SeverityCritical, false),
	}, "?group=compliance")

	if len(out.Workloads) != 0 {
		t.Fatalf("controls leaked into the workload list: %+v", out.Workloads)
	}
	if len(out.Benchmarks) != 2 {
		t.Fatalf("got %d benchmarks, want 2: %+v", len(out.Benchmarks), out.Benchmarks)
	}
	// Worst first.
	if out.Benchmarks[0].Name != "CIS Kubernetes Benchmarks v1.23" || out.Benchmarks[0].Failing != 2 {
		t.Errorf("first row = %+v, want CIS with 2 failing", out.Benchmarks[0])
	}
	if out.Benchmarks[0].Rollups != 1 {
		t.Errorf("rollups = %d, want 1 — stated so the number reconciles with totals that exclude them",
			out.Benchmarks[0].Rollups)
	}
}

// secretRec is an exposed-secret finding on a workload.
func secretRec(name, ns string, sev integrations.FindingSeverity) findings.Record {
	r := wlRec(name, ns, sev, "", "rotate it")
	r.Kind = integrations.FindingExposedSecret
	return r
}

// A leaked credential outranks any pile of CVEs, whatever the severity counts.
//
// This is the shape that failed in the field: measured on the dev cluster, a
// workload with a leaked AWS credential pair ranked 29th of 37 — page TWO — for
// having one critical while twenty-eight others had two or more. A critical CVE
// is a rebuild you schedule; a credential in a registry is already out.
func TestWorkloads_SecretsOutrankAnyPileOfCVEs(t *testing.T) {
	recs := []findings.Record{secretRec("leaky", "lab", integrations.SeverityCritical)}
	for i := 0; i < 30; i++ {
		recs = append(recs, wlRec("crit-pile", "prod", integrations.SeverityCritical, "", "upgrade"))
	}
	out := workloadsFor(t, recs, "")
	if out.Workloads[0].Name != "leaky" {
		t.Errorf("first row = %q, want leaky — 30 criticals must not bury one leaked credential",
			out.Workloads[0].Name)
	}
}

// The severity ordering INSIDE each group is untouched: only the first sort key
// changed. Two secret-carrying workloads still rank against each other normally.
func TestWorkloads_SeverityOrderSurvivesWithinTheSecretGroup(t *testing.T) {
	out := workloadsFor(t, []findings.Record{
		secretRec("mild", "a", integrations.SeverityMedium),
		secretRec("severe", "b", integrations.SeverityCritical),
	}, "")
	if out.Workloads[0].Name != "severe" {
		t.Errorf("first row = %q, want severe — inside the group severity still decides",
			out.Workloads[0].Name)
	}
}

// Kinds is what tells a patch problem from a credential problem. The field it
// replaced named the SCANNER, and every one of these reports "trivy" — so the
// row said nothing, which is why it was never rendered.
func TestWorkloads_KindsDiscriminateWhereSourceCannot(t *testing.T) {
	out := workloadsFor(t, []findings.Record{
		wlRec("mixed", "prod", integrations.SeverityHigh, "", "upgrade"),
		wlRec("mixed", "prod", integrations.SeverityHigh, "", "upgrade"),
		secretRec("mixed", "prod", integrations.SeverityCritical),
	}, "")
	if len(out.Workloads) != 1 {
		t.Fatalf("got %d workloads, want 1", len(out.Workloads))
	}
	row := out.Workloads[0]
	if row.Kinds["cve"] != 2 || row.Kinds["exposed_secret"] != 1 {
		t.Errorf("Kinds = %v, want 2 cve + 1 exposed_secret", row.Kinds)
	}
	if row.Secrets != 1 {
		t.Errorf("Secrets = %d, want 1 — it drives the sort and must not depend on a map lookup", row.Secrets)
	}
}

// The kind chip must narrow the TABLE, not only the summary.
//
// It shipped wired on one side only: the finding list took `kind`, the workload
// list did not, so picking "Secrets 2" recomputed the strip and left every
// workload in the lens on screen. A filter that visibly does nothing is worse
// than no filter — the operator concludes the data is wrong.
func TestWorkloads_KindFacetNarrowsTheTable(t *testing.T) {
	recs := []findings.Record{secretRec("leaky", "lab", integrations.SeverityCritical)}
	for i := 0; i < 5; i++ {
		recs = append(recs, wlRec("patchy", "prod", integrations.SeverityHigh, "", "upgrade"))
	}
	out := workloadsFor(t, recs, "?kind=exposed_secret")
	if len(out.Workloads) != 1 {
		t.Fatalf("got %d workloads, want 1 — the kind facet must reach this endpoint", len(out.Workloads))
	}
	if out.Workloads[0].Name != "leaky" {
		t.Errorf("row = %q, want leaky", out.Workloads[0].Name)
	}
}

// Filtering happens on the RECORDS, so the severity bar describes what survived
// the filter. Showing a workload's CVE severities under a Secrets filter would
// describe findings the filter just excluded.
func TestWorkloads_KindFacetRecountsSeverities(t *testing.T) {
	recs := []findings.Record{secretRec("mixed", "prod", integrations.SeverityMedium)}
	for i := 0; i < 9; i++ {
		recs = append(recs, wlRec("mixed", "prod", integrations.SeverityCritical, "", "upgrade"))
	}
	out := workloadsFor(t, recs, "?kind=exposed_secret")
	if len(out.Workloads) != 1 {
		t.Fatalf("got %d workloads, want 1", len(out.Workloads))
	}
	row := out.Workloads[0]
	if row.Critical != 0 || row.Medium != 1 || row.Total != 1 {
		t.Errorf("counts = %d critical / %d medium / %d total; want 0/1/1 — the bar must "+
			"describe the filtered set, not the workload's whole pile",
			row.Critical, row.Medium, row.Total)
	}
}
