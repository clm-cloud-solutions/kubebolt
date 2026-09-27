package copilot

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/rightsizing"
)

// get_coverage and query_metrics — the two tools a customer needs in the first
// hour, before anything else makes sense.
//
// get_coverage answers "what can KubeBolt see of this cluster right now". Just
// after a cluster is connected, pieces are missing — the agent has not
// registered, metrics have not arrived, Hubble or Falco are off, RBAC is
// partial — and every other tool then answers with less. Without this, a thin
// answer reads as a healthy cluster; through the MCP door, where the client's
// own model knows nothing about KubeBolt, there was no way to tell at all.
//
// query_metrics is the escape hatch get_workload_metrics is not: any PromQL,
// confined to the caller's org and the request's cluster by the same pipeline
// GET /metrics/query uses (caller-supplied tenant_id / cluster_id stripped,
// the server's own injected). It covers what the curated lanes do not —
// restarts, PVC fill, HTTP, latency, KSM state.

// CoverageSource is what the executor cannot read by itself: the metric
// probes (they run against the metrics store with the org and cluster pinned)
// and the live agent registry. Both are org-bound by the implementation.
type CoverageSource interface {
	MetricSources(ctx context.Context) []MetricSourceStatus
	// Agents are the agents of clusterUID connected right now, for the caller's
	// org only.
	Agents(ctx context.Context, clusterUID string) []AgentStatus
}

// MetricSourceStatus is one probe of GET /coverage.
type MetricSourceStatus struct {
	Name   string
	Probe  string
	Active bool
}

// AgentStatus is one live agent stream.
type AgentStatus struct {
	NodeName    string
	AuthMode    string
	ConnectedAt time.Time
}

// MetricsQuerySource runs a PromQL expression confined to the caller's org and
// the request's cluster. A range query when start < end, an instant one at end
// otherwise.
type MetricsQuerySource interface {
	Query(ctx context.Context, promQL string, start, end time.Time, step time.Duration) ([]MetricSeries, error)
}

// MetricSeries is one returned series, without the scope labels.
type MetricSeries struct {
	Labels map[string]string
	Points []MetricSample
}

// MetricSample is one (time, value) point.
type MetricSample struct {
	T time.Time
	V float64
}

// WithCoverage wires get_coverage's metric probes and agent registry.
// Chainable; nil means the tool reports those parts as unknown.
func (e *Executor) WithCoverage(src CoverageSource) *Executor {
	e.coverage = src
	return e
}

// WithMetricsQuery wires query_metrics. Chainable; nil means the install has
// no metrics store to query and the tool says so.
func (e *Executor) WithMetricsQuery(src MetricsQuerySource) *Executor {
	e.metricsQuery = src
	return e
}

// ---- get_coverage ----

func (e *Executor) coverageReport(ctx context.Context, conn *cluster.Connector) map[string]interface{} {
	out := map[string]interface{}{}
	gaps := []string{}

	// The cluster and how KubeBolt reaches it.
	clusterInfo := map[string]interface{}{"connected": conn != nil}
	uid := e.currentClusterID(ctx)
	if e.manager != nil {
		ctxName := e.manager.ActiveContextFor(ctx)
		if ctxName == "" {
			out["cluster"] = map[string]interface{}{"connected": false}
			out["gaps"] = []string{"no cluster is selected or connected for this caller — connect one (agent or kubeconfig) before anything else can be read"}
			return out
		}
		if n := e.manager.DisplayNameForCluster(ctx, uid); n != "" {
			clusterInfo["name"] = n
		} else {
			clusterInfo["name"] = ctxName
		}
		switch {
		case e.manager.MetricsOnlyClusterID(ctx) != "":
			clusterInfo["mode"] = "metrics-only"
			gaps = append(gaps, "metrics-only cluster: KubeBolt receives metrics but has no API access through the agent, so resources, logs, YAML, events and insights are not readable — install the agent with rbac.mode=reader or operator")
		case strings.HasPrefix(ctxName, cluster.AgentProxyContextPrefix):
			clusterInfo["mode"] = "agent"
		default:
			clusterInfo["mode"] = "direct (kubeconfig / in-cluster)"
		}
		if err := e.manager.ConnErrorFor(ctx); err != nil && conn == nil {
			clusterInfo["connectionError"] = cluster.RedactText(err.Error())
		}
	}
	if conn == nil && clusterInfo["mode"] != "metrics-only" {
		gaps = append(gaps, "the cluster is not reachable right now: live reads (resources, logs, YAML, events, insights) will fail until it reconnects; stored history (insight episodes, findings, runtime events) still answers")
	}
	out["cluster"] = clusterInfo

	// Agents.
	if e.coverage != nil {
		agents := e.coverage.Agents(ctx, uid)
		rows := make([]map[string]interface{}, 0, len(agents))
		sort.Slice(agents, func(i, j int) bool { return agents[i].NodeName < agents[j].NodeName })
		for _, a := range agents {
			row := map[string]interface{}{"node": a.NodeName, "connectedSince": a.ConnectedAt.UTC().Format(time.RFC3339)}
			if a.AuthMode != "" {
				row["auth"] = a.AuthMode
			}
			rows = append(rows, row)
		}
		out["agents"] = map[string]interface{}{"connected": len(agents), "list": capRows(rows, 20)}
		if len(agents) == 0 && clusterInfo["mode"] != "direct (kubeconfig / in-cluster)" {
			gaps = append(gaps, "no KubeBolt agent is connected for this cluster: no fresh metrics, and on an agent cluster no API access either")
		}

		// Metric sources.
		srcs := e.coverage.MetricSources(ctx)
		ms := make([]map[string]interface{}, 0, len(srcs))
		active := map[string]bool{}
		for _, s := range srcs {
			state := "inactive"
			if s.Active {
				state = "active"
				active[s.Name] = true
			}
			ms = append(ms, map[string]interface{}{"source": s.Name, "status": state, "probe": s.Probe})
		}
		out["metricSources"] = ms
		if !active["kubebolt-agent"] {
			gaps = append(gaps, "no agent metrics in the last 5 minutes: CPU / memory / network charts and get_workload_metrics will be empty or stale")
		}
		if !active["hubble"] {
			gaps = append(gaps, "no Hubble flows: who-talks-to-whom, HTTP error rates and network drops are not available (Hubble is off by default in the agent)")
		}
		if !active["kube-state-metrics"] {
			gaps = append(gaps, "no kube-state-metrics: object-state series (kube_*) are missing for query_metrics")
		}
	} else {
		out["agents"] = "unknown on this install"
		out["metricSources"] = "unknown on this install"
	}

	// Permissions.
	if conn != nil {
		readable, denied, absent := 0, []string{}, []string{}
		for key, p := range conn.Permissions() {
			switch {
			case p == nil:
			case p.CanList:
				readable++
			case p.Absent:
				absent = append(absent, key)
			default:
				denied = append(denied, key)
			}
		}
		sort.Strings(denied)
		sort.Strings(absent)
		perms := map[string]interface{}{"readableTypes": readable}
		if len(denied) > 0 {
			perms["denied"] = denied
			gaps = append(gaps, fmt.Sprintf("RBAC denies %d resource types (%s): those views and tools return 'forbidden'", len(denied), strings.Join(firstN(denied, 6), ", ")))
		}
		if len(absent) > 0 {
			perms["notInstalled"] = absent
		}
		out["permissions"] = perms
	}

	// Security feeds.
	sec := map[string]interface{}{}
	if e.findings != nil {
		recs, err := e.findings.List(ctx, findingsQueryFor(uid), false)
		if err == nil {
			bySource := map[string]int{}
			for _, r := range recs {
				bySource[r.Source]++
			}
			sec["activeFindingsBySource"] = bySource
			if len(bySource) == 0 {
				gaps = append(gaps, "no security findings reported: either no scanner (Trivy Operator, Kyverno) is installed or it has not finished a scan — this is not a clean bill of health")
			}
		}
	}
	if e.runtimeEvents != nil {
		evs, err := e.runtimeEvents.List(ctx, runtimeQueryFor(uid, 7*24*time.Hour), false)
		if err == nil {
			sec["runtimeEventsLast7d"] = len(evs)
		}
	}
	if len(sec) > 0 {
		out["security"] = sec
	}

	out["gaps"] = gaps
	if len(gaps) == 0 {
		out["summary"] = "everything KubeBolt can read for this cluster is flowing"
	}
	return out
}

func capRows(rows []map[string]interface{}, n int) []map[string]interface{} {
	if len(rows) > n {
		return rows[:n]
	}
	return rows
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(append([]string{}, s[:n]...), fmt.Sprintf("+%d more", len(s)-n))
	}
	return s
}

// ---- query_metrics ----

// Bounds for query_metrics. The window is what one answer needs; the series
// and points caps keep a result under the tool budget — a PromQL expression
// without aggregation can return thousands of series.
const (
	maxMetricsQueryLen     = 2000
	maxMetricsSeries       = 20
	maxMetricsPointsPerRow = 30
	metricsRangeSteps      = 60
)

var metricsQueryRanges = map[string]time.Duration{
	"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour,
	"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
}

func (e *Executor) runQueryMetrics(ctx context.Context, args map[string]interface{}) (map[string]interface{}, error) {
	promQL := strings.TrimSpace(stringArg(args, "query"))
	if promQL == "" {
		return nil, fmt.Errorf("query is required (a PromQL expression)")
	}
	if len(promQL) > maxMetricsQueryLen {
		return nil, fmt.Errorf("query is %d characters; the limit is %d", len(promQL), maxMetricsQueryLen)
	}
	end := time.Now().UTC()
	start := end
	var step time.Duration
	rng := stringArg(args, "range")
	if rng != "" {
		d, ok := metricsQueryRanges[rng]
		if !ok {
			return nil, fmt.Errorf("range %q is not one of 15m, 1h, 6h, 24h, 7d (omit it for an instant query)", rng)
		}
		if limit := e.metricsRetentionLimit(ctx); limit > 0 && d > limit {
			d = limit
		}
		start = end.Add(-d)
		step = d / metricsRangeSteps
		if step < 30*time.Second {
			step = 30 * time.Second
		}
	}
	series, err := e.metricsQuery.Query(ctx, promQL, start, end, step)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{
		"query":       promQL,
		"seriesTotal": len(series),
		"scope":       "your organization and the current cluster; tenant_id / cluster_id matchers in the query are replaced by the server",
	}
	if rng != "" {
		out["range"] = rng
		out["step"] = step.String()
	} else {
		out["at"] = end.Format(time.RFC3339)
	}
	if len(series) > maxMetricsSeries {
		out["truncated"] = true
		out["returned"] = maxMetricsSeries
		out["hint"] = "aggregate (sum by / topk) to get fewer series"
		series = series[:maxMetricsSeries]
	}
	rows := make([]map[string]interface{}, 0, len(series))
	for _, s := range series {
		row := map[string]interface{}{"labels": s.Labels}
		if len(s.Points) == 1 {
			row["value"] = roundSig(s.Points[0].V)
		} else if len(s.Points) > 1 {
			min, max, sum := math.Inf(1), math.Inf(-1), 0.0
			for _, p := range s.Points {
				min, max, sum = math.Min(min, p.V), math.Max(max, p.V), sum+p.V
			}
			row["summary"] = map[string]interface{}{
				"min": roundSig(min), "max": roundSig(max),
				"avg": roundSig(sum / float64(len(s.Points))), "last": roundSig(s.Points[len(s.Points)-1].V),
			}
			row["points"] = downsampleSamples(s.Points, maxMetricsPointsPerRow)
		}
		rows = append(rows, row)
	}
	out["series"] = rows
	if len(rows) == 0 {
		out["note"] = "no series matched in this cluster. An empty result is NOT a zero value: the metric may not be shipped here — get_coverage lists the active sources."
	}
	return out, nil
}

// metricsRetentionLimit is the org's metrics retention, 0 for no cap.
func (e *Executor) metricsRetentionLimit(ctx context.Context) time.Duration {
	if e.metricsRetention == nil {
		return 0
	}
	return e.metricsRetention(ctx)
}

func downsampleSamples(pts []MetricSample, n int) [][2]interface{} {
	stride := 1
	if len(pts) > n {
		stride = (len(pts) + n - 1) / n
	}
	out := make([][2]interface{}, 0, n+1)
	for i := 0; i < len(pts); i += stride {
		out = append(out, [2]interface{}{pts[i].T.Format(time.RFC3339), roundSig(pts[i].V)})
	}
	if last := pts[len(pts)-1]; (len(pts)-1)%stride != 0 {
		out = append(out, [2]interface{}{last.T.Format(time.RFC3339), roundSig(last.V)})
	}
	return out
}

// roundSig keeps 4 significant digits: enough to reason with, and a series of
// 17-digit floats is most of a tool budget.
func roundSig(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return v
	}
	mag := math.Pow(10, 3-math.Floor(math.Log10(math.Abs(v))))
	return math.Round(v*mag) / mag
}

// ---- get_right_sizing ----

// RightSizingSource is the right-sizing engine the Capacity and Cost screens
// read through GET /right-sizing — one computation, so the chat and the
// screen recommend the same thing.
type RightSizingSource interface {
	RightSizing(ctx context.Context) (rightsizing.Result, error)
}

// WithRightSizing wires get_right_sizing. Chainable; nil means the install
// cannot answer.
func (e *Executor) WithRightSizing(src RightSizingSource) *Executor {
	e.rightSizing = src
	return e
}

const (
	defaultRightSizingRows = 15
	maxRightSizingRows     = 50
)

func summarizeRightSizing(res rightsizing.Result, namespace, severity string, limit int) map[string]interface{} {
	rows := make([]rightsizing.Recommendation, 0, len(res.Recs))
	for _, r := range res.Recs {
		if namespace != "" && r.Namespace != namespace {
			continue
		}
		if severity != "" && string(r.Severity) != severity {
			continue
		}
		rows = append(rows, r)
	}
	out := map[string]interface{}{
		"total": len(rows),
		"reclaimable": map[string]string{
			"cpu":    formatMilli(res.Totals.ReclaimCPUMilli),
			"memory": formatBytes(res.Totals.ReclaimMemBytes),
		},
		"rules": "near-limit: P95 >= 80% of the limit (critical, raise the limit); over-provisioned: P95 < 50% of the request and above 50m / 100Mi (warning, lower the request); no-specs: usage with no request or limit (info). Suggestions add headroom over the 7-day P95: x1.2 for a request, x1.5 for a limit.",
	}
	if res.WindowDays != nil {
		out["historyDays"] = math.Round(*res.WindowDays*10) / 10
	}
	if res.Preliminary {
		out["preliminary"] = true
		out["preliminaryNote"] = "less than 2 days of usage history: the P95 has not seen two daily peaks, so savings are optimistic — say so, and do not recommend acting on them yet"
	}
	if len(rows) > limit {
		out["truncated"] = true
		out["returned"] = limit
		rows = rows[:limit]
	}
	list := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		row := map[string]interface{}{
			"workload": r.Kind + " " + r.Namespace + "/" + r.Name,
			"severity": string(r.Severity),
			"reason":   r.Reason,
		}
		if c := findingRow(r.CPU, formatMilli); c != nil {
			row["cpu"] = c
		}
		if m := findingRow(r.Mem, formatBytes); m != nil {
			row["memory"] = m
		}
		list = append(list, row)
	}
	out["recommendations"] = list
	if len(res.Recs) == 0 {
		out["note"] = "no workload breaks a rule. If the cluster was just connected, get_coverage says whether usage metrics are flowing — without them every P95 is 0 and nothing can be flagged."
	}
	return out
}

func findingRow(f rightsizing.Finding, format func(int64) string) map[string]interface{} {
	if f.State == rightsizing.OK {
		return nil
	}
	row := map[string]interface{}{
		"state":   string(f.State),
		"p95":     format(f.P95),
		"request": format(f.Request),
		"limit":   format(f.Limit),
	}
	switch f.State {
	case rightsizing.Over:
		row["suggestedRequest"] = format(f.Suggest)
	case rightsizing.NearLimit:
		row["suggestedLimit"] = format(f.Suggest)
	}
	return row
}

func formatMilli(m int64) string {
	if m == 0 {
		return "0"
	}
	if m%1000 == 0 {
		return fmt.Sprintf("%d", m/1000)
	}
	return fmt.Sprintf("%dm", m)
}

func formatBytes(b int64) string {
	const mi, gi = 1024 * 1024, 1024 * 1024 * 1024
	switch {
	case b == 0:
		return "0"
	case b >= gi && b%(gi/10) == 0:
		return strconvG(float64(b)/gi) + "Gi"
	default:
		return fmt.Sprintf("%dMi", int64(math.Round(float64(b)/mi)))
	}
}

func strconvG(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", f), "0"), ".")
}
