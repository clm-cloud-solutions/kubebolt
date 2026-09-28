package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// The Security pillar had zero Kobi coverage while carrying 1,224 active rows
// in one production org. Listing them is 245KB against a 32KB tool cap, and
// "1,224 findings" answers nothing — so the default answer is a posture.

type stubFindings struct {
	gotQuery findings.Query
	gotAll   bool
	recs     []findings.Record
	err      error

	detail                     *findings.Detail
	gotCluster, gotFingerprint string
}

func (s *stubFindings) List(_ context.Context, q findings.Query, all bool) ([]findings.Record, error) {
	s.gotQuery, s.gotAll = q, all
	return s.recs, s.err
}

func (s *stubFindings) Detail(_ context.Context, clusterID, fingerprint string) (*findings.Detail, bool, error) {
	s.gotCluster, s.gotFingerprint = clusterID, fingerprint
	if s.err != nil {
		return nil, false, s.err
	}
	return s.detail, s.detail != nil, nil
}

func finding(mut func(*findings.Record)) findings.Record {
	rec := findings.Record{
		ClusterID:   "c1",
		Fingerprint: strings.Repeat("f", 64),
		Finding: integrations.Finding{
			Kind: "vulnerability", Source: "trivy", Severity: "high",
			Title:             "CVE-2024-1234",
			ResourceKind:      "Deployment",
			ResourceNamespace: "payments",
			ResourceName:      "api",
			Image:             "registry/app:v1",
			Remediation:       "upgrade to 1.2.3",
		},
		Status: findings.StatusActive, FirstSeen: time.Now().Add(-48 * time.Hour),
	}
	if mut != nil {
		mut(&rec)
	}
	return rec
}

func callFindings(t *testing.T, e *Executor, input string) (ToolResult, map[string]any) {
	t.Helper()
	return callFindingTool(t, e, "get_findings", input)
}

func callFindingTool(t *testing.T, e *Executor, tool, input string) (ToolResult, map[string]any) {
	t.Helper()
	if input == "" {
		input = "{}"
	}
	res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: tool, Input: json.RawMessage(input)})
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, res.Content)
	}
	return res, payload
}

func TestFindings_UnfilteredReturnsPostureNotAList(t *testing.T) {
	var recs []findings.Record
	for i := 0; i < 200; i++ {
		i := i
		recs = append(recs, finding(func(r *findings.Record) {
			r.ResourceName = fmt.Sprintf("app-%d", i%12)
			if i%10 == 0 {
				r.Severity = "critical"
			}
		}))
	}
	src := &stubFindings{recs: recs}
	res, payload := callFindings(t, NewExecutor(nil).WithFindings(src), "")

	if _, ok := payload["findings"]; ok {
		t.Error("an unfiltered request returned rows; 1,224 of these do not fit in a tool result")
	}
	if payload["total"] != float64(200) {
		t.Errorf("total = %v, want 200", payload["total"])
	}
	sev, _ := payload["bySeverity"].(map[string]any)
	if sev["critical"] != float64(20) {
		t.Errorf("critical = %v, want 20", sev["critical"])
	}
	// "1,224 findings" means nothing; "across 12 workloads" is the sentence
	// that makes it a morning's work or a quarter's.
	if payload["affectedWorkloads"] != float64(12) {
		t.Errorf("affectedWorkloads = %v, want 12", payload["affectedWorkloads"])
	}
	if _, ok := payload["hint"]; !ok {
		t.Error("no hint telling the model how to get the rows")
	}
	if len(res.Content) > 4*1024 {
		t.Errorf("%d bytes for a posture — it is meant to be cheap", len(res.Content))
	}
}

// The image is the unit of repair: two workloads on the same image are ONE
// fix, and a ranked list of workloads never says that.
func TestFindings_SurfacesTheImagesThatCarryTheProblem(t *testing.T) {
	recs := []findings.Record{
		finding(func(r *findings.Record) { r.Image = "reg/a:1"; r.ResourceName = "w1" }),
		finding(func(r *findings.Record) { r.Image = "reg/a:1"; r.ResourceName = "w2"; r.Severity = "critical" }),
		finding(func(r *findings.Record) { r.Image = "reg/a:1"; r.ResourceName = "w3" }),
		finding(func(r *findings.Record) { r.Image = "reg/b:1"; r.ResourceName = "w4" }),
	}
	_, payload := callFindings(t, NewExecutor(nil).WithFindings(&stubFindings{recs: recs}), "")
	imgs, _ := payload["topImages"].([]any)
	if len(imgs) != 2 {
		t.Fatalf("topImages = %v", payload["topImages"])
	}
	first, _ := imgs[0].(map[string]any)
	if first["image"] != "reg/a:1" || first["findings"] != float64(3) || first["critical"] != float64(1) {
		t.Errorf("worst image = %v", first)
	}
	if _, ok := payload["imageNote"]; !ok {
		t.Error("nothing tells the model that rebuilding the image fixes every workload on it")
	}
}

// Mirrors api/findings.go: a Rollup control aggregates findings stored
// individually, so counting both inflates every number by the same problem
// twice. The row still ships — it is the counting that stops.
func TestFindings_RollupsShipButAreNotCounted(t *testing.T) {
	recs := []findings.Record{
		finding(nil),
		finding(func(r *findings.Record) {
			r.Rollup = true
			r.Kind = "compliance"
			r.Title = "CIS 5.2.6: 52 containers run as root"
			r.ResourceName = ""
		}),
	}
	_, payload := callFindings(t, NewExecutor(nil).WithFindings(&stubFindings{recs: recs}), "")
	if payload["total"] != float64(1) {
		t.Errorf("total = %v, want 1 — the rollup double-counts its own parts", payload["total"])
	}
	if payload["rollupsExcluded"] != float64(1) {
		t.Errorf("rollupsExcluded = %v; the UI cannot explain the gap without it", payload["rollupsExcluded"])
	}
	// And it must still be reachable as a row.
	_, rows := callFindings(t, NewExecutor(nil).WithFindings(&stubFindings{recs: recs}), `{"kind":"compliance"}`)
	list, _ := rows["findings"].([]any)
	if len(list) != 2 {
		t.Fatalf("rows = %d, want both — compliance posture is a real question", len(list))
	}
}

func TestFindings_FacetsReturnRowsWithTheirRemediation(t *testing.T) {
	src := &stubFindings{recs: []findings.Record{finding(func(r *findings.Record) { r.Severity = "critical" })}}
	_, payload := callFindings(t, NewExecutor(nil).WithFindings(src), `{"severity":"critical"}`)

	if src.gotQuery.Severity != "critical" {
		t.Errorf("the facet never reached the store: %+v", src.gotQuery)
	}
	rows, _ := payload["findings"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", payload["findings"])
	}
	row, _ := rows[0].(map[string]any)
	if row["remediation"] != "upgrade to 1.2.3" {
		t.Error("the row carries the problem but not the fix")
	}
	if row["resource"] != "payments/api" {
		t.Errorf("resource = %v", row["resource"])
	}
	if row["image"] != "registry/app:v1" {
		t.Error("the row hides the thing you actually rebuild")
	}
}

// image and resource have no column in findings.Query, so they filter in
// memory — and must still narrow.
func TestFindings_InMemoryFacetsNarrow(t *testing.T) {
	recs := []findings.Record{
		finding(func(r *findings.Record) { r.Image = "reg/a:1" }),
		finding(func(r *findings.Record) { r.Image = "reg/b:1" }),
	}
	_, payload := callFindings(t, NewExecutor(nil).WithFindings(&stubFindings{recs: recs}), `{"image":"reg/a:1"}`)
	if payload["total"] != float64(1) {
		t.Errorf("total = %v, want 1", payload["total"])
	}
	rows, _ := payload["findings"].([]any)
	if len(rows) != 1 {
		t.Errorf("rows = %d, want 1", len(rows))
	}
}

func TestFindings_DefaultsToActiveAndHonoursCluster(t *testing.T) {
	src := &stubFindings{}
	callFindings(t, NewExecutor(nil).WithFindings(src), "")
	if src.gotQuery.Status != findings.StatusActive {
		t.Errorf("status = %q, want active — the archive is nobody's question", src.gotQuery.Status)
	}
	if src.gotAll {
		t.Error("defaulted to every cluster")
	}
	src2 := &stubFindings{}
	callFindings(t, NewExecutor(nil).WithFindings(src2), `{"cluster":"all","status":"resolved"}`)
	if !src2.gotAll || src2.gotQuery.Status != "resolved" {
		t.Errorf("all=%v status=%q", src2.gotAll, src2.gotQuery.Status)
	}
}

func TestFindings_RowsAreCapped(t *testing.T) {
	var recs []findings.Record
	for i := 0; i < 500; i++ {
		recs = append(recs, finding(func(r *findings.Record) { r.Severity = "critical" }))
	}
	res, payload := callFindings(t, NewExecutor(nil).WithFindings(&stubFindings{recs: recs}), `{"severity":"critical","limit":9999}`)
	if res.IsError {
		t.Fatal(res.Content)
	}
	rows, _ := payload["findings"].([]any)
	if len(rows) != maxFindingRows {
		t.Errorf("rows = %d, want the cap %d", len(rows), maxFindingRows)
	}
	if payload["truncated"] != true {
		t.Error("cut without saying so")
	}
	if payload["total"] != float64(500) {
		t.Errorf("total = %v; the real count must survive the cap", payload["total"])
	}
}

// "Not available" and "nothing found" are different answers, and collapsing
// them reports a clean cluster to someone who has no scanner installed.
func TestFindings_UnavailableIsNotACleanCluster(t *testing.T) {
	res := NewExecutor(nil).ExecuteCtx(context.Background(),
		ToolCall{ID: "c1", Name: "get_findings", Input: json.RawMessage(`{}`)})
	if !res.IsError {
		t.Error("reported success with no store")
	}
	low := strings.ToLower(res.Content)
	if strings.Contains(low, `"total":0`) {
		t.Error("returned a zero posture, which reads as 'you are secure'")
	}
	if !strings.Contains(low, "not a clean cluster") {
		t.Errorf("does not distinguish absence from safety: %s", res.Content)
	}
}

func TestFindings_ReaderErrorSurfaces(t *testing.T) {
	src := &stubFindings{err: fmt.Errorf("bolt is closed")}
	res, _ := callFindings(t, NewExecutor(nil).WithFindings(src), "")
	if !res.IsError || !strings.Contains(res.Content, "bolt is closed") {
		t.Errorf("swallowed the cause: %s", res.Content)
	}
}

// Go map order is random; a summary whose "worst" flips between identical
// calls teaches the reader to distrust it.
func TestFindings_TopsAreDeterministic(t *testing.T) {
	recs := []findings.Record{
		finding(func(r *findings.Record) { r.ResourceName = "bbb"; r.Image = "reg/bbb:1" }),
		finding(func(r *findings.Record) { r.ResourceName = "aaa"; r.Image = "reg/aaa:1" }),
	}
	var worst, img string
	for i := 0; i < 25; i++ {
		_, payload := callFindings(t, NewExecutor(nil).WithFindings(&stubFindings{recs: recs}), "")
		w, _ := payload["worstWorkload"].(map[string]any)
		imgs, _ := payload["topImages"].([]any)
		first, _ := imgs[0].(map[string]any)
		if i == 0 {
			worst, img = fmt.Sprint(w["resource"]), fmt.Sprint(first["image"])
			continue
		}
		if got := fmt.Sprint(w["resource"]); got != worst {
			t.Fatalf("worstWorkload flipped: %q vs %q", worst, got)
		}
		if got := fmt.Sprint(first["image"]); got != img {
			t.Fatalf("topImages flipped: %q vs %q", img, got)
		}
	}
}

// The definition lives in tools.go and the case in executor.go; a tool that
// exists only in one of them is invisible to the model. This shipped that way
// for a moment — the drift test in prompt_causality_test.go caught it because
// the prompt named a tool the catalogue did not have.
func TestFindings_IsPublishedAndReadOnly(t *testing.T) {
	var def *ToolDefinition
	for _, d := range GovernedToolDefinitions(false, false) {
		if d.Name == "get_findings" {
			dd := d
			def = &dd
		}
	}
	if def == nil {
		t.Fatal("get_findings is missing from the read-only catalogue MCP publishes")
	}
	low := strings.ToLower(def.Description)
	for _, cue := range []string{"posture", "image", "not a clean cluster"} {
		if !strings.Contains(low, cue) {
			t.Errorf("the description does not orient the model: missing %q", cue)
		}
	}
	props, _ := def.InputSchema["properties"].(map[string]interface{})
	for _, facet := range []string{"severity", "source", "kind", "image", "resource", "status", "cluster", "limit"} {
		if _, ok := props[facet]; !ok {
			t.Errorf("facet %q is accepted by the executor but not declared in the schema", facet)
		}
	}
}

// ─── get_finding_workloads ───────────────────────────────────────────────

func workloadFinding(name string, sev integrations.FindingSeverity, kind integrations.FindingKind) findings.Record {
	return finding(func(r *findings.Record) {
		r.ResourceName, r.Severity, r.Kind = name, sev, kind
		r.Title = fmt.Sprintf("%s-%s-%s", name, sev, kind)
	})
}

func workloadNames(t *testing.T, payload map[string]any) []string {
	t.Helper()
	rows, _ := payload["workloads"].([]any)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.(map[string]any)["resource"].(string))
	}
	return out
}

// Same order as the Security page: a leaked credential outranks any pile of
// CVEs, and one critical outranks three highs. The chat must not rank a
// workload differently from the screen the operator is looking at.
func TestFindingWorkloads_RanksLikeTheSecurityPage(t *testing.T) {
	src := &stubFindings{recs: []findings.Record{
		workloadFinding("highs", "high", integrations.FindingCVE),
		workloadFinding("highs", "high", "vulnerability-2"),
		workloadFinding("highs", "high", "vulnerability-3"),
		workloadFinding("one-critical", "critical", integrations.FindingCVE),
		workloadFinding("leaked-key", "low", integrations.FindingExposedSecret),
	}}
	_, payload := callFindingTool(t, NewExecutor(nil).WithFindings(src), "get_finding_workloads", "")
	got := strings.Join(workloadNames(t, payload), ",")
	want := "payments/leaked-key,payments/one-critical,payments/highs"
	if got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	if src.gotQuery.Status != findings.StatusActive {
		t.Errorf("status = %q, want active by default", src.gotQuery.Status)
	}
}

// A facet narrows the RECORDS: under kind=exposed_secret the row counts the
// key, not the thirty CVEs beside it.
func TestFindingWorkloads_FacetsNarrowTheRecordsNotTheRows(t *testing.T) {
	var recs []findings.Record
	for i := 0; i < 30; i++ {
		recs = append(recs, workloadFinding("api", "critical", integrations.FindingCVE))
	}
	recs = append(recs, workloadFinding("api", "low", integrations.FindingExposedSecret))
	src := &stubFindings{recs: recs}
	_, payload := callFindingTool(t, NewExecutor(nil).WithFindings(src), "get_finding_workloads", `{"kind":"exposed_secret"}`)
	rows := payload["workloads"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0].(map[string]any)
	if row["total"].(float64) != 1 || row["critical"] != nil {
		t.Errorf("row = %v — counted findings the filter excluded", row)
	}
}

func TestFindingWorkloads_CapsRowsAndSaysSo(t *testing.T) {
	var recs []findings.Record
	for i := 0; i < 30; i++ {
		recs = append(recs, workloadFinding(fmt.Sprintf("app-%02d", i), "high", integrations.FindingCVE))
	}
	_, payload := callFindingTool(t, NewExecutor(nil).WithFindings(&stubFindings{recs: recs}), "get_finding_workloads", "")
	if payload["totalWorkloads"].(float64) != 30 || payload["truncated"] != true {
		t.Errorf("total=%v truncated=%v — a cut list must say it is cut", payload["totalWorkloads"], payload["truncated"])
	}
	if n := len(payload["workloads"].([]any)); n != defaultFindingWorkloads {
		t.Errorf("rows = %d, want %d", n, defaultFindingWorkloads)
	}
}

// Across the org a row must name its cluster: the same Deployment exists in
// every cluster, and get_finding_detail needs the id to find the row again.
func TestFindingWorkloads_OrgScopeStampsTheCluster(t *testing.T) {
	src := &stubFindings{recs: []findings.Record{workloadFinding("api", "high", integrations.FindingCVE)}}
	e := NewExecutor(nil).WithFindings(src)

	_, payload := callFindingTool(t, e, "get_finding_workloads", `{"cluster":"all"}`)
	row := payload["workloads"].([]any)[0].(map[string]any)
	if !src.gotAll || row["clusterId"] != "c1" {
		t.Errorf("all=%v clusterId=%v — org scope must span clusters and say which", src.gotAll, row["clusterId"])
	}

	_, payload = callFindingTool(t, e, "get_finding_workloads", "")
	row = payload["workloads"].([]any)[0].(map[string]any)
	if _, ok := row["clusterId"]; ok {
		t.Error("single-cluster rows carry the cluster id — noise when every row shares it")
	}
}

// ─── get_finding_detail ──────────────────────────────────────────────────

func cveDetail(mut func(*findings.Detail)) *findings.Detail {
	d := &findings.Detail{Record: finding(nil), Live: true}
	d.Kind = integrations.FindingCVE
	if mut != nil {
		mut(d)
	}
	return d
}

func TestFindingDetail_PassesTheFingerprintAndTheRowsCluster(t *testing.T) {
	src := &stubFindings{detail: cveDetail(nil)}
	e := NewExecutor(nil).WithFindings(src)

	res, _ := callFindingTool(t, e, "get_finding_detail", `{"fingerprint":"fp9","clusterId":"uid-2"}`)
	if res.IsError || src.gotFingerprint != "fp9" || src.gotCluster != "uid-2" {
		t.Errorf("err=%v fingerprint=%q cluster=%q", res.IsError, src.gotFingerprint, src.gotCluster)
	}
	if res, _ := callFindingTool(t, e, "get_finding_detail", `{}`); !res.IsError {
		t.Error("a detail without a fingerprint must be an error, not a guess")
	}
}

func TestFindingDetail_NotFoundPointsAtTheClusterId(t *testing.T) {
	res, payload := callFindingTool(t, NewExecutor(nil).WithFindings(&stubFindings{}), "get_finding_detail", `{"fingerprint":"nope"}`)
	if !res.IsError || !strings.Contains(payload["error"].(string), "clusterId") {
		t.Errorf("err=%v payload=%v — a miss must say how to ask again", res.IsError, payload)
	}
}

// Every package, fixable first, capped with the total stated — and a package
// with no fix says so instead of leaving the field blank.
func TestFindingDetail_ListsThePackagesTheStoredRowCollapsed(t *testing.T) {
	var pkgs []findings.VulnPackage
	for i := 0; i < detailPackagesReturned+5; i++ {
		p := findings.VulnPackage{Name: fmt.Sprintf("pkg-%02d", i), InstalledVersion: "1.0"}
		if i%2 == 0 {
			p.FixedVersion = "1.1"
		}
		pkgs = append(pkgs, p)
	}
	src := &stubFindings{detail: cveDetail(func(d *findings.Detail) {
		d.Images = []findings.AffectedImage{{Image: "registry/app:v1", Containers: []string{"app"}, Pods: -1, Packages: pkgs}}
	})}
	_, payload := callFindingTool(t, NewExecutor(nil).WithFindings(src), "get_finding_detail", `{"fingerprint":"fp"}`)
	img := payload["images"].([]any)[0].(map[string]any)
	if n := len(img["packages"].([]any)); n != detailPackagesReturned {
		t.Errorf("packages = %d, want the cap %d", n, detailPackagesReturned)
	}
	if img["packagesTotal"].(float64) != float64(len(pkgs)) {
		t.Errorf("packagesTotal = %v — a cut list must state the total", img["packagesTotal"])
	}
	if _, ok := img["pods"]; ok {
		t.Error("pods=-1 means unknown and must not be reported as a number")
	}
	blank := img["packages"].([]any)[1].(map[string]any)
	if blank["fixedIn"] != "no upstream fix yet" {
		t.Errorf("unfixed package reads %v", blank["fixedIn"])
	}
}

// A cluster that cannot be re-read is not a fixed finding.
func TestFindingDetail_NotLiveIsNotFixed(t *testing.T) {
	src := &stubFindings{detail: cveDetail(func(d *findings.Detail) {
		d.Live, d.LiveError = false, "prod is not connected — showing the stored finding only"
	})}
	_, payload := callFindingTool(t, NewExecutor(nil).WithFindings(src), "get_finding_detail", `{"fingerprint":"fp"}`)
	if payload["live"] != false || !strings.Contains(fmt.Sprint(payload["liveNote"]), "NOT a sign") {
		t.Errorf("payload = %v", payload)
	}
	if _, ok := payload["note"]; ok {
		t.Error("a stored-only detail claimed the scanner no longer reports the CVE")
	}
}

// Live, and the scanner no longer lists the CVE: say so, rather than an empty
// images list the model reads as "nothing affected".
func TestFindingDetail_CVEGoneFromTheLiveReportSaysSo(t *testing.T) {
	_, payload := callFindingTool(t, NewExecutor(nil).WithFindings(&stubFindings{detail: cveDetail(nil)}), "get_finding_detail", `{"fingerprint":"fp"}`)
	if !strings.Contains(fmt.Sprint(payload["note"]), "no longer lists") {
		t.Errorf("payload = %v", payload)
	}
}

func TestFindingDetail_ComplianceNamesTheFailingResources(t *testing.T) {
	var failing []findings.FailingResource
	for i := 0; i < detailFailingReturned+10; i++ {
		failing = append(failing, findings.FailingResource{Kind: "Deployment", Namespace: "ns", Name: fmt.Sprintf("w-%02d", i)})
	}
	src := &stubFindings{detail: &findings.Detail{
		Record: finding(func(r *findings.Record) { r.Kind, r.CISControl = integrations.FindingMisconfig, "5.2.6" }),
		Live:   true,
		Compliance: &findings.ComplianceDetail{
			Control: "5.2.6", Description: "Minimize the admission of root containers", Severity: "low",
			FailingResources: failing, FailingTotal: 42,
		},
	}}
	_, payload := callFindingTool(t, NewExecutor(nil).WithFindings(src), "get_finding_detail", `{"fingerprint":"fp"}`)
	c := payload["compliance"].(map[string]any)
	if c["failingTotal"].(float64) != 42 || len(c["failing"].([]any)) != detailFailingReturned {
		t.Errorf("compliance = %v", c)
	}
	if c["benchmarkSeverity"] != "low" || c["requires"] == nil {
		t.Errorf("the control's own text and rating must ship: %v", c)
	}
}

// ─── both ────────────────────────────────────────────────────────────────

func TestFindingTools_UnavailableIsNotACleanCluster(t *testing.T) {
	for _, tool := range []string{"get_finding_workloads", "get_finding_detail"} {
		res, payload := callFindingTool(t, NewExecutor(nil), tool, `{"fingerprint":"fp"}`)
		if !res.IsError || !strings.Contains(fmt.Sprint(payload["error"]), "NOT a clean cluster") {
			t.Errorf("%s: err=%v payload=%v", tool, res.IsError, payload)
		}
	}
}

func TestFindingTools_ArePublishedReadOnlyWithTheirSchema(t *testing.T) {
	want := map[string][]string{
		"get_finding_workloads": {"group", "severity", "kind", "status", "cluster", "limit"},
		"get_finding_detail":    {"fingerprint", "clusterId"},
	}
	for _, d := range GovernedToolDefinitions(false, false) {
		props, ok := want[d.Name]
		if !ok {
			continue
		}
		delete(want, d.Name)
		schema, _ := d.InputSchema["properties"].(map[string]interface{})
		for _, p := range props {
			if _, ok := schema[p]; !ok {
				t.Errorf("%s: %q is read by the executor but not declared", d.Name, p)
			}
		}
	}
	for name := range want {
		t.Errorf("%s is missing from the read-only catalogue MCP publishes", name)
	}
}

// Rows of an org-wide get_findings carry their cluster, or the detail cannot
// be asked for: a fingerprint is not unique across clusters.
func TestFindings_OrgScopeRowsCarryTheirCluster(t *testing.T) {
	src := &stubFindings{recs: []findings.Record{finding(nil)}}
	_, payload := callFindings(t, NewExecutor(nil).WithFindings(src), `{"cluster":"all","severity":"high"}`)
	row := payload["findings"].([]any)[0].(map[string]any)
	if row["clusterId"] != "c1" {
		t.Errorf("row = %v", row)
	}
}

// The planner asks with the image as the pod spec names it; Trivy stored it
// fully qualified. An exact match reported a CVE-carrying image as clean.
func TestFindings_ImageFacetMatchesTheShortForm(t *testing.T) {
	stored := "index.docker.io/library/nginx:1.27-alpine"
	for _, q := range []string{"nginx:1.27-alpine", "docker.io/library/nginx:1.27-alpine", stored, "nginx", "NGINX:1.27-alpine"} {
		if !sameImage(stored, q) {
			t.Errorf("%q does not match %q", q, stored)
		}
	}
	for _, q := range []string{"nginx:1.28-alpine", "nginx-unprivileged:1.27-alpine", "bitnami/nginx:1.27-alpine"} {
		if sameImage(stored, q) {
			t.Errorf("%q wrongly matches %q", q, stored)
		}
	}
	// A registry port is not a tag.
	if !sameImage("registry.local:5000/team/app:v1", "registry.local:5000/team/app") {
		t.Error("untagged query with a registry port did not match")
	}
	if sameImage("registry.local:5000/team/app:v1", "registry.local:5000/team/app:v2") {
		t.Error("different tags matched")
	}

	src := &stubFindings{recs: []findings.Record{finding(func(r *findings.Record) {
		r.Image, r.Severity = stored, "critical"
	})}}
	_, payload := callFindings(t, NewExecutor(nil).WithFindings(src), `{"image":"nginx:1.27-alpine","severity":"critical"}`)
	if payload["total"].(float64) != 1 {
		t.Errorf("total = %v — the short form must find the stored critical", payload["total"])
	}
}
