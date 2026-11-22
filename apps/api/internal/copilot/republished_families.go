package copilot

import (
	"fmt"
	"regexp"
	"strings"
)

// republishedFamilies are the kube-state-metrics families that the OpenCost the
// agent chart bundles also publishes, with `source="opencost"`, measured on
// yagan on 2026-11-21 (finding #102). Where a cluster sends both, each object
// has two series with equal values, and a sum or count over the family counts
// it twice. The value is the label set that names one object: the `max by` that
// keeps one copy.
var republishedFamilies = map[string]string{
	"kube_pod_container_resource_requests":                       "namespace, pod, container",
	"kube_pod_container_status_running":                          "namespace, pod, container",
	"kube_pod_container_status_terminated_reason":                "namespace, pod, container",
	"kube_pod_labels":                                            "namespace, pod",
	"kube_pod_owner":                                             "namespace, pod",
	"kube_node_status_allocatable":                               "node",
	"kube_node_status_capacity":                                  "node",
	"kube_node_labels":                                           "node",
	"kube_namespace_labels":                                      "namespace",
	"kube_persistentvolumeclaim_info":                            "namespace, persistentvolumeclaim",
	"kube_persistentvolumeclaim_resource_requests_storage_bytes": "namespace, persistentvolumeclaim",
	"kube_persistentvolume_capacity_bytes":                       "persistentvolume",
	"kube_job_status_failed":                                     "namespace, job_name",
}

var (
	promMetricName = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)
	// The `source` label itself, not `resource`, which every requests query has.
	sourceLabel = regexp.MustCompile(`\bsource\b`)
)

// republishedFamilyNote is the caution query_metrics attaches when a query reads
// one of those families without keeping one copy per object: no `max by`, and
// no `source` that tells the two apart. It rides the tool's output and not its
// description, so it costs nothing on the turns that never touch these families.
func republishedFamilyNote(promQL string) string {
	if strings.Contains(promQL, "max by") || sourceLabel.MatchString(promQL) {
		return ""
	}
	for _, name := range promMetricName.FindAllString(promQL, -1) {
		identity, ok := republishedFamilies[name]
		if !ok {
			continue
		}
		return fmt.Sprintf("%s can reach the store twice with equal values, from kube-state-metrics and from OpenCost (source=\"opencost\"). "+
			"Where a cluster sends both, a sum or count over it counts each object twice. Keep one per object first: "+
			"sum(max by (%s) (%s{…})).", name, identity, name)
	}
	return ""
}
