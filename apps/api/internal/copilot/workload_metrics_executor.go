package copilot

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// Workload metrics tool — executor case. See spec
// internal/copilot-execution-capacity/07-workload-metrics-tool.md and the
// pure helpers in workload_metrics.go.

// supportedWorkloadKinds is the input enum. Match exactly (case-sensitive)
// against the metric labels emitted by the agent — `Deployment` lower-cased
// would silently miss every series. Node was added in spec #07 V2: same
// tool surface, different PromQL underneath (node_* metrics).
var supportedWorkloadKinds = map[string]bool{
	"Pod":         true,
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"Job":         true,
	"CronJob":     true,
	"Node":        true,
}

// kindToConnectorType maps the input enum to the lowercase plural type the
// connector's GetResourceDetail / pod listers expect. Keeps the LLM's input
// (singular CamelCase, matching workload_kind labels) decoupled from the
// connector's internal naming.
var kindToConnectorType = map[string]string{
	"Pod":         "pods",
	"Deployment":  "deployments",
	"StatefulSet": "statefulsets",
	"DaemonSet":   "daemonsets",
	"Job":         "jobs",
	"CronJob":     "cronjobs",
	"Node":        "nodes",
}

// workloadMetricsResponse is the JSON shape the LLM sees. Fields here are
// the spec's contract — the model has been primed (via the tool description
// and system prompt) to read `summary` first and `utilizationPercent`
// when present. Renaming requires updating the prompt in lockstep.
type workloadMetricsResponse struct {
	Workload     workloadRef               `json:"workload"`
	Range        string                    `json:"range"`
	End          string                    `json:"end"`
	PodsResolved int                       `json:"podsResolved"`
	Metrics      map[string]metricResponse `json:"metrics"`
	Note         string                    `json:"note,omitempty"`
}

type workloadRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type metricResponse struct {
	Unit               string                     `json:"unit"`
	Summary            metricSummary              `json:"summary"`
	Trend              []metricPoint              `json:"trend"`
	Request            *float64                   `json:"request,omitempty"`
	Limit              *float64                   `json:"limit,omitempty"`
	UtilizationPercent *utilizationPercent        `json:"utilizationPercent,omitempty"`
	// PerContainer is the per-container breakdown when the caller passed
	// perContainer=true. The top-level Summary/Trend remain the workload
	// (or pod) aggregate so the LLM can answer "how much does the pod use
	// overall?" without re-aggregating. Empty unless perContainer=true.
	PerContainer map[string]containerMetric `json:"perContainer,omitempty"`
}

type containerMetric struct {
	Summary metricSummary `json:"summary"`
	Trend   []metricPoint `json:"trend"`
}

type utilizationPercent struct {
	VsRequest *float64 `json:"vsRequest,omitempty"`
	VsLimit   *float64 `json:"vsLimit,omitempty"`
}

// execGetWorkloadMetrics is the bulk of the tool. Returns a JSON string on
// success, or a JSON string error wrapped in a non-nil error on failure (so
// the executor switch can mark IsError without duplicating the marshal).
func (e *Executor) execGetWorkloadMetrics(_ ToolCall, args map[string]interface{}, conn *cluster.Connector) (string, error) {
	kind := stringArg(args, "kind")
	namespace := stringArg(args, "namespace")
	name := stringArg(args, "name")
	rangeStr := stringArg(args, "range")
	if rangeStr == "" {
		rangeStr = "15m"
	}
	perContainer := boolArg(args, "perContainer")

	if kind == "" || name == "" {
		return "", fmt.Errorf(`{"error":"kind and name are required"}`)
	}
	// Namespace is required for everything except Node (cluster-scoped).
	// LLMs occasionally pass "_" or "" for cluster-scoped resources — we
	// accept either form gracefully so Kobi doesn't have to encode that
	// detail in its prompt logic.
	if kind != "Node" && namespace == "" {
		return "", fmt.Errorf(`{"error":"namespace is required for kind=%q"}`, kind)
	}
	if !supportedWorkloadKinds[kind] {
		return "", fmt.Errorf(`{"error":"unsupported kind %q","valid":["Pod","Deployment","StatefulSet","DaemonSet","Job","CronJob","Node"]}`, kind)
	}
	metrics, err := parseMetricsArg(args["metrics"])
	if err != nil {
		return "", fmt.Errorf(`{"error":%q}`, err.Error())
	}

	now := time.Now().UTC()
	start, end, spec, err := parseMetricsRange(rangeStr, now)
	if err != nil {
		return "", fmt.Errorf(`{"error":%q}`, err.Error())
	}

	// Verify target exists. Same early-404 pattern as the propose_* tools so
	// the LLM gets a clear "doesn't exist" rather than an empty metrics
	// response that it might misread as "exists but idle".
	ctype, ok := kindToConnectorType[kind]
	if !ok {
		return "", fmt.Errorf(`{"error":"internal: no connector type mapped for kind %q"}`, kind)
	}
	if _, err := conn.GetResourceDetail(ctype, namespace, name); err != nil {
		return "", fmt.Errorf(`{"error":"target not found: %s %s/%s"}`, kind, namespace, name)
	}

	// Resolve pod set. For Pod kind it's just the target. For workloads we
	// need the pods to (a) build the network selector and (b) build the
	// requests/limits join even when KSM doesn't carry workload labels.
	// Node kind has no pod set — the node IS the target; podsResolved is
	// reused as "target resolved" (1 if the node exists, 0 if missing).
	var pods []string
	if kind == "Node" {
		// GetResourceDetail above already confirmed the node exists, so
		// this is purely about populating podsResolved=1 for the response
		// shape (the LLM uses it to know "this query had data behind it").
		pods = []string{name}
	} else {
		pods, err = resolveWorkloadPods(conn, kind, namespace, name)
		if err != nil {
			return "", fmt.Errorf(`{"error":%q}`, err.Error())
		}
	}

	// Hard cap on network roll-up. CPU/memory aggregate via workload_kind so
	// they don't hit a regex blowup, but network needs pod=~"..." which gets
	// expensive on huge workloads. Node kind skips this — node-level network
	// is a single series per device, bounded by the (small) interface count.
	needsNetwork := containsAny(metrics, MetricNetworkRX, MetricNetworkTX)
	if kind != "Node" && needsNetwork && len(pods) > maxNetworkPods {
		return "", fmt.Errorf(`{"error":"workload too large for network roll-up (%d pods > %d cap) — call with kind=Pod for the specific pods of interest","podsResolved":%d}`,
			len(pods), maxNetworkPods, len(pods))
	}

	// Build the response skeleton. podsResolved=0 is not an error — Kobi
	// gets the workload identity back plus a note, lets it diagnose.
	resp := workloadMetricsResponse{
		Workload:     workloadRef{Kind: kind, Namespace: namespace, Name: name},
		Range:        rangeStr,
		End:          end.Format(time.RFC3339),
		PodsResolved: len(pods),
		Metrics:      map[string]metricResponse{},
	}
	// Empty-target short-circuit applies to non-Pod, non-Node kinds where
	// pod resolution yielded nothing (workload paused, scaled to 0, etc).
	// Node kind never hits this — the existence check above already 404'd
	// for missing nodes.
	if len(pods) == 0 && kind != "Pod" && kind != "Node" {
		resp.Note = "Workload has no running pods in the queried range"
		// Still emit empty metric entries so the LLM sees the shape it
		// expected, with zeroes — clearer than "metrics: {}".
		// Trend MUST be a non-nil slice (Go marshals nil as JSON null
		// which crashes the frontend's chart-card rendering on
		// `.length`); empty slice marshals as `[]` and renders the
		// empty-state branch cleanly.
		for _, m := range metrics {
			resp.Metrics[string(m)] = metricResponse{
				Unit:    unitFor(m),
				Summary: metricSummary{},
				Trend:   []metricPoint{},
			}
		}
		return marshal(resp), nil
	}

	b := promBuilder{
		kind:         kind,
		namespace:    namespace,
		name:         name,
		clusterUID:   conn.ClusterUID(),
		pods:         pods,
		perContainer: perContainer,
		rateWindow:   spec.RateWindow,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, m := range metrics {
		mr, qerr := runMetric(ctx, b, m, start, end, spec.Step)
		if qerr != nil {
			// One metric's failure shouldn't kill the others — record an
			// empty response with the error inline so the LLM can decide
			// whether to retry or proceed. Trend stays a non-nil slice so
			// the frontend's chart card render doesn't crash on `.length`.
			resp.Metrics[string(m)] = metricResponse{
				Unit:    unitFor(m),
				Summary: metricSummary{},
				Trend:   []metricPoint{},
			}
			continue
		}
		resp.Metrics[string(m)] = mr
	}

	// Attach requests/limits + utilizationPercent for CPU and memory. This
	// is what spec #07 §"Requests/limits join" describes — handled below.
	attachRequestsLimits(ctx, &resp, b, end)

	return marshal(resp), nil
}

// parseMetricsArg extracts the metric enum array. Returns a clear error on
// any malformed entry so the LLM sees the valid set and can retry.
func parseMetricsArg(raw interface{}) ([]MetricKind, error) {
	asSlice, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("metrics must be a non-empty array of strings")
	}
	if len(asSlice) == 0 {
		return nil, fmt.Errorf("metrics must be a non-empty array of strings")
	}
	valid := map[string]MetricKind{
		"cpu":        MetricCPU,
		"memory":     MetricMemory,
		"network_rx": MetricNetworkRX,
		"network_tx": MetricNetworkTX,
	}
	seen := map[MetricKind]bool{}
	out := make([]MetricKind, 0, len(asSlice))
	for _, item := range asSlice {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("metrics entries must be strings")
		}
		mk, ok := valid[s]
		if !ok {
			return nil, fmt.Errorf("invalid metric %q (valid: cpu, memory, network_rx, network_tx)", s)
		}
		if seen[mk] {
			continue
		}
		seen[mk] = true
		out = append(out, mk)
	}
	return out, nil
}

// resolveWorkloadPods returns the pod names owned by the workload. For Pod
// kind the "owner" is the pod itself. For CronJob we walk to active Jobs
// and union their pods — gives Kobi a useful snapshot of "what's running
// right now under this CronJob", which is the question that maps to the
// metric data we have.
func resolveWorkloadPods(conn *cluster.Connector, kind, namespace, name string) ([]string, error) {
	switch kind {
	case "Pod":
		return []string{name}, nil
	case "Deployment":
		return extractPodNames(conn.GetDeploymentPods(namespace, name)), nil
	case "StatefulSet":
		return extractPodNames(conn.GetStatefulSetPods(namespace, name)), nil
	case "DaemonSet":
		return extractPodNames(conn.GetDaemonSetPods(namespace, name)), nil
	case "Job":
		return extractPodNames(conn.GetJobPods(namespace, name)), nil
	case "CronJob":
		jobs := conn.GetCronJobJobs(namespace, name)
		var pods []string
		for _, j := range jobs {
			jname, _ := j["name"].(string)
			if jname == "" {
				continue
			}
			pods = append(pods, extractPodNames(conn.GetJobPods(namespace, jname))...)
		}
		// Dedup just in case (shouldn't happen, but cheap).
		return dedupStrings(pods), nil
	}
	return nil, fmt.Errorf("unsupported kind: %s", kind)
}

func extractPodNames(rows []map[string]interface{}) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if n, ok := r["name"].(string); ok && n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out) // deterministic — keeps the PromQL regex stable across calls
	return out
}

func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// runMetric runs one metric's range query, summarizes, and downsamples.
// Handles both single-series (workload aggregate) and multi-series
// (perContainer=true → one series per container) responses.
//
// For perContainer mode we also compute a top-level Summary/Trend that
// reflects the sum across containers — useful for "is the pod overall
// hot?" follow-ups without a second query. Aggregation is point-by-point
// merge by timestamp because all containers share the same VM step.
func runMetric(ctx context.Context, b promBuilder, m MetricKind, start, end time.Time, step time.Duration) (metricResponse, error) {
	var query string
	switch m {
	case MetricCPU:
		query = b.buildCPU()
	case MetricMemory:
		query = b.buildMemory()
	case MetricNetworkRX, MetricNetworkTX:
		query = b.buildNetwork(m)
	default:
		return metricResponse{}, fmt.Errorf("unknown metric: %s", m)
	}
	series, err := queryRange(ctx, query, start, end, step)
	if err != nil {
		return metricResponse{}, err
	}
	// Always initialise Trend to a non-nil slice. Go marshals nil slices
	// as JSON null, which broke the frontend's `.length` access in chart
	// rendering (caught by ErrorBoundary as "Cannot read properties of
	// null (reading 'length')"). Empty slice marshals as `[]` and the
	// frontend's chart card handles "0 points" cleanly.
	out := metricResponse{Unit: unitFor(m), Trend: []metricPoint{}}
	if len(series) == 0 {
		return out, nil
	}

	if !b.perContainer || m == MetricNetworkRX || m == MetricNetworkTX {
		// Single-series shape: the PromQL is `sum(...)`, network is
		// always pod-keyed (no container split). Take the first/only
		// series. Defensive: if VM unexpectedly returned multi-series
		// here, we still use the first — the builder tests guarantee
		// the query is single-series for this path.
		points := series[0].Points
		out.Summary = summarize(points)
		out.Trend = downsample(points, trendTargetPoints)
		return out, nil
	}

	// perContainer for CPU/memory: route series by container label.
	out.PerContainer = map[string]containerMetric{}
	for _, s := range series {
		container := s.Labels["container"]
		if container == "" {
			// VM returned a series without the container label — shouldn't
			// happen for `sum by (container)` but skip defensively so an
			// unexpected blank doesn't shadow a real container's data.
			continue
		}
		out.PerContainer[container] = containerMetric{
			Summary: summarize(s.Points),
			Trend:   downsample(s.Points, trendTargetPoints),
		}
	}
	// Top-level aggregate: sum the per-container values at each timestamp.
	// The LLM still sees a workload-level Summary/Trend; perContainer adds
	// the breakdown alongside.
	aggregate := mergeSeriesByTimestamp(series)
	out.Summary = summarize(aggregate)
	out.Trend = downsample(aggregate, trendTargetPoints)
	return out, nil
}

// mergeSeriesByTimestamp sums sample values across series at matching
// timestamps. Used to compute the pod-level aggregate from a
// per-container response. All container series share the same VM step,
// so the timestamp set is consistent in the happy path; we still merge
// rather than assume so a container that started mid-window doesn't
// break the alignment.
func mergeSeriesByTimestamp(series []vmSeries) []metricPoint {
	byTs := map[int64]float64{}
	for _, s := range series {
		for _, p := range s.Points {
			byTs[p.T.Unix()] += p.V
		}
	}
	keys := make([]int64, 0, len(byTs))
	for k := range byTs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]metricPoint, len(keys))
	for i, k := range keys {
		out[i] = metricPoint{T: time.Unix(k, 0).UTC(), V: byTs[k]}
	}
	return out
}

// attachRequestsLimits performs the KSM join for CPU and memory metrics.
// The LLM's main use case for this tool is sizing propose_set_resources;
// utilizationPercent is the single derived field that answers "at limit?"
// without another tool round-trip. KSM absence is handled gracefully — we
// just leave the fields off and surface a note.
func attachRequestsLimits(ctx context.Context, resp *workloadMetricsResponse, b promBuilder, at time.Time) {
	type resourceKey struct {
		metric   string
		resource string // cpu | memory
	}
	jobs := []resourceKey{}
	if _, ok := resp.Metrics["cpu"]; ok {
		jobs = append(jobs, resourceKey{"cpu", "cpu"})
	}
	if _, ok := resp.Metrics["memory"]; ok {
		jobs = append(jobs, resourceKey{"memory", "memory"})
	}
	if len(jobs) == 0 {
		return
	}

	ksmSeen := false
	for _, j := range jobs {
		mr := resp.Metrics[j.metric]
		reqVal, reqOK, reqErr := queryInstant(ctx, b.buildRequestsLimits(j.resource, "requests"), at)
		limVal, limOK, limErr := queryInstant(ctx, b.buildRequestsLimits(j.resource, "limits"), at)
		if reqErr == nil && limErr == nil && (reqOK || limOK) {
			ksmSeen = true
		}
		if reqOK {
			rv := reqVal
			mr.Request = &rv
		}
		if limOK {
			lv := limVal
			mr.Limit = &lv
		}
		if (reqOK || limOK) && mr.Summary.Max != 0 {
			up := utilizationPercent{}
			if reqOK && reqVal > 0 {
				v := (mr.Summary.Max / reqVal) * 100
				up.VsRequest = &v
			}
			if limOK && limVal > 0 {
				v := (mr.Summary.Max / limVal) * 100
				up.VsLimit = &v
			}
			if up.VsRequest != nil || up.VsLimit != nil {
				mr.UtilizationPercent = &up
			}
		}
		resp.Metrics[j.metric] = mr
	}
	if !ksmSeen && resp.Note == "" {
		resp.Note = "requests/limits not available — kube-state-metrics not detected"
	}
}

// unitFor returns the canonical unit string per metric. Always emitted so
// the LLM doesn't have to remember.
func unitFor(m MetricKind) string {
	switch m {
	case MetricCPU:
		return "cores"
	case MetricMemory:
		return "bytes"
	case MetricNetworkRX, MetricNetworkTX:
		return "bytes/sec"
	}
	return ""
}

// containsAny reports whether any of `targets` appears in `set`. Used to
// detect "is network requested" without a full pass.
func containsAny(set []MetricKind, targets ...MetricKind) bool {
	for _, s := range set {
		for _, t := range targets {
			if s == t {
				return true
			}
		}
	}
	return false
}

// marshal wraps jsonString so error returns from the tool flow can share the
// same JSON encoder + dedup the "marshal failed" fallback.
func marshal(v interface{}) string {
	return jsonString(v)
}
