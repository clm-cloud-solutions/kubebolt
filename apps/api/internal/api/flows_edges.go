package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// FlowEdge is the rendered-friendly shape of a single pod-to-pod flow.
// Matches what the frontend ClusterMap wants to read without doing
// further PromQL parsing.
type FlowEdge struct {
	SrcNamespace string     `json:"srcNamespace"`
	SrcPod       string     `json:"srcPod"`
	DstNamespace string     `json:"dstNamespace"`
	DstPod       string     `json:"dstPod"`
	Verdict      string     `json:"verdict"`
	RatePerSec   float64    `json:"ratePerSec"`
	L7           *L7Summary `json:"l7,omitempty"`
}

// L7Summary decorates a forwarded edge with HTTP observations when the
// Cilium proxy has L7 visibility enabled for the destination pod.
// Absent on edges where no HTTP traffic was parsed.
type L7Summary struct {
	RequestsPerSec float64            `json:"requestsPerSec"`
	StatusClass    map[string]float64 `json:"statusClass"` // ok / redir / client_err / server_err / info / unknown → req/s
	AvgLatencyMs   float64            `json:"avgLatencyMs,omitempty"`
}

// FlowEdgesResponse wraps the edge list with window metadata so the UI
// can label the chart ("last 5m") without guessing.
type FlowEdgesResponse struct {
	Edges         []FlowEdge `json:"edges"`
	WindowMinutes int        `json:"windowMinutes"`
	Source        string     `json:"source"` // always "hubble" for now
}

// handleFlowEdges executes the PromQL that aggregates pod_flow_events_total
// into per-pair rates over the requested window and shapes the result as
// FlowEdgesResponse. Params:
//
//	window=5m   (default 5, accepts plain minutes as integer)
//	namespace=<ns>  (optional — filters both src and dst to that namespace)
//
// The window is capped at 60 minutes to keep cardinality reasonable.
//
// When the agent has received L7 HTTP events from the Hubble proxy, two
// additional queries enrich the forwarded edges with status-class and
// average latency so the cluster map can color edges by HTTP health.
func (h *handlers) handleFlowEdges(w http.ResponseWriter, r *http.Request) {
	// 1-minute default: the cluster map is a "current activity" view, not
	// a historical one. Longer windows mask recent status-class changes
	// (e.g. a burst of 5xx takes minutes to dominate a 5m rate), which
	// the user has to wait out. Agent flushes every 5s, so 1m is plenty
	// of samples for stable rate() even on quiet pairs.
	windowMin := 1
	if v := r.URL.Query().Get("window"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			respondError(w, http.StatusBadRequest, "window must be a positive integer (minutes)")
			return
		}
		if n > 60 {
			n = 60
		}
		windowMin = n
	}

	ns := r.URL.Query().Get("namespace")

	var eventsSelector string
	var httpSelector string
	var latSelector string
	if ns != "" {
		// Match either side in the requested namespace. Intra-namespace
		// traffic lights up both halves of the OR naturally.
		eventsSelector = fmt.Sprintf(`{src_namespace=%q,source="hubble"} or pod_flow_events_total{dst_namespace=%q,source="hubble"}`, ns, ns)
		httpSelector = fmt.Sprintf(`{src_namespace=%q,source="hubble"} or pod_flow_http_requests_total{dst_namespace=%q,source="hubble"}`, ns, ns)
		latSelector = fmt.Sprintf(`{src_namespace=%q,source="hubble"} or pod_flow_http_latency_seconds_sum{dst_namespace=%q,source="hubble"}`, ns, ns)
	} else {
		eventsSelector = `{source="hubble"}`
		httpSelector = `{source="hubble"}`
		latSelector = `{source="hubble"}`
	}

	// Scope all three queries to the currently-connected cluster so a
	// secondary cluster (e.g. docker-desktop running in parallel with
	// kind) doesn't contribute flows that belong to a different
	// visual.
	uid := h.activeClusterUID()
	eventsQuery := scopeQueryByCluster(fmt.Sprintf(
		`sum by (src_namespace, src_pod, dst_namespace, dst_pod, verdict) (rate(pod_flow_events_total%s[%dm]))`,
		eventsSelector, windowMin,
	), uid)
	httpQuery := scopeQueryByCluster(fmt.Sprintf(
		`sum by (src_namespace, src_pod, dst_namespace, dst_pod, status_class) (rate(pod_flow_http_requests_total%s[%dm]))`,
		httpSelector, windowMin,
	), uid)
	// Avg latency = rate(sum) / rate(count). Small windows with 0 count
	// yield NaN, which we filter out below.
	latQuery := scopeQueryByCluster(fmt.Sprintf(
		`sum by (src_namespace, src_pod, dst_namespace, dst_pod) (rate(pod_flow_http_latency_seconds_sum%s[%dm])) / sum by (src_namespace, src_pod, dst_namespace, dst_pod) (rate(pod_flow_http_latency_seconds_count%s[%dm]))`,
		latSelector, windowMin, latSelector, windowMin,
	), uid)

	eventsRows, err := runInstantQuery(r.Context(), eventsQuery)
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}

	// L7 queries are best-effort — if they fail (metric doesn't exist
	// yet, VM hiccup) we still return the event rates. The cluster map
	// falls back to verdict coloring.
	httpRows, _ := runInstantQuery(r.Context(), httpQuery)
	latRows, _ := runInstantQuery(r.Context(), latQuery)

	// Pre-bucket HTTP by pair so the edge-build loop below is O(1) lookup.
	type pairKey struct{ srcNs, srcPod, dstNs, dstPod string }
	httpByPair := map[pairKey]*L7Summary{}
	for _, row := range httpRows {
		k := pairKey{
			srcNs:  row.Labels["src_namespace"],
			srcPod: row.Labels["src_pod"],
			dstNs:  row.Labels["dst_namespace"],
			dstPod: row.Labels["dst_pod"],
		}
		sc := row.Labels["status_class"]
		if sc == "" {
			sc = "unknown"
		}
		s := httpByPair[k]
		if s == nil {
			s = &L7Summary{StatusClass: map[string]float64{}}
			httpByPair[k] = s
		}
		s.StatusClass[sc] += row.Value
		s.RequestsPerSec += row.Value
	}
	for _, row := range latRows {
		k := pairKey{
			srcNs:  row.Labels["src_namespace"],
			srcPod: row.Labels["src_pod"],
			dstNs:  row.Labels["dst_namespace"],
			dstPod: row.Labels["dst_pod"],
		}
		// VM returns NaN as "NaN" string; ParseFloat handles that and
		// yields NaN which we reject (!=), keeping the latency absent.
		if row.Value != row.Value || row.Value <= 0 {
			continue
		}
		if s := httpByPair[k]; s != nil {
			s.AvgLatencyMs = row.Value * 1000
		}
	}

	edges := make([]FlowEdge, 0, len(eventsRows))
	for _, row := range eventsRows {
		edge := FlowEdge{
			SrcNamespace: row.Labels["src_namespace"],
			SrcPod:       row.Labels["src_pod"],
			DstNamespace: row.Labels["dst_namespace"],
			DstPod:       row.Labels["dst_pod"],
			Verdict:      row.Labels["verdict"],
			RatePerSec:   row.Value,
		}
		// Only attach L7 to the forwarded edge — dropped flows by
		// definition never reached the proxy.
		if edge.Verdict == "forwarded" {
			k := pairKey{edge.SrcNamespace, edge.SrcPod, edge.DstNamespace, edge.DstPod}
			if s, ok := httpByPair[k]; ok {
				edge.L7 = s
			}
		}
		edges = append(edges, edge)
	}

	respondJSON(w, http.StatusOK, FlowEdgesResponse{
		Edges:         edges,
		WindowMinutes: windowMin,
		Source:        "hubble",
	})
}

// vmRow is one result row from a VictoriaMetrics instant query, shaped
// for the handler's needs (labels + parsed float value).
type vmRow struct {
	Labels map[string]string
	Value  float64
}

func runInstantQuery(ctx context.Context, query string) ([]vmRow, error) {
	target, _ := url.Parse(metricsStorageURL() + "/api/v1/query")
	params := url.Values{"query": {query}}
	target.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build upstream request")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := metricsHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metrics storage unreachable")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upstream body")
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(body))
	}

	var vmResp struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]interface{}    `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &vmResp); err != nil {
		return nil, fmt.Errorf("parse upstream body: %s", err.Error())
	}

	out := make([]vmRow, 0, len(vmResp.Data.Result))
	for _, r := range vmResp.Data.Result {
		rateStr, ok := r.Value[1].(string)
		if !ok {
			continue
		}
		rate, err := strconv.ParseFloat(rateStr, 64)
		if err != nil {
			continue
		}
		out = append(out, vmRow{Labels: r.Metric, Value: rate})
	}
	return out, nil
}
