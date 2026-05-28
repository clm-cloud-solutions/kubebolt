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
// one series of `prometheus_build_info` tagged with the given
// cluster_id — i.e. a Prometheus has been remote_write-ing samples
// for that cluster.
//
// Why `prometheus_build_info` specifically: it's the Prom self-metric
// every Prometheus instance emits (gauge always = 1, labeled with
// version, revision, branch, goversion). Critically:
//
//   - Mode A (kubebolt-agent DaemonSet) does NOT emit it — the agent
//     ships cAdvisor / kubelet / Hubble, not Prom self-metrics.
//   - Mode C (kubebolt-agent promread Deployment) does NOT pull it
//     either — the chart's default matchers are surgical and don't
//     include `prometheus_build_info`. An operator overriding
//     matchers to a very broad regex MIGHT pull it; that's an
//     operator-opt-in trade-off, not a default false-positive.
//
// So presence of `prometheus_build_info{cluster_id="<X>"}` cleanly
// attributes to "an actual Prometheus instance has remote_write-pushed
// samples for cluster X" — which is what the Prometheus (push)
// integration card is meant to detect.
//
// History (session 11-A 2026-05-27): this used to query `up`, which
// worked when Mode A was the only agent topology (Mode A doesn't ship
// `up`). When Mode C landed in 1.13 with default matchers that
// include `up` (so the operator's UI panels can see scrape health
// across the customer's Prom targets), `up` started appearing in VM
// from the agent rather than from a real Prom — and the Prom (push)
// card falsely lit up as "Streaming" on every cluster running Mode C
// with the default matchers. `prometheus_build_info` doesn't have
// this collision because it's not in any chart default.
func (c *VMProbeClient) PromSamplesForCluster(ctx context.Context, clusterID string) (bool, error) {
	if clusterID == "" {
		// Without a cluster UID we can't form a safe query — the
		// caller's filter is already a no-op in that case, so the
		// probe's answer doesn't change anything either way.
		// Return "confirmed" so the legacy behavior keeps working.
		return true, nil
	}
	q := fmt.Sprintf(`count(prometheus_build_info{cluster_id=%q})`, clusterID)
	n, err := c.CountQuery(ctx, q)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// AgentSamplesForCluster is the shape main.go wires into the Agent
// integration's samplesProbe. Confirms VM holds at least one
// `kubebolt_agent_info` series tagged with the given cluster_id —
// i.e. the kubebolt-agent installed in that cluster is actually
// streaming to THIS backend.
//
// Why `kubebolt_agent_info`: it's the canonical "I am here and alive"
// gauge that the agent's self-collector emits every cycle (stamped
// with cluster_id from the agent's resolved kube-system UID + tenant
// info). Presence of this series for cluster X means "the agent in
// cluster X is shipping to this VM". Absence means "no agent samples
// for that cluster are arriving here, even if the operator's
// kubeconfig sees the agent's DaemonSet in that cluster" — the agent
// is configured to ship elsewhere (different backend).
//
// Discovered session 11-A v3 — see
// project_agent_cross_backend_false_positive for the operator-facing
// symptom this probe addresses.
func (c *VMProbeClient) AgentSamplesForCluster(ctx context.Context, clusterID string) (bool, error) {
	if clusterID == "" {
		// Without a cluster UID we can't form a safe scoped query.
		// Caller treats nil/zero as "skipped" — keep legacy behavior.
		return true, nil
	}
	q := fmt.Sprintf(`count(kubebolt_agent_info{cluster_id=%q})`, clusterID)
	n, err := c.CountQuery(ctx, q)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// PromreadActiveForCluster is the shape main.go wires into the
// Prometheus (read) provider. Confirms that at least one
// kubebolt-agent promread pod currently holds the kubebolt-promread
// Lease for the given cluster_id — i.e. Mode C is wired and the
// leader is actively polling the customer's Prom.
//
// Signal: `count(kubebolt_promread_leader{cluster_id="<X>"} == 1)`.
// The gauge is emitted continuously by every promread pod (0 for
// followers, 1 for the leader). Steady-state should always be 1;
// transient 0 during pod restarts / lease handover; >1 only during
// the brief split-brain window the Lease itself closes within
// LeaseDuration. So "> 0" is the right "active" predicate.
//
// Cluster-scoping is hard-required: without it, every cluster's card
// would light up the moment ANY cluster onboarded Mode C — the gauge
// stream is global. cluster_id == "" returns (false, nil) so the
// provider can branch on Unknown rather than the operator being told
// it's installed when no UID exists to verify.
func (c *VMProbeClient) PromreadActiveForCluster(ctx context.Context, clusterID string) (bool, error) {
	if clusterID == "" {
		return false, nil
	}
	q := fmt.Sprintf(`count(kubebolt_promread_leader{cluster_id=%q} == 1)`, clusterID)
	n, err := c.CountQuery(ctx, q)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
