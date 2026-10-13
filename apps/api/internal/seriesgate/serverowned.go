package seriesgate

import "strings"

// Server-owned series: the ones KubeBolt writes into VictoriaMetrics about a
// customer, stamped with its tenant_id (and cluster_id) — the AI observability
// families of doc #67: kubebolt_kobi_copilot_* (the API) and
// kubebolt_kobi_autopilot_* (the Autopilot service). The regex also keeps the
// names they had before the split by mode (kubebolt_kobi_<metric>,
// kubebolt_ai_*, kubebolt_autopilot_*), so a VictoriaMetrics that still holds
// them never counts them. They are KubeBolt's own bookkeeping about the
// customer, not the customer's ingest, so:
//
//   - they stay out of the active-series cap and the billing count while the
//     customer cannot see them (which plan opens them is a product decision);
//   - they stay out of the metrics-only freshness probe, where a Kobi session
//     on a dead cluster would otherwise make it look like it is still shipping.
//
// Every query that counts "the customer's series" adds ExcludeServerOwned.
// The web app mirrors ServerOwnedFamiliesRegex in utils/promql.ts.

// ServerOwnedFamiliesRegex matches the server-owned metric names.
const ServerOwnedFamiliesRegex = `kubebolt_(kobi|autopilot|ai)_.+|kubebolt_cluster_team_info`

// ExcludeServerOwned is the PromQL label matcher that leaves them out; add it
// inside a selector next to the tenant matcher.
const ExcludeServerOwned = `__name__!~"` + ServerOwnedFamiliesRegex + `"`

var serverOwnedPrefixes = []string{"kubebolt_kobi_", "kubebolt_autopilot_", "kubebolt_ai_"}

// IsServerOwnedMetricName reports whether name is a server-owned family —
// the same set as ServerOwnedFamiliesRegex.
func IsServerOwnedMetricName(name string) bool {
	if name == "kubebolt_cluster_team_info" {
		return true
	}
	for _, p := range serverOwnedPrefixes {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			return true
		}
	}
	return false
}
