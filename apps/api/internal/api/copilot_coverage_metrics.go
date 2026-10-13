package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// Sources behind get_coverage and query_metrics. Both live here, not in the
// copilot package, because they reuse the exact pipeline of their REST twins —
// GET /coverage's probes and GET /metrics/query's scoping — and a second copy
// of "which series may this caller read" is how isolation bugs are written.

// metricsTenantPinCtx is metricsTenantPin for a tool call, which carries a
// context rather than a request: the org whose series may be read, "" in
// single-tenant, and a sentinel matching nothing for a multi-tenant caller
// without an org.
func metricsTenantPinCtx(ctx context.Context) string {
	if t := findingsTenant(ctx); t != "" || !auth.MultiTenantEnabled {
		return t
	}
	return noTenantSentinel
}

type coverageSource struct{ h *handlers }

func (s coverageSource) MetricSources(ctx context.Context) []copilot.MetricSourceStatus {
	uid := s.h.activeClusterUID(ctx)
	tid := metricsTenantPinCtx(ctx)
	out := make([]copilot.MetricSourceStatus, 0, len(coverageProbes))
	for _, probe := range coverageProbes {
		q := scopeQueryByTenant(scopeQueryByCluster(probe.query, uid), tid)
		out = append(out, copilot.MetricSourceStatus{
			Name:   probe.name,
			Probe:  probe.query,
			Active: coverageStatusForQuery(ctx, q) == "active",
		})
	}
	return out
}

// Agents is strict on BOTH axes, org and cluster. /admin/agents reads "" as
// "the whole registry" for a caller without an org; a tool must not.
func (s coverageSource) Agents(ctx context.Context, clusterUID string) []copilot.AgentStatus {
	if s.h.agentRegistry == nil || clusterUID == "" {
		return nil
	}
	tid := findingsTenant(ctx)
	if tid == "" && auth.MultiTenantEnabled {
		return nil
	}
	var out []copilot.AgentStatus
	for _, a := range s.h.agentRegistry.List() {
		if a.ClusterID != clusterUID || (tid != "" && a.TenantID != tid) {
			continue
		}
		out = append(out, copilot.AgentStatus{NodeName: a.NodeName, AuthMode: a.AuthMode, ConnectedAt: a.Connected})
	}
	return out
}

func (h *handlers) coverageSourceFor() copilot.CoverageSource {
	if h.manager == nil {
		return nil
	}
	return coverageSource{h: h}
}

type metricsQuerySource struct{ h *handlers }

// metricsQueryBodyLimit bounds what the store may hand back to one tool call.
const metricsQueryBodyLimit = 8 << 20

// Query runs promQL confined the way GET /metrics/query confines it: any
// caller-written tenant_id / cluster_id matcher is stripped, then the request's
// cluster and the caller's org are injected. The server's values always win.
func (s metricsQuerySource) Query(ctx context.Context, promQL string, start, end time.Time, step time.Duration) ([]copilot.MetricSeries, error) {
	uid := s.h.activeClusterUID(ctx)
	q := stripReservedScopeLabels(promQL)
	q = scopeQueryByCluster(q, uid)
	q = scopeQueryByTenant(q, metricsTenantPinCtx(ctx))
	// The query is the model's — or, over /mcp, anyone's. The rewriter above
	// is not a boundary for text someone else wrote; VM enforces this filter
	// on the parsed query (vm_read_filter.go).
	if uid == "" {
		uid = noClusterUIDSentinel
	}
	filter := vmReadFilter{tenant: metricsTenantPinCtx(ctx), cluster: uid}

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	params := url.Values{"query": {q}}
	path := "/api/v1/query"
	if start.Before(end) {
		path = "/api/v1/query_range"
		params.Set("start", strconv.FormatInt(start.Unix(), 10))
		params.Set("end", strconv.FormatInt(end.Unix(), 10))
		params.Set("step", strconv.Itoa(int(step.Seconds())))
	} else {
		params.Set("time", strconv.FormatInt(end.Unix(), 10))
	}
	filter.apply(params)
	target, _ := url.Parse(metricsStorageURL() + path)
	target.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build metrics request")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := kobiMetricsClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// Not an outage: this query is too heavy. Say so, or the model
			// reports the metrics store as down.
			return nil, fmt.Errorf("the query took longer than 12s; aggregate it (sum by / topk) or use a shorter range")
		}
		return nil, fmt.Errorf("metrics storage unreachable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, metricsQueryBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read metrics response")
	}
	if len(body) > metricsQueryBodyLimit {
		return nil, fmt.Errorf("the query returned more than %d MB; aggregate it (sum by / topk) or narrow the selector", metricsQueryBodyLimit>>20)
	}
	var vm struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
				Values [][]interface{}   `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &vm); err != nil {
		return nil, fmt.Errorf("parse metrics response")
	}
	if vm.Status != "success" {
		// The store's message names the PromQL problem — useful to the model,
		// and it never carries another org's data.
		return nil, fmt.Errorf("the metrics store rejected the query: %s", vm.Error)
	}
	out := make([]copilot.MetricSeries, 0, len(vm.Data.Result))
	for _, r := range vm.Data.Result {
		labels := make(map[string]string, len(r.Metric))
		for k, v := range r.Metric {
			if k == "cluster_id" || k == TenantIDLabelName {
				continue // scope, not signal — and never shown back
			}
			labels[k] = v
		}
		series := copilot.MetricSeries{Labels: labels}
		if len(r.Value) == 2 {
			if p, ok := vmSample(r.Value); ok {
				series.Points = append(series.Points, p)
			}
		}
		for _, v := range r.Values {
			if p, ok := vmSample(v); ok {
				series.Points = append(series.Points, p)
			}
		}
		out = append(out, series)
	}
	// Largest last value first: with a series cap, the ones that matter most
	// survive the cut.
	sort.SliceStable(out, func(i, j int) bool { return lastValue(out[i]) > lastValue(out[j]) })
	return out, nil
}

func vmSample(v []interface{}) (copilot.MetricSample, bool) {
	if len(v) != 2 {
		return copilot.MetricSample{}, false
	}
	ts, ok := v[0].(float64)
	if !ok {
		return copilot.MetricSample{}, false
	}
	str, ok := v[1].(string)
	if !ok {
		return copilot.MetricSample{}, false
	}
	f, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return copilot.MetricSample{}, false
	}
	return copilot.MetricSample{T: time.Unix(int64(ts), 0).UTC(), V: f}, true
}

func lastValue(s copilot.MetricSeries) float64 {
	if len(s.Points) == 0 {
		return 0
	}
	return s.Points[len(s.Points)-1].V
}

func (h *handlers) metricsQuerySourceFor() copilot.MetricsQuerySource {
	if h.manager == nil {
		return nil
	}
	return metricsQuerySource{h: h}
}
