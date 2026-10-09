package agent

import (
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"

	"github.com/kubebolt/kubebolt/apps/api/internal/seriesgate"
)

// dropReserved removes the samples whose name only KubeBolt writes about
// itself (seriesgate.IsReservedMetricName), like the remote_write door: they
// feed the Health views and the alert rules, and a copy arriving through an
// agent would double them. The agent's own self-metrics (kubebolt_agent_*,
// kubebolt_promread_leader…) are not reserved. Filters in place.
func dropReserved(samples []*agentv2.Sample) (kept []*agentv2.Sample, dropped int) {
	kept = samples[:0]
	for _, sm := range samples {
		if seriesgate.IsReservedMetricName(sm.GetMetricName()) {
			dropped++
			continue
		}
		kept = append(kept, sm)
	}
	return kept, dropped
}
