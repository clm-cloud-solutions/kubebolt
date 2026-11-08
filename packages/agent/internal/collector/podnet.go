package collector

import (
	"strings"
	"sync/atomic"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// PodNetworkSeen hands the cAdvisor collector the pod network samples the
// stats collector sent in its last pass. Both read the counters of the same
// network namespace, but their label sets never match (stats carries pod_uid
// and, after PodsCache.Enrich, the workload labels; cAdvisor carries
// neither), so VictoriaMetrics kept both copies and every sum came out
// doubled. cAdvisor now sends only what stats did not: each
// (metric, namespace, pod, interface) reaches the store once.
//
// What stats did not send keeps cAdvisor's copy:
//   - packets_dropped, which /stats/summary has no field for;
//   - a pod stats has not reported, either short-lived (it ended between two
//     passes) or on a kubelet that leaves the summary's network block empty;
//   - an interface stats did not list. With an empty interfaces[] it reports
//     only the pod's default interface (asFallbackInterface), so a second
//     one (e.g. Multus net1) exists only in cAdvisor. The key is per
//     interface, never per pod: a second interface is real traffic.
//
// The set is swapped whole and read lock-free. A nil *PodNetworkSeen is
// valid and records nothing, so cAdvisor sends everything.
type PodNetworkSeen struct {
	set atomic.Pointer[map[podNetKey]struct{}]
}

type podNetKey struct {
	metric, namespace, pod, iface string
}

// NewPodNetworkSeen returns an empty set: until the first stats pass,
// cAdvisor skips nothing.
func NewPodNetworkSeen() *PodNetworkSeen { return &PodNetworkSeen{} }

// record publishes the pod network samples of one stats pass. A failed pass
// records nil: it reported nothing, so cAdvisor has nothing to skip.
func (s *PodNetworkSeen) record(samples []*agentv2.Sample) {
	if s == nil {
		return
	}
	var set map[podNetKey]struct{}
	for _, sm := range samples {
		if !strings.HasPrefix(sm.MetricName, "container_network_") {
			continue
		}
		if set == nil {
			set = make(map[podNetKey]struct{})
		}
		set[podNetKey{sm.MetricName, sm.Labels["namespace"], sm.Labels["pod"], sm.Labels["interface"]}] = struct{}{}
	}
	s.set.Store(&set)
}

// has reports whether the last stats pass sent this metric for this pod
// interface.
func (s *PodNetworkSeen) has(metric, namespace, pod, iface string) bool {
	if s == nil {
		return false
	}
	set := s.set.Load()
	if set == nil {
		return false
	}
	_, ok := (*set)[podNetKey{metric, namespace, pod, iface}]
	return ok
}
