// Package costmetrics is the one list of OpenCost families KubeBolt reads, for
// every consumer: the API's core/custom classifier (seriesgate), the agent's
// OpenCost exporter filter and promread's cost matchers (finding #101). The
// agent chart's scrape allowlist is YAML and cannot import it; a test there
// reads values.yaml against this list.
//
// Exact names only, no prefixes: promread sends them as explicit matchers (a
// regex matcher is not portable across managed Prometheus services), and a
// prefix such as `kubecost_` would take in families nobody reads
// (`kubecost_http_*`, OpenCost's own server metrics).
//
// A family enters the list when a view, rule or tool reads it. The GPU
// families (`node_gpu_hourly_cost`, `container_gpu_allocation`) stay out until
// the product does: on a node without a GPU they are a series per container
// that always reads zero.
package costmetrics

import "strings"

// Families are the OpenCost families the product reads.
var Families = []string{
	// Node prices: the headline spend and the per-container attribution.
	"node_total_hourly_cost",
	"node_cpu_hourly_cost",
	"node_ram_hourly_cost",
	// What each container is charged for.
	"container_cpu_allocation",
	"container_memory_allocation_bytes",
	// Volumes, and which pod holds each claim.
	"pv_hourly_cost",
	"pod_pvc_allocation",
	// Spot nodes.
	"kubecost_node_is_spot",
	// Network: load balancers and egress.
	"kubecost_load_balancer_cost",
	"kubecost_network_internet_egress_cost",
	"kubecost_network_nat_gateway_egress_cost",
	"kubecost_network_region_egress_cost",
	"kubecost_network_zone_egress_cost",
}

var families = func() map[string]struct{} {
	m := make(map[string]struct{}, len(Families))
	for _, f := range Families {
		m[f] = struct{}{}
	}
	return m
}()

// IsCostFamily reports whether a metric name is one of the cost families.
func IsCostFamily(name string) bool {
	_, ok := families[name]
	return ok
}

// KeepFromOpenCost reports whether a series OpenCost published is worth
// keeping: a cost family, or one of the kube-state-metrics-compatible `kube_*`
// families it also publishes. Those stay because the product reads some of
// them (Kobi's request and limit lines, the metrics-only overview) and, in a
// cluster without kube-state-metrics, OpenCost is their only source.
// Everything else it publishes (`deployment_match_labels`, `pod_info`,
// `kubecost_http_*`, the GPU families…) is read by nothing.
func KeepFromOpenCost(name string) bool {
	return IsCostFamily(name) || strings.HasPrefix(name, "kube_")
}
