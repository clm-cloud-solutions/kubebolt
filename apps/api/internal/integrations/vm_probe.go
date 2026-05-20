package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// VMProbeClient is a tiny VictoriaMetrics query client tailored to
// the integrations package's needs. The full metrics-query proxy
// lives in the api package, but spinning that whole thing up for a
// 1-line `count(...)` would be overkill — and creates a circular
// dependency (api imports integrations). This local helper keeps
// the dependency direction one-way.
//
// Returns nil for a request error (network / 5xx / malformed
// response). Returns the parsed count when VM answers a scalar
// `count()` query; missing-vector or 0-row vectors are surfaced
// as count == 0, which lets the caller treat them as "no
// presence" without special-casing.
type VMProbeClient struct {
	baseURL string
	http    *http.Client
}

// NewVMProbeClient builds a probe client targeting the given VM
// base URL (e.g. "http://localhost:8428"). Trailing slash is
// tolerated. Pass a shared http.Client for connection reuse — when
// nil, a default with a short timeout is created (5s feels right
// for a probe; we don't want it gating the integrations card on
// slow VM responses).
func NewVMProbeClient(baseURL string, httpClient *http.Client) *VMProbeClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &VMProbeClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    httpClient,
	}
}

// CountQuery executes a PromQL `count(...)` (or similar scalar-shape)
// query against VM and returns the count value. A query that returns
// no rows (the metric doesn't exist) → (0, nil). A failed query →
// (0, err) — caller decides whether to retry or fall back.
func (c *VMProbeClient) CountQuery(ctx context.Context, promql string) (int, error) {
	if c.baseURL == "" {
		return 0, fmt.Errorf("vm probe: no base URL configured")
	}
	target, err := url.Parse(c.baseURL + "/api/v1/query")
	if err != nil {
		return 0, fmt.Errorf("vm probe: parse url: %w", err)
	}
	q := target.Query()
	q.Set("query", promql)
	target.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("vm probe: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vm probe: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("vm probe: status %d", resp.StatusCode)
	}

	// VM /api/v1/query response shape — scalar-shaped queries
	// (count, sum) come back as `vector` with one row whose value
	// is a [unix_ts, "stringified_number"] pair. Empty vector
	// (no matching series) is the "absence" answer.
	var body struct {
		Data struct {
			Result []struct {
				Value [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("vm probe: decode: %w", err)
	}
	if len(body.Data.Result) == 0 {
		return 0, nil
	}
	// Value[1] is the numeric portion, serialized as a JSON string
	// in the Prom-compatible API. Unquote then parse.
	var raw string
	if err := json.Unmarshal(body.Data.Result[0].Value[1], &raw); err != nil {
		return 0, fmt.Errorf("vm probe: unmarshal value: %w", err)
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("vm probe: parse value %q: %w", raw, err)
	}
	return int(n), nil
}

// PromSamplesForCluster is the shape main.go wires into the
// Prometheus provider's promSampleProbe. Confirms VM holds at least
// one series of `up` tagged with the given cluster_id — i.e. a
// Prometheus scraper has been emitting samples for that cluster.
//
// Why `up` specifically: it's the canonical per-scrape-target gauge
// that every healthy Prom emits, and the kubebolt-agent gRPC channel
// does NOT emit it (the agent ships cAdvisor / kubelet / Hubble, not
// scrape-up). So presence of `up{cluster_id="<X>"}` cleanly attributes
// to "Prometheus shipping for cluster X" rather than the agent.
func (c *VMProbeClient) PromSamplesForCluster(ctx context.Context, clusterID string) (bool, error) {
	if clusterID == "" {
		// Without a cluster UID we can't form a safe query — the
		// caller's filter is already a no-op in that case, so the
		// probe's answer doesn't change anything either way.
		// Return "confirmed" so the legacy behavior keeps working.
		return true, nil
	}
	q := fmt.Sprintf(`count(up{cluster_id=%q})`, clusterID)
	n, err := c.CountQuery(ctx, q)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
