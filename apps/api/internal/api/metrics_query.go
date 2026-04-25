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
// scopeQueryByCluster fails closed on empty by injecting a sentinel
// that no real cluster would ever emit, so an unknown UID returns
// zero series rather than leaking data from other clusters that
// happen to share the same VM.
func (h *handlers) activeClusterUID() string {
	conn := h.manager.Connector()
	if conn == nil {
		return ""
	}
	return conn.ClusterUID()
}

// noClusterUIDSentinel is used in place of a real kube-system UID
// when the backend couldn't read one (RBAC, slow EKS auth, partial
// connection). Real UIDs are 36-char lowercase hex with dashes; this
// value is intentionally not a valid UID so it can never collide.
const noClusterUIDSentinel = "__kubebolt_no_uid__"

// metricSelectorRE matches PromQL label selectors — the `{...}` chunk
// that follows a metric name or appears bare (e.g. `{source="hubble"}`).
// The simple `\{([^}]*)\}` pattern is enough because none of our query
// shapes include nested braces; label values can contain them in
// principle but all of ours are plain identifiers.
var metricSelectorRE = regexp.MustCompile(`\{([^}]*)\}`)

// bareMetricRE matches metric references that follow our agent's
// naming convention (one of these prefixes + `_` + body). We use an
// explicit prefix list rather than a generic identifier match so we
// don't accidentally inject selectors into PromQL keywords (sum, rate,
// by, …) or into labels passed to clauses like `by(...)`. Extend the
// list when a new metric family ships from the agent.
//
// Identifiers used as label names elsewhere — `cluster_id`, `pod_name`,
// `pod_namespace`, `pod_uid` — would also match this regex, but step 2
// of scopeQueryByCluster skips text inside `{...}` braces so labels in
// existing selectors are left alone. Labels passed to `by(...)` /
// `without(...)` clauses are NOT inside braces; today none of those
// labels in our queries match this pattern, but new code must keep
// that invariant or scope its query manually.
var bareMetricRE = regexp.MustCompile(
	`\b(?:node|pod|container|kubebolt|hubble)_[a-zA-Z0-9_]+\b`,
)

// scopeQueryByCluster injects `cluster_id="<uid>"` into every metric
// reference in a PromQL expression so a query can't accidentally sum
// series from other clusters that happen to report to the same VM.
// Does nothing when uid is empty (backend couldn't discover the UID,
// e.g. dev-mode without in-cluster creds). Idempotent: existing
// `cluster_id` matchers are left alone.
//
// Two passes:
//
//  1. Existing `{...}` selectors get `cluster_id` prepended (or are
//     skipped if they already have one). Handles `metric{a="b"}` and
//     bare label sets like `{source="hubble"}`.
//  2. Bare metric references with no selector get a fresh
//     `{cluster_id="..."}` appended. Handles `sum(node_cpu_usage_cores)`
//     and `rate(node_network_total[1m])` — query shapes used by
//     OverviewPage and NodesPage that have no `{...}` chunk for pass 1
//     to find. Pass 2 walks the string honoring `{...}` boundaries so
//     label names inside selectors aren't mistaken for metrics.
//
// Regex-based rather than a real PromQL parser because our query shapes
// are stable and simple. If we ever need multi-cluster aggregation or
// more complex expressions, switch to a proper AST rewrite.
func scopeQueryByCluster(promQL, uid string) string {
	if uid == "" {
		// Fail closed: an unknown UID becomes a sentinel that no real
		// agent would ever emit, so the query returns 0 series instead
		// of leaking data from other clusters in the same VM. Don't
		// short-circuit return — we still need both passes to inject
		// the sentinel everywhere, otherwise bare metrics slip through.
		uid = noClusterUIDSentinel
	}
	injected := fmt.Sprintf(`cluster_id=%q`, uid)

	// Pass 1: existing `{...}` selectors.
	promQL = metricSelectorRE.ReplaceAllStringFunc(promQL, func(sel string) string {
		inner := sel[1 : len(sel)-1]
		if strings.Contains(inner, "cluster_id") {
			return sel
		}
		if strings.TrimSpace(inner) == "" {
			return "{" + injected + "}"
		}
		return "{" + injected + "," + inner + "}"
	})

	// Pass 2: bare metric references. Split into "outside-braces" and
	// "inside-braces" regions so identifiers within selectors (label
	// names) aren't rewritten. We pass `nextChar` into injectBareMetrics
	// so a metric that sits right at a chunk boundary followed by `{`
	// (i.e. pass 1 just rewrote its selector) doesn't get a duplicate
	// selector appended.
	var out strings.Builder
	out.Grow(len(promQL) + 32)
	depth := 0
	chunkStart := 0
	for i := 0; i < len(promQL); i++ {
		c := promQL[i]
		if c == '{' {
			if depth == 0 {
				out.WriteString(injectBareMetrics(promQL[chunkStart:i], injected, '{'))
				chunkStart = i
			}
			depth++
		} else if c == '}' {
			if depth > 0 {
				depth--
				if depth == 0 {
					out.WriteString(promQL[chunkStart : i+1])
					chunkStart = i + 1
				}
			}
		}
	}
	if depth == 0 {
		out.WriteString(injectBareMetrics(promQL[chunkStart:], injected, 0))
	} else {
		// Unbalanced braces — shouldn't happen for valid PromQL; emit
		// the trailing chunk verbatim and let VM surface the parse
		// error.
		out.WriteString(promQL[chunkStart:])
	}
	return out.String()
}

// injectBareMetrics finds bare metric references in s and appends a
// `{cluster_id="..."}` selector to each. A reference that's already
// followed by `{` (either inside s or at the chunk boundary via
// nextChar) is left as-is — pass 1 already handled the selector in
// that case.
func injectBareMetrics(s, injected string, nextChar byte) string {
	matches := bareMetricRE.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + len(matches)*(len(injected)+2))
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		sb.WriteString(s[last:start])
		sb.WriteString(s[start:end])
		var follow byte
		if end < len(s) {
			follow = s[end]
		} else {
			follow = nextChar
		}
		if follow != '{' {
			sb.WriteString("{" + injected + "}")
		}
		last = end
	}
	sb.WriteString(s[last:])
	return sb.String()
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
