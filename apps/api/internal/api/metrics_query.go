package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// metricsStorageURL returns the backing VictoriaMetrics (or any
// Prometheus-compatible) endpoint, configurable via env. Falls back to the
// Docker Compose service DNS and then to localhost for bare-host dev.
func metricsStorageURL() string {
	if u := os.Getenv("KUBEBOLT_METRICS_STORAGE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8428"
}

var metricsHTTPClient = &http.Client{Timeout: 15 * time.Second}

// activeClusterUID returns the kube-system UID of the cluster this
// handler is currently pointed at, or empty when no connector is
// available (startup before first connect, or connection errored).
// Empty disables query scoping entirely — callers treat that as a
// best-effort "query whatever's in VM" which is the pre-scoping
// behavior.
func (h *handlers) activeClusterUID() string {
	conn := h.manager.Connector()
	if conn == nil {
		return ""
	}
	return conn.ClusterUID()
}

// metricSelectorRE matches PromQL label selectors — the `{...}` chunk
// that follows a metric name or appears bare (e.g. `{source="hubble"}`).
// The simple `\{([^}]*)\}` pattern is enough because none of our query
// shapes include nested braces; label values can contain them in
// principle but all of ours are plain identifiers.
var metricSelectorRE = regexp.MustCompile(`\{([^}]*)\}`)

// scopeQueryByCluster injects `cluster_id="<uid>"` into every label
// selector in a PromQL expression so a query can't accidentally sum
// series from other clusters that happen to report to the same VM.
// Does nothing when uid is empty (backend couldn't discover the UID,
// e.g. dev-mode without in-cluster creds). Idempotent: if a selector
// already has a cluster_id matcher, it's left alone.
//
// Regex-based rather than a real PromQL parser because our query shapes
// are stable and simple (`metric{...}`, possibly wrapped in sum/rate).
// If we ever need multi-cluster aggregation or more complex expressions,
// switch to a proper AST rewrite.
func scopeQueryByCluster(promQL, uid string) string {
	if uid == "" {
		return promQL
	}
	injected := fmt.Sprintf(`cluster_id=%q`, uid)
	return metricSelectorRE.ReplaceAllStringFunc(promQL, func(sel string) string {
		inner := sel[1 : len(sel)-1]
		if strings.Contains(inner, "cluster_id") {
			return sel
		}
		if strings.TrimSpace(inner) == "" {
			return "{" + injected + "}"
		}
		return "{" + injected + "," + inner + "}"
	})
}

// handleMetricsQueryRange proxies a PromQL range query to the TSDB.
//
// Query params (all required):
//
//	query  — PromQL expression
//	start  — RFC3339 or Unix seconds
//	end    — RFC3339 or Unix seconds
//	step   — Prometheus duration string (e.g. 15s, 1m)
//
// The response is VM's native JSON, returned verbatim. Content-Type is
// forced to application/json since we trust the TSDB response.
func (h *handlers) handleMetricsQueryRange(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	step := r.URL.Query().Get("step")

	if q == "" || start == "" || end == "" || step == "" {
		respondError(w, http.StatusBadRequest, "query, start, end, and step are all required")
		return
	}

	q = scopeQueryByCluster(q, h.activeClusterUID())

	target, err := url.Parse(metricsStorageURL() + "/api/v1/query_range")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "invalid storage URL")
		return
	}
	params := url.Values{}
	params.Set("query", q)
	params.Set("start", start)
	params.Set("end", end)
	params.Set("step", step)
	target.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "build upstream request")
		return
	}
	req.Header.Set("Accept", "application/json")

	resp, err := metricsHTTPClient.Do(req)
	if err != nil {
		slog.Warn("tsdb query failed", slog.String("error", err.Error()))
		respondError(w, http.StatusBadGateway, "metrics storage unreachable")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		respondError(w, http.StatusBadGateway, "read upstream body")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(body); err != nil {
		slog.Debug("metrics response write", slog.String("error", err.Error()))
	}
}

// handleMetricsQuery proxies a PromQL instant query. Used for single-point
// lookups (current value rather than a time range).
func (h *handlers) handleMetricsQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	if q == "" {
		respondError(w, http.StatusBadRequest, "query is required")
		return
	}
	q = scopeQueryByCluster(q, h.activeClusterUID())
	target, _ := url.Parse(metricsStorageURL() + "/api/v1/query")
	params := url.Values{"query": {q}}
	if t := r.URL.Query().Get("time"); t != "" {
		params.Set("time", t)
	}
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
