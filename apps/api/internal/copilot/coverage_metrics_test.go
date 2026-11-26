package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/rightsizing"
)

type stubCoverage struct {
	sources []MetricSourceStatus
	agents  []AgentStatus
}

func (s stubCoverage) MetricSources(context.Context) []MetricSourceStatus { return s.sources }
func (s stubCoverage) Agents(context.Context, string) []AgentStatus        { return s.agents }

func runTool(t *testing.T, e *Executor, name, input string) (ToolResult, map[string]any) {
	t.Helper()
	res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: name, Input: json.RawMessage(input)})
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, res.Content)
	}
	return res, payload
}

// A freshly connected cluster: agent metrics on, Hubble off, no scanner. The
// report names each missing piece as a gap, so a thin answer elsewhere is not
// read as a healthy or clean cluster.
func TestCoverage_NamesTheGapsOfAFreshCluster(t *testing.T) {
	cov := stubCoverage{
		sources: []MetricSourceStatus{
			{Name: "kubebolt-agent", Active: true}, {Name: "hubble"}, {Name: "kube-state-metrics"},
		},
		agents: []AgentStatus{{NodeName: "worker-1", ConnectedAt: time.Now()}},
	}
	res, payload := runTool(t, NewExecutor(nil).WithCoverage(cov).WithFindings(&stubFindings{}), "get_coverage", `{}`)
	if res.IsError {
		t.Fatalf("error: %s", res.Content)
	}
	gaps := fmt.Sprint(payload["gaps"])
	for _, want := range []string{"Hubble", "kube-state-metrics", "not a clean bill of health"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("gap about %q missing: %s", want, gaps)
		}
	}
	if strings.Contains(gaps, "no agent metrics") {
		t.Errorf("agent metrics are active but reported as a gap: %s", gaps)
	}
	if payload["agents"].(map[string]any)["connected"].(float64) != 1 {
		t.Errorf("agents = %v", payload["agents"])
	}
}

type stubMetrics struct {
	gotQuery      string
	gotStart, end time.Time
	gotStep       time.Duration
	series        []MetricSeries
	err           error
}

func (s *stubMetrics) Query(_ context.Context, q string, start, end time.Time, step time.Duration) ([]MetricSeries, error) {
	s.gotQuery, s.gotStart, s.end, s.gotStep = q, start, end, step
	return s.series, s.err
}

func TestQueryMetrics_InstantRangeAndCaps(t *testing.T) {
	var many []MetricSeries
	for i := 0; i < 50; i++ {
		var pts []MetricSample
		for j := 0; j < 120; j++ {
			pts = append(pts, MetricSample{T: time.Unix(int64(j*60), 0), V: float64(i*1000 + j)})
		}
		many = append(many, MetricSeries{Labels: map[string]string{"pod": fmt.Sprintf("p%d", i)}, Points: pts})
	}
	src := &stubMetrics{series: many}
	e := NewExecutor(nil).WithMetricsQuery(src)

	_, payload := runTool(t, e, "query_metrics", `{"query":"kube_pod_container_status_restarts_total","range":"6h"}`)
	if !src.gotStart.Before(src.end) || src.gotStep < 30*time.Second {
		t.Errorf("range 6h ran as start=%v end=%v step=%v", src.gotStart, src.end, src.gotStep)
	}
	if payload["seriesTotal"].(float64) != 50 || payload["truncated"] != true || len(payload["series"].([]any)) != maxMetricsSeries {
		t.Errorf("seriesTotal=%v truncated=%v rows=%d", payload["seriesTotal"], payload["truncated"], len(payload["series"].([]any)))
	}
	row := payload["series"].([]any)[0].(map[string]any)
	if n := len(row["points"].([]any)); n > maxMetricsPointsPerRow+1 {
		t.Errorf("points per series = %d, over the cap", n)
	}

	src.series = []MetricSeries{{Labels: map[string]string{}, Points: []MetricSample{{T: time.Now(), V: 3}}}}
	_, payload = runTool(t, e, "query_metrics", `{"query":"sum(up)"}`)
	if !src.gotStart.Equal(src.end) || payload["series"].([]any)[0].(map[string]any)["value"].(float64) != 3 {
		t.Errorf("instant query: start=%v end=%v payload=%v", src.gotStart, src.end, payload)
	}
}

func TestQueryMetrics_RefusesWhatItCannotAnswerHonestly(t *testing.T) {
	e := NewExecutor(nil).WithMetricsQuery(&stubMetrics{})
	for _, in := range []string{`{}`, `{"query":"up","range":"3d"}`, fmt.Sprintf(`{"query":%q}`, strings.Repeat("a", maxMetricsQueryLen+1))} {
		if res, _ := runTool(t, e, "query_metrics", in); !res.IsError {
			t.Errorf("%s: accepted", in)
		}
	}
	_, payload := runTool(t, e, "query_metrics", `{"query":"up"}`)
	if !strings.Contains(fmt.Sprint(payload["note"]), "NOT a zero") {
		t.Errorf("an empty result must say it is not a zero: %v", payload)
	}
	if res, payload := runTool(t, NewExecutor(nil), "query_metrics", `{"query":"up"}`); !res.IsError || !strings.Contains(fmt.Sprint(payload["error"]), "NOT a zero") {
		t.Errorf("no store: %v", payload)
	}
}

// While right-sizing is collecting, the engine returns no recommendations. The
// payload must then carry only the collecting note — "no workload breaks a
// rule" next to it reads as "nothing is oversized".
func TestRightSizing_CollectingSaysOnlyThat(t *testing.T) {
	days := 0.4
	var payload map[string]any
	b, _ := json.Marshal(summarizeRightSizing(rightsizing.Result{
		Recs: []rightsizing.Recommendation{}, WindowDays: &days, InsufficientHistory: true,
	}, "", "", defaultRightSizingRows))
	_ = json.Unmarshal(b, &payload)
	if _, ok := payload["note"]; ok {
		t.Errorf("a collecting result must not say no workload breaks a rule: %v", payload["note"])
	}
	if payload["insufficientHistoryNote"] == nil {
		t.Error("a collecting result must carry insufficientHistoryNote")
	}
}

func TestRightSizing_ReadableUnitsAndInsufficientHistory(t *testing.T) {
	days := 1.2
	res := rightsizing.Result{
		Recs: []rightsizing.Recommendation{{
			Namespace: "shop", Kind: "Deployment", Name: "api", Severity: rightsizing.Warning, Reason: "CPU over-provisioned",
			CPU: rightsizing.Finding{Request: 2000, P95: 150, State: rightsizing.Over, Suggest: 180},
			Mem: rightsizing.Finding{Request: 512 * 1024 * 1024, P95: 100 * 1024 * 1024, State: rightsizing.OK},
		}},
		Totals:              rightsizing.Totals{Count: 1, ReclaimCPUMilli: 1820},
		WindowDays:          &days,
		InsufficientHistory: true,
	}
	// The tool needs a live connector (it reads the workloads' specs), so the
	// shaping is exercised directly; the executor gate is the generic one.
	var payload map[string]any
	b, _ := json.Marshal(summarizeRightSizing(res, "", "", defaultRightSizingRows))
	_ = json.Unmarshal(b, &payload)
	row := payload["recommendations"].([]any)[0].(map[string]any)
	cpu := row["cpu"].(map[string]any)
	if cpu["request"] != "2" || cpu["suggestedRequest"] != "180m" || cpu["p95"] != "150m" {
		t.Errorf("cpu row = %v", cpu)
	}
	if _, ok := row["memory"]; ok {
		t.Error("a resource in state ok must not add a row")
	}
	if payload["insufficientHistory"] != true || payload["reclaimable"].(map[string]any)["cpu"] != "1820m" {
		t.Errorf("payload = %v", payload)
	}
}

// Finding #102: a sum over a family OpenCost republishes counts each object
// twice where the cluster also sends kube-state-metrics. The caution rides the
// result, so it costs nothing on turns that never read these families.
func TestQueryMetrics_CautionsWhenARepublishedFamilyIsReadTwice(t *testing.T) {
	src := &stubMetrics{series: []MetricSeries{{Labels: map[string]string{}, Points: []MetricSample{{T: time.Now(), V: 0.4}}}}}
	e := NewExecutor(nil).WithMetricsQuery(src)

	_, payload := runTool(t, e, "query_metrics", `{"query":"sum(kube_pod_container_resource_requests{namespace=\"kube-system\",resource=\"cpu\"})"}`)
	note := fmt.Sprint(payload["note"])
	if !strings.Contains(note, `source="opencost"`) || !strings.Contains(note, "max by (namespace, pod, container)") {
		t.Errorf("a plain sum of requests carries no caution: %q", note)
	}
	_, payload = runTool(t, e, "query_metrics", `{"query":"sum(kube_node_status_allocatable{resource=\"memory\"})"}`)
	if !strings.Contains(fmt.Sprint(payload["note"]), "max by (node)") {
		t.Errorf("allocatable: %v", payload["note"])
	}

	for _, q := range []string{
		`sum(max by (namespace, pod, container) (kube_pod_container_resource_requests{resource=\"cpu\"}))`,
		`sum by (source, job) (kube_pod_container_resource_requests)`,
		`sum(kube_pod_container_status_restarts_total)`,
	} {
		_, payload = runTool(t, e, "query_metrics", `{"query":"`+q+`"}`)
		if payload["note"] != nil {
			t.Errorf("%s: cautioned although it reads each object once or is not republished: %v", q, payload["note"])
		}
	}
}

// A filtered call's reclaimable total is the matched rows', not the
// cluster's: one namespace's rows next to the whole cluster's 560m read as
// that namespace's (Kobi Plans live test, 25-nov).
func TestRightSizing_ReclaimableFollowsTheFilter(t *testing.T) {
	days := 3.1
	over := func(ns, name string, request, suggest int64) rightsizing.Recommendation {
		return rightsizing.Recommendation{Namespace: ns, Kind: "Deployment", Name: name, Severity: rightsizing.Warning,
			CPU: rightsizing.Finding{Request: request, P95: suggest - 2, State: rightsizing.Over, Suggest: suggest}}
	}
	res := rightsizing.Result{
		Recs:       []rightsizing.Recommendation{over("kyverno", "a", 100, 10), over("kyverno", "b", 100, 30), over("kube-system", "coredns", 200, 60)},
		Totals:     rightsizing.Totals{Count: 3, ReclaimCPUMilli: 300},
		WindowDays: &days,
	}
	reclaimable := func(namespace string) any {
		var payload map[string]any
		b, _ := json.Marshal(summarizeRightSizing(res, namespace, "", defaultRightSizingRows))
		_ = json.Unmarshal(b, &payload)
		return payload["reclaimable"].(map[string]any)["cpu"]
	}
	if got := reclaimable("kyverno"); got != "160m" {
		t.Errorf("kyverno reclaimable = %v, want 160m", got)
	}
	if got := reclaimable(""); got != "300m" {
		t.Errorf("cluster reclaimable = %v, want 300m", got)
	}
}

// A workload the provider reconciles is named as such and is not reclaimable
// (P17, plans-12): nobody but the provider can resize it.
func TestRightSizing_ProviderManagedIsNotReclaimable(t *testing.T) {
	over := func(ns, name string, request, suggest int64, managedBy string) rightsizing.Recommendation {
		return rightsizing.Recommendation{Namespace: ns, Kind: "DaemonSet", Name: name, Severity: rightsizing.Warning, ManagedBy: managedBy,
			CPU: rightsizing.Finding{Request: request, P95: suggest - 2, State: rightsizing.Over, Suggest: suggest}}
	}
	res := rightsizing.Result{Recs: []rightsizing.Recommendation{over("kube-system", "azure-cns", 400, 30, "aks"), over("shop", "web", 200, 60, "")}}
	var payload map[string]any
	b, _ := json.Marshal(summarizeRightSizing(res, "", "", defaultRightSizingRows))
	_ = json.Unmarshal(b, &payload)
	if got := payload["reclaimable"].(map[string]any)["cpu"]; got != "140m" {
		t.Fatalf("reclaimable = %v, want 140m (azure-cns left out)", got)
	}
	if payload["managedByNote"] == nil {
		t.Fatal("no note says what managedBy means")
	}
	rows := payload["recommendations"].([]any)
	if rows[0].(map[string]any)["managedBy"] != "AKS" || rows[1].(map[string]any)["managedBy"] != nil {
		t.Fatalf("rows = %v", rows)
	}
}
