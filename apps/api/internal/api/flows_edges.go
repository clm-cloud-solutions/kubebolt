package api

import (
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
	SrcNamespace string  `json:"srcNamespace"`
	SrcPod       string  `json:"srcPod"`
	DstNamespace string  `json:"dstNamespace"`
	DstPod       string  `json:"dstPod"`
	Verdict      string  `json:"verdict"`
	RatePerSec   float64 `json:"ratePerSec"`
}

// FlowEdgesResponse wraps the edge list with window metadata so the UI
// can label the chart ("last 5m") without guessing.
type FlowEdgesResponse struct {
	Edges          []FlowEdge `json:"edges"`
	WindowMinutes  int        `json:"windowMinutes"`
	Source         string     `json:"source"` // always "hubble" for now
}

// handleFlowEdges executes the PromQL that aggregates pod_flow_events_total
// into per-pair rates over the requested window and shapes the result as
// FlowEdgesResponse. Params:
//
//   window=5m   (default 5, accepts plain minutes as integer)
//   namespace=<ns>  (optional — filters both src and dst to that namespace)
//
// The window is capped at 60 minutes to keep cardinality reasonable.
func (h *handlers) handleFlowEdges(w http.ResponseWriter, r *http.Request) {
	windowMin := 5
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

	var selector string
	if ns != "" {
		// Match either side in the requested namespace. Intra-namespace
		// traffic lights up both halves of the OR naturally.
		selector = fmt.Sprintf(`{src_namespace=%q,source="hubble"} or pod_flow_events_total{dst_namespace=%q,source="hubble"}`, ns, ns)
	} else {
		selector = `{source="hubble"}`
	}
	query := fmt.Sprintf(
		`sum by (src_namespace, src_pod, dst_namespace, dst_pod, verdict) (rate(pod_flow_events_total%s[%dm]))`,
		selector, windowMin,
	)

	target, _ := url.Parse(metricsStorageURL() + "/api/v1/query")
	params := url.Values{"query": {query}}
	target.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "build upstream request")
		return
	}
	req.Header.Set("Accept", "application/json")

	resp, err := metricsHTTPClient.Do(req)
	if err != nil {
		respondError(w, http.StatusBadGateway, "metrics storage unreachable")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		respondError(w, http.StatusBadGateway, "read upstream body")
		return
	}
	if resp.StatusCode >= 300 {
		// Pass VM's error through to make debugging queries painless.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
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
		respondError(w, http.StatusBadGateway, "parse upstream body: "+err.Error())
		return
	}

	edges := make([]FlowEdge, 0, len(vmResp.Data.Result))
	for _, r := range vmResp.Data.Result {
		rateStr, ok := r.Value[1].(string)
		if !ok {
			continue
		}
		rate, err := strconv.ParseFloat(rateStr, 64)
		if err != nil {
			continue
		}
		edges = append(edges, FlowEdge{
			SrcNamespace: r.Metric["src_namespace"],
			SrcPod:       r.Metric["src_pod"],
			DstNamespace: r.Metric["dst_namespace"],
			DstPod:       r.Metric["dst_pod"],
			Verdict:      r.Metric["verdict"],
			RatePerSec:   rate,
		})
	}

	respondJSON(w, http.StatusOK, FlowEdgesResponse{
		Edges:         edges,
		WindowMinutes: windowMin,
		Source:        "hubble",
	})
}
