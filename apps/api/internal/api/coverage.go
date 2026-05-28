package api

import (
	"context"
	"net/http"
)

// CoverageSource describes one observability data source the
// backend can detect by checking for presence of a canonical
// metric in VictoriaMetrics. The UI uses these to render a
// "what's actively shipping samples" banner so operators see
// what they have without grepping logs.
type CoverageSource struct {
	// Name is the operator-facing identifier ("kubebolt-agent",
	// "node-exporter", etc.). Stable; the UI uses it as a key.
	Name string `json:"name"`
	// Probe is the canonical metric we query for presence.
	// Documented so operators can reproduce the check by hand.
	Probe string `json:"probe"`
	// Status is "active" when at least one sample for the probe
	// metric exists within the lookback window for the active
	// cluster, "inactive" otherwise. Phase 2 keeps this binary;
	// Phase 4+ may introduce "stale" / "degraded" states once we
	// have multiple sources of the same metric (e.g. agent +
	// scrape-receiver).
	Status string `json:"status"`
}

// CoverageResponse is the shape returned by GET /api/v1/coverage.
type CoverageResponse struct {
	Sources []CoverageSource `json:"sources"`
	// LookbackMinutes is the window the probes ran against, so
	// the UI can render copy like "last seen ≤ 5m ago".
	LookbackMinutes int `json:"lookbackMinutes"`
}

// coverageProbes maps a source name to the canonical metric whose
// presence proves "this source is actively shipping samples". One
// metric per source — these are the cheapest checks (no aggregation,
// just `count()`) and they're chosen to be the *most likely to be
// the first thing emitted* so a freshly-bootstrapped source flips
// to active fast:
//
//   - kubebolt-agent: kubebolt_agent_info is emitted on every cycle
//     the agent's self-collector runs (Phase 1 Day 4). Faster signal
//     than container_cpu_usage_seconds_total which only appears once
//     a container's stats land.
//   - node-exporter: node_cpu_seconds_total is the canonical
//     node-exporter heartbeat — every node-exporter ships it.
//     Distinct from KubeBolt's node_cpu_usage_seconds_total which
//     is the agent-derived metric; if both are present, both
//     sources are active and we just show both.
//   - kube-state-metrics: kube_pod_status_phase is core KSM (every
//     pod KSM observes has at least one phase series) AND it's part
//     of the curated KSM subset that managed providers ship —
//     GKE's GMP, AMP, Azure Managed Prometheus all include it. The
//     previously-used kube_pod_info is more series-rich but NOT in
//     GMP's curated subset, so the chip would falsely stay inactive
//     on GKE clusters with Mode C reading from GMP. Discovered in
//     session 11-A re-validation 2026-05-27.
//   - hubble: pod_flow_events_total carries the source="hubble"
//     label that distinguishes it from any other future flow source.
var coverageProbes = []struct {
	name  string
	query string
}{
	{
		name:  "kubebolt-agent",
		query: `count(kubebolt_agent_info)`,
	},
	{
		name:  "node-exporter",
		query: `count(node_cpu_seconds_total)`,
	},
	{
		name:  "kubebolt-node-stress",
		// Probes the Mode A NodeStress collector (Fix #10 session 11-A
		// v3). Emits node_load{1,5,15} + node_pressure_*_waiting_seconds_total
		// from /proc/loadavg + /proc/pressure/* directly. Distinct chip
		// from node-exporter because:
		//   - node-exporter chip honestly reports "is there a real
		//     node-exporter source scraped into this VM?"
		//   - kubebolt-node-stress chip reports "is the agent's
		//     in-process /proc reader active?"
		// Both can light up simultaneously when an operator runs both
		// (e.g. kube-prom-stack node-exporter + Mode A); they cover
		// different concerns.
		//
		// The `source="kubebolt-agent"` filter is critical: NodeStress
		// emits standard node-exporter metric names (so UI panels
		// like Node Monitor "Load average" find data regardless of
		// origin). Without the label filter, this probe would match
		// node-exporter's `node_load1` and falsely light up on every
		// cluster running kube-prom-stack — even if Fix #10 isn't
		// deployed there. NodeStress.nodeLabels() stamps the source
		// label specifically to make this discriminator honest.
		// Discovered session 11-A v3 — first version of the probe was
		// just `count(node_load1)` and lit up wrongly on the
		// operator's local kind cluster.
		query: `count(node_load1{source="kubebolt-agent"})`,
	},
	{
		name:  "kube-state-metrics",
		query: `count(kube_pod_status_phase)`,
	},
	{
		name:  "hubble",
		query: `count(pod_flow_events_total{source="hubble"})`,
	},
}

// coverageLookbackMinutes is hard-coded for Phase 2. VictoriaMetrics
// instant queries already use the staleness interval (5 minutes by
// default) to determine whether a series is "live", so a simple
// `count()` call covers the lookback semantics for free — we just
// surface the default in the response for the UI.
const coverageLookbackMinutes = 5

// handleCoverage returns the active scrape-source coverage for the
// current cluster. Read-only, cheap (4 instant queries), safe to
// poll from the UI on a 30-60s tick.
func (h *handlers) handleCoverage(w http.ResponseWriter, r *http.Request) {
	uid := h.activeClusterUID()

	sources := make([]CoverageSource, 0, len(coverageProbes))
	for _, probe := range coverageProbes {
		query := scopeQueryByCluster(probe.query, uid)
		status := coverageStatusForQuery(r.Context(), query)
		sources = append(sources, CoverageSource{
			Name:   probe.name,
			Probe:  probe.query,
			Status: status,
		})
	}

	respondJSON(w, http.StatusOK, CoverageResponse{
		Sources:         sources,
		LookbackMinutes: coverageLookbackMinutes,
	})
}

// coverageStatusForQuery executes the probe and reports whether
// any series came back. Errors (VM unreachable, parse failure) map
// to "inactive" — the operator should see "no signal" rather than
// a misleading "active" when we can't tell.
func coverageStatusForQuery(ctx context.Context, query string) string {
	rows, err := runInstantQuery(ctx, query)
	if err != nil {
		return "inactive"
	}
	for _, row := range rows {
		if row.Value > 0 {
			return "active"
		}
	}
	return "inactive"
}

