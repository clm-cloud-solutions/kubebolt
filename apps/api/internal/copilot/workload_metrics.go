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
	"regexp"
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

// buildCPU returns the PromQL for the CPU range query. Uses the regex
// pattern from podNamePattern() to capture every pod the workload has ever
// spawned (so a 1h range across a rolling update sees the deleted pods'
// historical samples too).
//
// Aggregation:
//   - Default: `sum(...)` — produces a SINGLE time series with the workload
//     total. The executor only reads the first series in VM's response, so
//     `sum by (pod)` would silently truncate to one pod's rate.
//   - perContainer=true: `sum by (container)` — caller wants the split.
//     Multi-series response shape still needs the executor to handle it
//     fully (see workload_metrics_executor.go); single-container pods
//     collapse cleanly.
//
// The pod_uid!="" filter excludes duplicate series being scraped by an
// external Prometheus pointed at the same VM — kube-prometheus-stack's
// cadvisor scrape emits container_cpu_usage_seconds_total with the same
// pod label but no pod_uid (only the agent's enrichment sets it). Without
// this filter a cluster running both sources sees doubled rate values.
func (b *promBuilder) buildCPU() string {
	if b.isNode() {
		// Agent's node_cpu_usage_seconds_total has labels {cluster_id, node,
		// tenant_id} — no pod_uid, no namespace, no container. Different
		// metric name from node-exporter's `node_cpu_seconds_total` (with
		// mode=user|system|idle), so no name collision.
		return fmt.Sprintf(
			`sum(rate(node_cpu_usage_seconds_total{cluster_id=%q,node=%q}[%s]))`,
			b.clusterUID, b.name, promDuration(b.rateWindow),
		)
	}
	inner := fmt.Sprintf(
		`rate(container_cpu_usage_seconds_total{cluster_id=%q,namespace=%q,%s,pod_uid!=""}[%s])`,
		b.clusterUID, b.namespace, b.podSelector(), promDuration(b.rateWindow),
	)
	return b.wrapAggregation(inner)
}

// buildMemory returns the PromQL for working-set memory. Memory is a gauge —
// no rate() wrap. Same pod-set + pod_uid + aggregation pattern as CPU.
// Node kind drops to a node-level selector — node_memory_working_set_bytes
// has no equivalent at the container level for nodes themselves.
func (b *promBuilder) buildMemory() string {
	if b.isNode() {
		return fmt.Sprintf(
			`sum(node_memory_working_set_bytes{cluster_id=%q,node=%q})`,
			b.clusterUID, b.name,
		)
	}
	inner := fmt.Sprintf(
		`container_memory_working_set_bytes{cluster_id=%q,namespace=%q,%s,pod_uid!=""}`,
		b.clusterUID, b.namespace, b.podSelector(),
	)
	return b.wrapAggregation(inner)
}

// isNode is a small helper to keep the kind check terse where it appears
// (every metric builder). The input enum value is "Node" — case-sensitive
// match against the workload_kind label convention and the schema enum.
func (b *promBuilder) isNode() bool {
	return b.kind == "Node"
}

// nodeNetworkDeviceFilter whitelists physical NICs while excluding the
// soup of virtual interfaces a CNI lays down (cilium_*, lxc*, veth*,
// cali*, flannel*, docker0, gre*, sit*, tunl*, lo, etc). Catches eth0
// / ens5 / eno1 / enp0s3 — every modern Linux NIC naming convention
// used by EKS, GKE, kind, vanilla Ubuntu, RHEL. Same pattern Grafana's
// node-exporter dashboards use. Without this the network rate would
// double-count container veth traffic on top of the physical NIC.
const nodeNetworkDeviceFilter = `eth.*|ens.*|en[a-z].*`

// wrapAggregation applies `sum(...)` or `sum by (container) (...)` depending
// on perContainer. Keeps the workload roll-up at one series (so the
// executor's single-series read returns the workload total, not just one
// pod's rate).
func (b *promBuilder) wrapAggregation(inner string) string {
	if b.perContainer {
		return fmt.Sprintf(`sum by (container) (%s)`, inner)
	}
	return fmt.Sprintf(`sum(%s)`, inner)
}

// podSelector returns the PromQL label-selector fragment that targets the
// pods owned by the workload. For Pod kind it's a literal `pod="<name>"`.
// For workload kinds it's a regex matching every pod the controller has
// ever spawned — current AND historically deleted — via the stable K8s
// pod-naming conventions (see podNamePattern). The historical match is
// the semantic the operator means by "metrics for this Deployment over
// 1h": if there was a rolling update 30 min ago, the previous RS's pods
// MUST show up in the trend, not just the survivors.
//
// We could pass the live pod list as `pod=~"a|b|c"`, but that under-reports
// after any rotation (deleted pods don't match) and is fragile to dump
// staleness in the lister. The regex pattern is robust to both.
func (b *promBuilder) podSelector() string {
	if strings.EqualFold(b.kind, "Pod") {
		return fmt.Sprintf(`pod=%q`, b.name)
	}
	return fmt.Sprintf(`pod=~%q`, b.podNamePattern())
}

// podNamePattern returns the regex (RE2, anchored full-match by VM) that
// matches every pod name a controller of this kind+name has ever produced.
// Each controller has a deterministic pod-naming convention:
//
//   - Deployment   <name>-<rs-hash>-<pod-suffix>          (two random groups)
//   - StatefulSet  <name>-<ordinal>                       (numeric ordinal)
//   - DaemonSet    <name>-<pod-suffix>                    (one random group)
//   - Job          <name>-<pod-suffix>                    (one random group)
//   - CronJob      <name>-<job-timestamp>-<pod-suffix>    (two groups, first numeric)
//
// The patterns are strict enough to avoid prefix collisions: a Deployment
// "api" pattern won't match "api-cache" pods because the `[a-z0-9]+` group
// can't span across `-` boundaries.
//
// Workload name is regex-escaped — K8s names are RFC1123 (no metachars in
// practice) but we don't trust the input shape.
func (b *promBuilder) podNamePattern() string {
	name := regexp.QuoteMeta(b.name)
	switch b.kind {
	case "Deployment":
		return name + "-[a-z0-9]+-[a-z0-9]+"
	case "StatefulSet":
		return name + "-[0-9]+"
	case "DaemonSet", "Job":
		return name + "-[a-z0-9]+"
	case "CronJob":
		return name + "-[0-9]+-[a-z0-9]+"
	}
	// Defensive fallback: should never be reached because supportedWorkloadKinds
	// gates the inputs. Permissive `.*` so a future kind isn't silently filtered
	// to zero before the validator catches it.
	return name + ".*"
}


// buildNetwork returns the PromQL for network_rx or network_tx.
//
// Kind branches:
//
//   - Node: uses node-level `node_network_*_bytes_total{device=~physical}`.
//     The device filter is critical — without it the sum includes every
//     veth/lxc/cilium interface, double-counting container traffic on top
//     of the physical NIC. (We learned this the hard way comparing
//     KubeBolt vs Grafana vs CloudWatch on yagan — Grafana was
//     over-reporting precisely because it summed veth devices.) The
//     emitting source can be either the agent's kubelet-stats collector
//     OR an external node-exporter scrape; both write the same metric
//     name. We don't filter by `job` so either source works.
//   - Pod / workload kinds: uses container-level data filtered by the
//     pod-name regex; perContainer is ignored because the metric is
//     pod-keyed (no container label at this level). The pod_uid!="" filter
//     excludes external Prometheus-scraped duplicates (see buildCPU).
func (b *promBuilder) buildNetwork(direction MetricKind) string {
	if b.isNode() {
		metricName := "node_network_receive_bytes_total"
		if direction == MetricNetworkTX {
			metricName = "node_network_transmit_bytes_total"
		}
		return fmt.Sprintf(
			`sum(rate(%s{cluster_id=%q,node=%q,device=~%q}[%s]))`,
			metricName, b.clusterUID, b.name, nodeNetworkDeviceFilter, promDuration(b.rateWindow),
		)
	}
	metricName := "container_network_receive_bytes_total"
	if direction == MetricNetworkTX {
		metricName = "container_network_transmit_bytes_total"
	}
	selector := b.podSelector()
	return fmt.Sprintf(
		`sum(rate(%s{cluster_id=%q,namespace=%q,%s,pod_uid!=""}[%s]))`,
		metricName, b.clusterUID, b.namespace, selector, promDuration(b.rateWindow),
	)
}

// buildRequestsLimits returns the instant query for the
// "request" and "limit" denominators used by the utilizationPercent join.
//
// Kind branches:
//
//   - Node: maps to kube_node_status_allocatable (the analog of "request"
//     — what the scheduler will actually let pods consume after system
//     reservations) and kube_node_status_capacity (the analog of "limit"
//     — total physical resources on the box). Allocatable ≤ capacity.
//     Treating capacity as the saturation ceiling is the semantic an
//     operator means by "how full is this node" — Kobi's utilizationPercent
//     vs limit becomes "% of node capacity used", which is what
//     dashboards like Capacity already display.
//   - Pod / workload kinds: kube_pod_container_resource_requests /
//     _limits, summed across containers in the pod set.
//
// KSM exports both shapes; missing KSM degrades gracefully (the executor's
// attachRequestsLimits handles the empty path and omits the chip).
func (b *promBuilder) buildRequestsLimits(resource string, mode string /* "requests" | "limits" */) string {
	if b.isNode() {
		// allocatable = "request" denominator; capacity = "limit" denominator.
		// Both have a `unit` label (core/byte) — we don't filter on it
		// because resource=cpu|memory uniquely determines the unit anyway,
		// and an extra filter only adds query brittleness when KSM versions
		// disagree on label spelling.
		metricName := "kube_node_status_allocatable"
		if mode == "limits" {
			metricName = "kube_node_status_capacity"
		}
		return fmt.Sprintf(
			`sum(%s{cluster_id=%q,node=%q,resource=%q})`,
			metricName, b.clusterUID, b.name, resource,
		)
	}
	metricName := "kube_pod_container_resource_requests"
	if mode == "limits" {
		metricName = "kube_pod_container_resource_limits"
	}
	return fmt.Sprintf(
		`sum(%s{cluster_id=%q,namespace=%q,%s,resource=%q})`,
		metricName, b.clusterUID, b.namespace, b.podSelector(), resource,
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

// vmURLTestOverride is intended for tests only. When non-empty it wins over
// the env var so unit tests can point queryRange/queryInstant at an
// httptest.Server without mutating shared environment.
var vmURLTestOverride string

// metricsStorageURLForCopilot mirrors api.metricsStorageURL() but lives here
// to keep the copilot package free of an api-package dep. The env var is the
// single source of truth — see deploy templates.
func metricsStorageURLForCopilot() string {
	if vmURLTestOverride != "" {
		return strings.TrimRight(vmURLTestOverride, "/")
	}
	if u := os.Getenv("KUBEBOLT_METRICS_STORAGE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8428"
}

// vmHTTPClient is the package-level client for VM round-trips. 15s matches
// the api package's metricsHTTPClient — long enough for slow range queries
// over 24h windows, short enough to fail loud when VM is wedged.
var vmHTTPClient = &http.Client{Timeout: 15 * time.Second}

// vmSeries is one labelled time-series from a VM range response. We
// return the full set (not just the first) so callers can route by
// labels — specifically the `container` label when perContainer=true
// produces a `sum by (container)` query with N results.
type vmSeries struct {
	Labels map[string]string
	Points []metricPoint
}

// queryRange runs a /api/v1/query_range against VM and returns ALL series
// in the response. The PromQL builders produce:
//
//   - `sum(...)` → one series (workload-aggregated)
//   - `sum by (container) (...)` → N series (per-container split)
//
// The earlier implementation returned only the first series, which
// silently truncated the per-container case to one container's data (the
// bug scenario 3 surfaced). Callers that expect one series check
// `len(result) == 1` themselves now; multi-series callers route by
// `result[i].Labels[<label>]`.
func queryRange(ctx context.Context, promQL string, start, end time.Time, step time.Duration) ([]vmSeries, error) {
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
	out := make([]vmSeries, 0, len(parsed.Data.Result))
	for _, r := range parsed.Data.Result {
		out = append(out, vmSeries{
			Labels: r.Metric,
			Points: convertVMValues(r.Values),
		})
	}
	return out, nil
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

// QueryInstant exposes the package-internal instant VM query to other packages — the
// metrics-only cluster overview (api package) builds resource counts + health from KSM
// via scoped instant queries. Same contract as queryInstant: (value, found, error).
func QueryInstant(ctx context.Context, promQL string, at time.Time) (float64, bool, error) {
	return queryInstant(ctx, promQL, at)
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
