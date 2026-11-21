package agent

import (
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
	"github.com/kubebolt/kubebolt/packages/shared/costmetrics"
)

// openCostSource is the `source` label the agent's exporter reader stamps on
// what it reads from OpenCost: the bundled chart lists it as `opencost=<url>`
// and a customer's own OpenCost goes under `collectors.exporters.opencost`.
const openCostSource = "opencost"

// dropOpenCostUnread removes what the agent read from OpenCost and the product
// never reads (costmetrics.KeepFromOpenCost: `deployment_match_labels`,
// `pod_info`, `kubecost_http_*`, the GPU families…), for every tenant and plan
// (finding #101). Agents up to 1.4.9 forward everything OpenCost publishes
// but Go runtime metrics, about half of it unread; the agent filters it at the
// source from the next release, and this keeps the server right for the agents
// nobody upgrades. Only samples with `source="opencost"` are touched: a name
// arriving by any other path is the customer's, not OpenCost's noise.
// Filters in place.
func dropOpenCostUnread(samples []*agentv2.Sample) (kept []*agentv2.Sample, dropped int) {
	kept = samples[:0]
	for _, sm := range samples {
		if sm.GetLabels()["source"] == openCostSource && !costmetrics.KeepFromOpenCost(sm.GetMetricName()) {
			dropped++
			continue
		}
		kept = append(kept, sm)
	}
	return kept, dropped
}
