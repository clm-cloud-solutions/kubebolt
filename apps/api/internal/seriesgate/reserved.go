package seriesgate

import (
	"bytes"
	"strings"
)

// Reserved series: the kubebolt_* families the platform writes itself — the AI
// families it stamps with an org (kubebolt_kobi_*, kubebolt_ai_*,
// kubebolt_autopilot_*, kubebolt_cluster_team_info) and the API's own
// operational series (HTTP, VictoriaMetrics calls, WebSocket, jobs, Postgres
// pool, platform alerts, ingest and agent-channel counters, build info).
//
// A customer's door — Prometheus remote_write or an agent — never writes them.
// If it did:
//
//   - the AI families are server-owned (IsServerOwnedMetricName) and stay out
//     of the active-series cap and the billing count, so a customer could
//     store any number of series for free under those names;
//   - Platform › Operations and the operator's alerts read every kubebolt_*
//     series regardless of tenant (PlatformMetricsFilters), so a forged
//     kubebolt_http_requests_total{code="5xx"} would fire api_5xx.
//
// Both doors drop them, whatever the plan (IsReservedMetricName), and count the
// drop under reason="reserved". The agent's own self-metrics
// (kubebolt_agent_heap_*, kubebolt_agent_info, kubebolt_promread_leader…) are
// not reserved: the agent writes them through the agent door by design.

var reservedPrefixes = []string{
	// Server-owned AI families (ServerOwnedFamiliesRegex).
	"kubebolt_kobi_",
	"kubebolt_autopilot_",
	"kubebolt_ai_",
	// The API's own series.
	"kubebolt_http_",
	"kubebolt_vm_",
	"kubebolt_ws_",
	"kubebolt_job_",
	"kubebolt_pg_pool_",
	"kubebolt_platform_",
	"kubebolt_prom_write_",
	"kubebolt_api_",
	"kubebolt_agent_grpc_",
	"kubebolt_agent_kube_request_",
}

var reservedExact = map[string]struct{}{
	"kubebolt_cluster_team_info": {},
	"kubebolt_build_info":        {},
	"kubebolt_agent_channels":    {},
}

// IsReservedMetricName reports whether name is a family only the platform
// writes. Runs per series on the ingest hot path: a map lookup, then a short
// prefix scan only for kubebolt_* names.
func IsReservedMetricName(name string) bool {
	if !strings.HasPrefix(name, "kubebolt_") {
		return false
	}
	if _, ok := reservedExact[name]; ok {
		return true
	}
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

var reservedTokens = func() [][]byte {
	out := make([][]byte, 0, len(reservedPrefixes)+len(reservedExact))
	for _, p := range reservedPrefixes {
		out = append(out, []byte(p))
	}
	for n := range reservedExact {
		out = append(out, []byte(n))
	}
	return out
}()

// MayHoldReserved is the cheap pre-check on a decoded remote_write payload:
// false means no series in it can carry a reserved name, so the door forwards
// it without walking it. A true is only a maybe — a label value can contain
// the same bytes — and the walk decides.
func MayHoldReserved(payload []byte) bool {
	if !bytes.Contains(payload, []byte("kubebolt_")) {
		return false
	}
	for _, t := range reservedTokens {
		if bytes.Contains(payload, t) {
			return true
		}
	}
	return false
}
