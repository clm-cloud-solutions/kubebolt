// Package metriccatalog says what KubeBolt knows about a metric name: whether
// it is core (the cluster metrics KubeBolt itself produces or reads), part of a
// custom family from the catalog (the metrics of a tool KubeBolt has a use for,
// which an org turns on), or unknown.
//
//   - Core is recognised by EXACT name, never by prefix. With a prefix, any
//     metric renamed kube_myapp_* would pass as core.
//   - A custom family enters Families only once something in KubeBolt reads it
//     (Kobi first, then the rest of the product).
//   - Anything else is unknown: nothing in the product reads it, because Kobi
//     and every screen query exact names.
//
// The API's own series (kubebolt_http_*, kubebolt_kobi_*…) are not listed here:
// only the platform writes them, both ingest doors drop them when anyone else
// sends them, and the API's classifier calls them core before asking this
// package (seriesgate.ClassifyName).
//
// The API and the agent both read this package, linked with `replace` like
// costmetrics, so the classifier, the ingest doors and the agent's filters can
// never disagree.
package metriccatalog

import (
	"sort"

	"github.com/kubebolt/kubebolt/packages/shared/costmetrics"
)

// Class is what a metric name is to KubeBolt.
type Class string

const (
	// Core: the cluster metrics KubeBolt produces or reads. They grow with
	// nodes and pods.
	Core Class = "core"
	// Custom: a family from the catalog, counted apart once an org turns it
	// on.
	Custom Class = "custom"
	// Unknown: neither. Nothing in the product reads it.
	Unknown Class = "unknown"
)

// Family is one custom family of the catalog: the metrics of one tool that
// KubeBolt has a use for.
type Family struct {
	// ID is stable: org settings and usage rows store it.
	ID string
	// Integration is the Integrations card the family belongs to (trivy,
	// kyverno…).
	Integration string
	// Names are the exact metric names, every supported version and known
	// alias included (an OTel-translated name, a rename between releases).
	Names []string
	// Labels are the labels the family needs to be useful. A series without
	// them still counts, but no capability can read it.
	Labels []string
	// Use is what reads it: the Kobi skill, the screen.
	Use string
}

// Families is the custom catalog. Empty until the first family has a use; until
// then every series outside core is unknown.
var Families []Family

// agentNames are what the KubeBolt agent itself emits: kubelet stats, the
// cAdvisor network allowlist, the node readings it synthesises, PVC stats,
// Hubble flows and its own health. packages/agent's metric-name test fails when
// the agent emits a name missing here.
var agentNames = []string{
	// Containers (kubelet /stats/summary and cAdvisor).
	"container_cpu_usage_seconds_total",
	"container_cpu_usage_cores",
	"container_memory_failures_total",
	"container_memory_major_page_faults_total",
	"container_memory_page_faults_total",
	"container_memory_rss",
	"container_memory_rss_bytes",
	"container_memory_usage_bytes",
	"container_memory_working_set_bytes",
	"container_network_receive_bytes_total",
	"container_network_receive_errors_total",
	"container_network_receive_packets_dropped_total",
	"container_network_transmit_bytes_total",
	"container_network_transmit_errors_total",
	"container_network_transmit_packets_dropped_total",
	// Nodes, as the agent names them (they differ from node-exporter's).
	"node_cpu_usage_cores",
	"node_cpu_usage_seconds_total",
	"node_fs_capacity_bytes",
	"node_fs_used_bytes",
	"node_load1",
	"node_load5",
	"node_load15",
	"node_memory_available_bytes",
	"node_memory_usage_bytes",
	"node_memory_working_set_bytes",
	"node_network_receive_bytes_total",
	"node_network_transmit_bytes_total",
	"node_pressure_cpu_waiting_seconds_total",
	"node_pressure_io_waiting_seconds_total",
	"node_pressure_memory_waiting_seconds_total",
	// Volumes.
	"kubelet_volume_stats_available_bytes",
	"kubelet_volume_stats_capacity_bytes",
	"kubelet_volume_stats_inodes",
	"kubelet_volume_stats_inodes_free",
	"kubelet_volume_stats_inodes_used",
	"kubelet_volume_stats_used_bytes",
	// Hubble flows.
	"hubble_collector_up",
	"pod_dns_resolutions_total",
	"pod_flow_events_total",
	"pod_flow_external_events_total",
	"pod_flow_http_latency_seconds_count",
	"pod_flow_http_latency_seconds_sum",
	"pod_flow_http_requests_total",
	// The agent's own health.
	"kubebolt_agent_aggregator_keys",
	"kubebolt_agent_buffer_size_current",
	"kubebolt_agent_buffer_size_max",
	"kubebolt_agent_gc_num_total",
	"kubebolt_agent_goroutines",
	"kubebolt_agent_heap_alloc_bytes",
	"kubebolt_agent_heap_idle_bytes",
	"kubebolt_agent_heap_inuse_bytes",
	"kubebolt_agent_heap_released_bytes",
	"kubebolt_agent_heap_sys_bytes",
	"kubebolt_agent_info",
	"kubebolt_agent_mcache_sys_bytes",
	"kubebolt_agent_memory_bytes",
	"kubebolt_agent_mspan_sys_bytes",
	"kubebolt_agent_next_gc_bytes",
	"kubebolt_agent_other_sys_bytes",
	"kubebolt_agent_pods_cache_size",
	"kubebolt_agent_samples_collected_total",
	"kubebolt_agent_samples_dropped_total",
	"kubebolt_agent_stack_sys_bytes",
	"kubebolt_agent_total_sys_bytes",
	"kubebolt_flow_collector_leader",
	"kubebolt_promread_leader",
}

// nodeExporterNames are the node-exporter metrics KubeBolt asks for or reads:
// promread's default matchers, plus the ones the Node views read when a
// scrape of node-exporter provides them. The rest of node-exporter is unknown
// until a screen reads it.
var nodeExporterNames = []string{
	"node_cpu_seconds_total",
	"node_disk_io_time_seconds_total",
	"node_disk_read_bytes_total",
	"node_disk_written_bytes_total",
	"node_filesystem_avail_bytes",
	"node_filesystem_size_bytes",
	"node_load1",
	"node_load5",
	"node_load15",
	"node_network_receive_errs_total",
	"node_network_transmit_errs_total",
	"node_pressure_cpu_waiting_seconds_total",
	"node_pressure_io_waiting_seconds_total",
	"node_pressure_memory_waiting_seconds_total",
}

// targetNames are the per-target health series every Prometheus scrape adds,
// and the standard process collector of a scraped exporter. promread asks for
// up and two process series; the rest share the names, so they are the same
// series whoever scrapes them.
var targetNames = []string{
	"up",
	"process_cpu_seconds_total",
	"process_max_fds",
	"process_network_receive_bytes_total",
	"process_network_transmit_bytes_total",
	"process_open_fds",
	"process_resident_memory_bytes",
	"process_start_time_seconds",
	"process_virtual_memory_bytes",
	"process_virtual_memory_max_bytes",
}

var (
	core     = map[string]struct{}{}
	inFamily = map[string]string{}
)

func init() {
	for _, list := range [][]string{agentNames, nodeExporterNames, targetNames, ksmNames, costmetrics.Families} {
		for _, n := range list {
			core[n] = struct{}{}
		}
	}
	for _, f := range Families {
		for _, n := range f.Names {
			inFamily[n] = f.ID
		}
	}
}

// Classify says what a metric name is, and for a custom name, which family it
// belongs to. Runs per series on the ingest and metering paths: map lookups
// only.
func Classify(name string) (Class, string) {
	if _, ok := core[name]; ok {
		return Core, ""
	}
	if id, ok := inFamily[name]; ok {
		return Custom, id
	}
	return Unknown, ""
}

// IsCore reports whether name is a core metric.
func IsCore(name string) bool {
	_, ok := core[name]
	return ok
}

// CoreNames returns every core name, sorted.
func CoreNames() []string {
	out := make([]string, 0, len(core))
	for n := range core {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
