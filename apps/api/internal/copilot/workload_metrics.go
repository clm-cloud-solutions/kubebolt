package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Workload metrics tool — spec: internal/copilot-execution-capacity/07-workload-metrics-tool.md
//
// This file contains the pure pieces (range table, PromQL builders, summarize,
// downsample) so they can be unit-tested without a live VM. The executor case
// and the HTTP call live in executor.go.

// MetricKind enumerates the metric IDs the LLM can request.
type MetricKind string

const (
	MetricCPU        MetricKind = "cpu"
	MetricMemory     MetricKind = "memory"
	MetricNetworkRX  MetricKind = "network_rx"
	MetricNetworkTX  MetricKind = "network_tx"
)

// metricsRangeSpec ties a UI-friendly range string to the VM query parameters.
// 12 trend points per response keeps the token cost ~constant across windows.
type metricsRangeSpec struct {
	Step       time.Duration // VM step for range queries
	RateWindow time.Duration // window inside rate(...[X])
	Duration   time.Duration // total span of the range
}

// metricsRangeTable maps the input enum to VM parameters. Keep in lockstep
// with the spec's "Range-to-step mapping" table.
var metricsRangeTable = map[string]metricsRangeSpec{
	"5m":  {Step: 25 * time.Second, RateWindow: 1 * time.Minute, Duration: 5 * time.Minute},
	"15m": {Step: 75 * time.Second, RateWindow: 1 * time.Minute, Duration: 15 * time.Minute},
	"1h":  {Step: 5 * time.Minute, RateWindow: 5 * time.Minute, Duration: 1 * time.Hour},
	"6h":  {Step: 30 * time.Minute, RateWindow: 5 * time.Minute, Duration: 6 * time.Hour},
	"24h": {Step: 2 * time.Hour, RateWindow: 15 * time.Minute, Duration: 24 * time.Hour},
}

// trendTargetPoints is the downsample target. Matches the spec's "12 points
// per response" budget and is chosen to fit comfortably under the 1500-token
// per-tool-call cap.
const trendTargetPoints = 12

// maxNetworkPods is the hard cap from §Server behavior. Workloads with more
// pods refuse network queries — Kobi is steered to call kind=Pod for the
// specific pods of interest.
const maxNetworkPods = 50

// parseMetricsRange resolves the range enum to its VM parameters and the
// absolute start/end timestamps anchored on `now`. Returns an error when the
// caller passes a value outside the enum so the validator surfaces a clean
// 400 instead of letting it through to VM as an empty step.
func parseMetricsRange(rangeStr string, now time.Time) (start, end time.Time, spec metricsRangeSpec, err error) {
	if rangeStr == "" {
		rangeStr = "15m"
	}
	spec, ok := metricsRangeTable[rangeStr]
	if !ok {
		valid := make([]string, 0, len(metricsRangeTable))
		for k := range metricsRangeTable {
			valid = append(valid, k)
		}
		sort.Strings(valid)
		return time.Time{}, time.Time{}, metricsRangeSpec{}, fmt.Errorf("invalid range %q (valid: %s)", rangeStr, strings.Join(valid, ", "))
	}
	end = now
	start = end.Add(-spec.Duration)
	return start, end, spec, nil
}

// promBuilder constructs the PromQL queries for a single workload metrics
// call. Centralised here so the tests can golden-match the exact strings and
// catch accidental selector regressions.
type promBuilder struct {
	kind         string // "Pod" | "Deployment" | "StatefulSet" | "DaemonSet" | "Job" | "CronJob"
	namespace    string
	name         string
	clusterUID   string   // injected as cluster_id="..." into every selector
	pods         []string // resolved pod names — required for Pod kind AND for network metrics
	perContainer bool
	rateWindow   time.Duration
}

// buildCPU returns the PromQL for the CPU range query. Workload kinds use the
// workload_kind/workload_name labels emitted by the agent (verified on yagan
// 2026-05-21). Pod kind drops to pod="<name>".
func (b *promBuilder) buildCPU() string {
	groupBy := "workload_kind, workload_name"
	if b.perContainer {
		groupBy += ", container"
	}
	if strings.EqualFold(b.kind, "Pod") {
		groupBy = "pod"
		if b.perContainer {
			groupBy = "pod, container"
		}
		return fmt.Sprintf(
			`sum by (%s) (rate(container_cpu_usage_seconds_total{cluster_id=%q,namespace=%q,pod=%q}[%s]))`,
			groupBy, b.clusterUID, b.namespace, b.name, promDuration(b.rateWindow),
		)
	}
	return fmt.Sprintf(
		`sum by (%s) (rate(container_cpu_usage_seconds_total{cluster_id=%q,namespace=%q,workload_kind=%q,workload_name=%q}[%s]))`,
		groupBy, b.clusterUID, b.namespace, b.kind, b.name, promDuration(b.rateWindow),
	)
}

// buildMemory returns the PromQL for working-set memory. Memory is a gauge —
// no rate() wrap. Same workload_kind/workload_name pattern as CPU.
func (b *promBuilder) buildMemory() string {
	groupBy := "workload_kind, workload_name"
	if b.perContainer {
		groupBy += ", container"
	}
	if strings.EqualFold(b.kind, "Pod") {
		groupBy = "pod"
		if b.perContainer {
			groupBy = "pod, container"
		}
		return fmt.Sprintf(
			`sum by (%s) (container_memory_working_set_bytes{cluster_id=%q,namespace=%q,pod=%q})`,
			groupBy, b.clusterUID, b.namespace, b.name,
		)
	}
	return fmt.Sprintf(
		`sum by (%s) (container_memory_working_set_bytes{cluster_id=%q,namespace=%q,workload_kind=%q,workload_name=%q})`,
		groupBy, b.clusterUID, b.namespace, b.kind, b.name,
	)
}

// buildNetwork returns the PromQL for network_rx or network_tx. Pod-level
// metric — no workload labels in the source data, so we have to inject a
// pod=~"..." selector from server-side pod resolution. perContainer is
// ignored on network (the metric is pod-keyed, not container-keyed).
func (b *promBuilder) buildNetwork(direction MetricKind) string {
	metricName := "container_network_receive_bytes_total"
	if direction == MetricNetworkTX {
		metricName = "container_network_transmit_bytes_total"
	}
	if strings.EqualFold(b.kind, "Pod") {
		return fmt.Sprintf(
			`sum(rate(%s{cluster_id=%q,namespace=%q,pod=%q}[%s]))`,
			metricName, b.clusterUID, b.namespace, b.name, promDuration(b.rateWindow),
		)
	}
	// Workload roll-up: regex-OR the resolved pod set.
	return fmt.Sprintf(
		`sum(rate(%s{cluster_id=%q,namespace=%q,pod=~%q}[%s]))`,
		metricName, b.clusterUID, b.namespace, podRegex(b.pods), promDuration(b.rateWindow),
	)
}

// buildRequestsLimits returns the instant query for total
// requests-or-limits for a given resource (cpu/memory) across the resolved
// pods. Used for the utilizationPercent join. We aggregate across containers
// — `summary.max / pod-aggregate-limit` is what an operator means by "at
// limit", not the per-container limit.
func (b *promBuilder) buildRequestsLimits(resource string, mode string /* "requests" | "limits" */) string {
	metricName := "kube_pod_container_resource_requests"
	if mode == "limits" {
		metricName = "kube_pod_container_resource_limits"
	}
	if strings.EqualFold(b.kind, "Pod") {
		return fmt.Sprintf(
			`sum(%s{cluster_id=%q,namespace=%q,pod=%q,resource=%q})`,
			metricName, b.clusterUID, b.namespace, b.name, resource,
		)
	}
	if len(b.pods) == 0 {
		// No pods → no series → KSM join returns empty. Caller treats as absent.
		return fmt.Sprintf(
			`sum(%s{cluster_id=%q,namespace=%q,pod=~"__no_pods__",resource=%q})`,
			metricName, b.clusterUID, b.namespace, resource,
		)
	}
	return fmt.Sprintf(
		`sum(%s{cluster_id=%q,namespace=%q,pod=~%q,resource=%q})`,
		metricName, b.clusterUID, b.namespace, podRegex(b.pods), resource,
	)
}

// promDuration renders a Go time.Duration as a PromQL duration literal.
// PromQL only accepts integer counts so we round to the smallest unit that
// gives a whole number. Keeps the queries readable in golden tests.
func promDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// podRegex builds the `a|b|c` body for a pod=~"..." selector. Anchors are
// implicit on either side because PromQL matches against the full label
// value. Names are escaped for regex metachars — pod names are RFC1123
// (letters, digits, `-`, `.`) so only `.` needs escaping.
func podRegex(pods []string) string {
	escaped := make([]string, 0, len(pods))
	for _, p := range pods {
		escaped = append(escaped, strings.ReplaceAll(p, ".", `\.`))
	}
	return strings.Join(escaped, "|")
}

// metricPoint is one (timestamp, value) sample from VM.
type metricPoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// metricSummary captures the four headline figures every metric returns.
type metricSummary struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
	P95 float64 `json:"p95"`
}

// summarize computes min/avg/max/p95 from a point series. Empty input
// returns zero-valued summary — caller decides whether to surface it as
// "no data" or render zeros. p95 is computed client-side on the 12-ish
// points we already have; a quantile_over_time call would cost a round-trip
// for one number with marginal accuracy gain.
func summarize(points []metricPoint) metricSummary {
	if len(points) == 0 {
		return metricSummary{}
	}
	values := make([]float64, 0, len(points))
	var sum float64
	min, max := math.Inf(1), math.Inf(-1)
	for _, p := range points {
		if math.IsNaN(p.V) {
			continue
		}
		values = append(values, p.V)
		sum += p.V
		if p.V < min {
			min = p.V
		}
		if p.V > max {
			max = p.V
		}
	}
	if len(values) == 0 {
		return metricSummary{}
	}
	sort.Float64s(values)
	// p95 with nearest-rank — fine for ~12 points; linear interpolation here
	// adds complexity without meaningful precision.
	idx := int(math.Ceil(0.95*float64(len(values)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return metricSummary{
		Min: min,
		Avg: sum / float64(len(values)),
		Max: max,
		P95: values[idx],
	}
}

// downsample reduces a series to at most `target` points by bucket-averaging.
// VM's step table is already tuned so each range yields ~12 points; this
// helper exists as a safety net for ranges where VM returns more (e.g. when
// the step doesn't divide the duration evenly).
func downsample(points []metricPoint, target int) []metricPoint {
	if target <= 0 || len(points) <= target {
		return points
	}
	bucketSize := float64(len(points)) / float64(target)
	out := make([]metricPoint, 0, target)
	for i := 0; i < target; i++ {
		startIdx := int(math.Floor(float64(i) * bucketSize))
		endIdx := int(math.Floor(float64(i+1) * bucketSize))
		if endIdx > len(points) {
			endIdx = len(points)
		}
		if startIdx >= endIdx {
			continue
		}
		var sum float64
		for j := startIdx; j < endIdx; j++ {
			sum += points[j].V
		}
		// Use the timestamp of the midpoint of the bucket so the trend
		// rendered on the LLM side lines up with the wall-clock minute
		// the values came from.
		mid := points[startIdx+(endIdx-startIdx)/2]
		out = append(out, metricPoint{
			T: mid.T,
			V: sum / float64(endIdx-startIdx),
		})
	}
	return out
}

// vmRangeResponse is the subset of VM's /api/v1/query_range response shape we
// consume. VM mirrors Prometheus here.
type vmRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"` // [unix_ts (float), value (string)]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// vmInstantResponse is the subset of /api/v1/query response we consume.
type vmInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any              `json:"value"` // [unix_ts (float), value (string)]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// metricsStorageURLForCopilot mirrors api.metricsStorageURL() but lives here
// to keep the copilot package free of an api-package dep. The env var is the
// single source of truth — see deploy templates.
func metricsStorageURLForCopilot() string {
	if u := os.Getenv("KUBEBOLT_METRICS_STORAGE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8428"
}

// vmHTTPClient is the package-level client for VM round-trips. 15s matches
// the api package's metricsHTTPClient — long enough for slow range queries
// over 24h windows, short enough to fail loud when VM is wedged.
var vmHTTPClient = &http.Client{Timeout: 15 * time.Second}

// queryRange runs a /api/v1/query_range against VM and returns the first
// series' points. The PromQL builders above intentionally produce
// single-series outputs (workload-aggregated via `sum`); multi-series
// responses indicate a bug in the builder and we surface the first only.
func queryRange(ctx context.Context, promQL string, start, end time.Time, step time.Duration) ([]metricPoint, error) {
	u, err := url.Parse(metricsStorageURLForCopilot() + "/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("vm url: %w", err)
	}
	params := url.Values{
		"query": {promQL},
		"start": {fmt.Sprintf("%d", start.Unix())},
		"end":   {fmt.Sprintf("%d", end.Unix())},
		"step":  {promDuration(step)},
	}
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("vm req: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := vmHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vm unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vm body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vm status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var parsed vmRangeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vm decode: %w", err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("vm error: %s", parsed.Error)
	}
	if len(parsed.Data.Result) == 0 {
		return nil, nil
	}
	return convertVMValues(parsed.Data.Result[0].Values), nil
}

// queryInstant runs a /api/v1/query for a single value at `at`. Used for the
// requests/limits KSM join — we only need the current setpoint.
func queryInstant(ctx context.Context, promQL string, at time.Time) (float64, bool, error) {
	u, err := url.Parse(metricsStorageURLForCopilot() + "/api/v1/query")
	if err != nil {
		return 0, false, fmt.Errorf("vm url: %w", err)
	}
	params := url.Values{
		"query": {promQL},
		"time":  {fmt.Sprintf("%d", at.Unix())},
	}
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, false, fmt.Errorf("vm req: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := vmHTTPClient.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("vm unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false, fmt.Errorf("vm body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("vm status %d", resp.StatusCode)
	}

	var parsed vmInstantResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, false, fmt.Errorf("vm decode: %w", err)
	}
	if parsed.Status != "success" {
		return 0, false, fmt.Errorf("vm error: %s", parsed.Error)
	}
	if len(parsed.Data.Result) == 0 || len(parsed.Data.Result[0].Value) < 2 {
		return 0, false, nil
	}
	v, ok := parseFloatFromVM(parsed.Data.Result[0].Value[1])
	if !ok {
		return 0, false, nil
	}
	return v, true, nil
}

// convertVMValues turns VM's [[ts, "value"], ...] shape into typed points.
// Bad rows are skipped silently — VM almost never produces them and a single
// bad row shouldn't kill the whole response.
func convertVMValues(rows [][]any) []metricPoint {
	out := make([]metricPoint, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		ts, ok := row[0].(float64)
		if !ok {
			continue
		}
		v, ok := parseFloatFromVM(row[1])
		if !ok {
			continue
		}
		out = append(out, metricPoint{
			T: time.Unix(int64(ts), 0).UTC(),
			V: v,
		})
	}
	return out
}

// parseFloatFromVM accepts either VM's canonical string-encoded float or a
// raw float (depending on Content-Type negotiation). Returns false on
// non-numeric inputs like "NaN".
func parseFloatFromVM(raw any) (float64, bool) {
	switch v := raw.(type) {
	case string:
		if v == "" || v == "NaN" {
			return 0, false
		}
		var f float64
		_, err := fmt.Sscanf(v, "%g", &f)
		if err != nil {
			return 0, false
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// truncate trims a string to n bytes with an ellipsis marker. Used for
// upstream error bodies that VM occasionally returns as full HTML pages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
