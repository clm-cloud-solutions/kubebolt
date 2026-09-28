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

func TestRightSizing_ReadableUnitsAndPreliminary(t *testing.T) {
	days := 1.2
	res := rightsizing.Result{
		Recs: []rightsizing.Recommendation{{
			Namespace: "shop", Kind: "Deployment", Name: "api", Severity: rightsizing.Warning, Reason: "CPU over-provisioned",
			CPU: rightsizing.Finding{Request: 2000, P95: 150, State: rightsizing.Over, Suggest: 180},
			Mem: rightsizing.Finding{Request: 512 * 1024 * 1024, P95: 100 * 1024 * 1024, State: rightsizing.OK},
		}},
		Totals:      rightsizing.Totals{Count: 1, ReclaimCPUMilli: 1820},
		WindowDays:  &days,
		Preliminary: true,
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
	if payload["preliminary"] != true || payload["reclaimable"].(map[string]any)["cpu"] != "1820m" {
		t.Errorf("payload = %v", payload)
	}
}
