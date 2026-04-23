package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
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
