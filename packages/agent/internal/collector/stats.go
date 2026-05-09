// Package collector contains the kubelet-reading data collectors.
//
// StatsCollector hits kubelet /stats/summary and emits per-container +
// per-pod-network + per-volume + per-node samples. The kubelet response is a
// superset of what we surface: this collector covers CPU, memory, network,
// filesystem. Detailed throttle / swap / page-fault metrics that live in
// cAdvisor's /metrics endpoint are emitted by CadvisorCollector.
//
// Schema: as of v1.0 (Universal Data Plane Plan, Phase 1) this collector
// emits Prometheus-canonical metric names and labels — cAdvisor convention
// for container metrics, kubelet_volume_stats_* for volumes, node-exporter
// convention for node network. The dual-source pattern (stats.go + cadvisor.go
// both emit container_network_*) relies on VM collapsing duplicate samples
// when the label set matches; cadvisor.go is the authoritative source on
// kubelets where the /stats/summary network block is empty.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

type StatsCollector struct {
	client      *kubelet.Client
	clusterID   string
	clusterName string
	nodeName    string
	// deferNodeNetwork suppresses emission of node_network_receive_bytes_total
	// and node_network_transmit_bytes_total — the only two node-scoped
	// metrics whose names overlap exactly with what node-exporter emits.
	// When the agent is deployed alongside a vmagent sidecar that
	// scrapes node-exporter (Phase 2 of the Universal Data Plane Plan),
	// keeping the agent's emission would pile a third copy of the same
	// series into VictoriaMetrics, producing the 3× overcount on
	// `sum(rate(node_network_*[1m]))` queries surfaced during in-vivo
	// validation. Other node-* metrics from the agent
	// (node_cpu_usage_seconds_total, node_memory_working_set_bytes,
	// node_fs_used_bytes, etc.) have DIFFERENT names from node-exporter
	// and don't overlap — they keep emitting unconditionally, including
	// some that node-exporter has no equivalent for (working_set_bytes
	// is kubelet-derived from cgroup accounting). The flag is
	// surgical, scoped only to the names that actually collide.
	deferNodeNetwork bool
}

// StatsOption is a functional option for NewStats.
type StatsOption func(*StatsCollector)

// WithDeferNodeNetwork suppresses node_network_*_bytes_total emission.
// Wired by the helm chart when the vmagent sidecar is configured to
// scrape node-exporter; node-exporter emits the same metric name with
// the same labels, so the agent steps aside to avoid double-counting.
func WithDeferNodeNetwork(defer_ bool) StatsOption {
	return func(c *StatsCollector) { c.deferNodeNetwork = defer_ }
}

func NewStats(client *kubelet.Client, clusterID, clusterName, nodeName string, opts ...StatsOption) *StatsCollector {
	c := &StatsCollector{
		client:      client,
		clusterID:   clusterID,
		clusterName: clusterName,
		nodeName:    nodeName,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *StatsCollector) Name() string { return "kubelet_stats_summary" }

// nodeLabels returns the base label set for any node-scoped metric.
func (c *StatsCollector) nodeLabels() map[string]string {
	labels := map[string]string{
		"cluster_id": c.clusterID,
		"node":       c.nodeName,
	}
	if c.clusterName != "" {
		labels["cluster_name"] = c.clusterName
	}
	return labels
}

// podLabels returns the base label set for any pod-scoped metric. Container
// scope is added by the caller via mergeLabels.
//
// pod_uid is preserved as a label because the PodsCache enrichment path joins
// stats samples by pod UID to attach workload + label metadata. It's not
// strictly part of cAdvisor canonical labels but coexists fine.
func (c *StatsCollector) podLabels(ns, name, uid string) map[string]string {
	labels := c.nodeLabels()
	labels["namespace"] = ns
	labels["pod"] = name
	labels["pod_uid"] = uid
	return labels
}

// Collect returns all samples produced by a single /stats/summary poll.
// The sample list is unenriched; the caller is expected to pass it through
// the pods metadata cache before shipping.
func (c *StatsCollector) Collect(ctx context.Context) ([]*agentv2.Sample, error) {
	body, err := c.client.Get(ctx, "/stats/summary")
	if err != nil {
		return nil, err
	}
	var summary statsSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, fmt.Errorf("decode stats/summary: %w", err)
	}

	now := timestamppb.Now()
	var samples []*agentv2.Sample

	samples = append(samples, c.collectNode(&summary, now)...)
	for i := range summary.Pods {
		samples = append(samples, c.collectPod(&summary.Pods[i], now)...)
	}

	return samples, nil
}

// collectNode emits node-scoped metrics. Naming follows node-exporter
// convention where applicable (network device label), kubelet-derived
// metrics keep their own names but are marked for Phase 2 deprecation in
// favor of node-exporter scrape via the vmagent sidecar.
func (c *StatsCollector) collectNode(s *statsSummary, ts *timestamppb.Timestamp) []*agentv2.Sample {
	base := c.nodeLabels()
	var samples []*agentv2.Sample

	if s.Node.CPU != nil {
		// node_cpu_usage_cores (gauge derived from rate) was removed in
		// v1.0 — backend derives via PromQL `rate(*_seconds_total[5m])`.
		if v := s.Node.CPU.UsageCoreNanoSeconds; v != nil {
			samples = append(samples, sample("node_cpu_usage_seconds_total", float64(*v)/1e9, base, ts))
		}
	}
	if s.Node.Memory != nil {
		if v := s.Node.Memory.WorkingSetBytes; v != nil {
			samples = append(samples, sample("node_memory_working_set_bytes", float64(*v), base, ts))
		}
		if v := s.Node.Memory.AvailableBytes; v != nil {
			samples = append(samples, sample("node_memory_available_bytes", float64(*v), base, ts))
		}
		if v := s.Node.Memory.UsageBytes; v != nil {
			samples = append(samples, sample("node_memory_usage_bytes", float64(*v), base, ts))
		}
	}
	if s.Node.FS != nil {
		if v := s.Node.FS.UsedBytes; v != nil {
			samples = append(samples, sample("node_fs_used_bytes", float64(*v), base, ts))
		}
		if v := s.Node.FS.CapacityBytes; v != nil {
			samples = append(samples, sample("node_fs_capacity_bytes", float64(*v), base, ts))
		}
	}

	// Node network: aligns with node-exporter convention (label `device`).
	// Skipped entirely when the deployer wired WithDeferNodeNetwork —
	// node-exporter emits the same metric name with identical labels
	// from the same kernel counters, so two emitters produce a 2×
	// (or higher, with annotation re-discovery) overcount on
	// `sum(rate(node_network_*[1m]))` queries.
	if !c.deferNodeNetwork {
		nodeIfaces := s.Node.Network.Interfaces
		if len(nodeIfaces) == 0 {
			if fb, ok := s.Node.Network.asFallbackInterface(); ok {
				nodeIfaces = []netInterface{fb}
			}
		}
		for _, iface := range nodeIfaces {
			ifaceLabels := mergeLabels(base, map[string]string{"device": iface.Name})
			if iface.RxBytes != nil {
				samples = append(samples, sample("node_network_receive_bytes_total", float64(*iface.RxBytes), ifaceLabels, ts))
			}
			if iface.TxBytes != nil {
				samples = append(samples, sample("node_network_transmit_bytes_total", float64(*iface.TxBytes), ifaceLabels, ts))
			}
		}
	}
	return samples
}

// collectPod emits all metrics for a single pod from /stats/summary:
// per-container CPU + memory, per-pod-interface network (with container=""
// per cAdvisor pod-level convention), per-volume kubelet_volume_stats_*.
func (c *StatsCollector) collectPod(p *podStats, ts *timestamppb.Timestamp) []*agentv2.Sample {
	podBase := c.podLabels(p.PodRef.Namespace, p.PodRef.Name, p.PodRef.UID)
	var samples []*agentv2.Sample

	for i := range p.Containers {
		samples = append(samples, c.collectContainer(podBase, &p.Containers[i], ts)...)
	}

	// Pod-level network. cAdvisor convention emits these as
	// container_network_* with container="" (the pod's network namespace
	// is owned by the pause container, which cAdvisor reports with empty
	// container label). Same convention here so VM aggregates duplicates
	// from stats.go and cadvisor.go cleanly when both fire.
	netLabels := mergeLabels(podBase, map[string]string{"container": ""})
	podIfaces := p.Network.Interfaces
	if len(podIfaces) == 0 {
		if fb, ok := p.Network.asFallbackInterface(); ok {
			podIfaces = []netInterface{fb}
		}
	}
	for _, iface := range podIfaces {
		ifaceLabels := mergeLabels(netLabels, map[string]string{"interface": iface.Name})
		if iface.RxBytes != nil {
			samples = append(samples, sample("container_network_receive_bytes_total", float64(*iface.RxBytes), ifaceLabels, ts))
		}
		if iface.TxBytes != nil {
			samples = append(samples, sample("container_network_transmit_bytes_total", float64(*iface.TxBytes), ifaceLabels, ts))
		}
		if iface.RxErrors != nil {
			samples = append(samples, sample("container_network_receive_errors_total", float64(*iface.RxErrors), ifaceLabels, ts))
		}
		if iface.TxErrors != nil {
			samples = append(samples, sample("container_network_transmit_errors_total", float64(*iface.TxErrors), ifaceLabels, ts))
		}
	}

	// Volumes: only PVCs land here per kubelet canonical convention.
	// EmptyDir, configMap, secret, and similar volumes don't get
	// kubelet_volume_stats_* metrics in mainstream Prom dashboards. The
	// `volume` (kubelet's internal name) label is dropped —
	// `persistentvolumeclaim` is the canonical key.
	for i := range p.Volumes {
		vol := &p.Volumes[i]
		if vol.PVCRef == nil || vol.PVCRef.Name == "" {
			continue
		}
		volLabels := mergeLabels(podBase, map[string]string{
			"persistentvolumeclaim": vol.PVCRef.Name,
		})
		if vol.UsedBytes != nil {
			samples = append(samples, sample("kubelet_volume_stats_used_bytes", float64(*vol.UsedBytes), volLabels, ts))
		}
		if vol.CapacityBytes != nil {
			samples = append(samples, sample("kubelet_volume_stats_capacity_bytes", float64(*vol.CapacityBytes), volLabels, ts))
		}
		if vol.AvailableBytes != nil {
			samples = append(samples, sample("kubelet_volume_stats_available_bytes", float64(*vol.AvailableBytes), volLabels, ts))
		}
		if vol.Inodes != nil {
			samples = append(samples, sample("kubelet_volume_stats_inodes", float64(*vol.Inodes), volLabels, ts))
		}
		if vol.InodesUsed != nil {
			samples = append(samples, sample("kubelet_volume_stats_inodes_used", float64(*vol.InodesUsed), volLabels, ts))
		}
		if vol.InodesFree != nil {
			samples = append(samples, sample("kubelet_volume_stats_inodes_free", float64(*vol.InodesFree), volLabels, ts))
		}
	}

	return samples
}

// collectContainer emits per-container CPU + memory metrics. The gauge
// `container_cpu_usage_cores` (formerly emitted as a derived value) was
// removed in v1.0 — consumers compute CPU rate via PromQL on the seconds
// counter, which is the cAdvisor-canonical pattern.
func (c *StatsCollector) collectContainer(podBase map[string]string, container *containerStats, ts *timestamppb.Timestamp) []*agentv2.Sample {
	labels := mergeLabels(podBase, map[string]string{"container": container.Name})
	var samples []*agentv2.Sample

	if container.CPU != nil {
		if v := container.CPU.UsageCoreNanoSeconds; v != nil {
			samples = append(samples, sample("container_cpu_usage_seconds_total", float64(*v)/1e9, labels, ts))
		}
	}
	if container.Memory != nil {
		if v := container.Memory.WorkingSetBytes; v != nil {
			samples = append(samples, sample("container_memory_working_set_bytes", float64(*v), labels, ts))
		}
		if v := container.Memory.RSSBytes; v != nil {
			// _bytes suffix dropped in v1.0 to align with cAdvisor canonical
			// (container_memory_rss, not container_memory_rss_bytes).
			samples = append(samples, sample("container_memory_rss", float64(*v), labels, ts))
		}
		if v := container.Memory.UsageBytes; v != nil {
			samples = append(samples, sample("container_memory_usage_bytes", float64(*v), labels, ts))
		}
		// Page faults: in v1.0 the two separate metrics
		// (container_memory_page_faults_total, *_major_page_faults_total)
		// collapse into the cAdvisor-canonical container_memory_failures_total
		// with `failure_type` and `scope` labels.
		if v := container.Memory.PageFaults; v != nil {
			pfLabels := mergeLabels(labels, map[string]string{
				"failure_type": "pgfault",
				"scope":        "container",
			})
			samples = append(samples, sample("container_memory_failures_total", float64(*v), pfLabels, ts))
		}
		if v := container.Memory.MajorPageFaults; v != nil {
			pfLabels := mergeLabels(labels, map[string]string{
				"failure_type": "pgmajfault",
				"scope":        "container",
			})
			samples = append(samples, sample("container_memory_failures_total", float64(*v), pfLabels, ts))
		}
	}
	return samples
}

// --- wire types matching the subset of /stats/summary we use ----------------

type statsSummary struct {
	Node nodeStats  `json:"node"`
	Pods []podStats `json:"pods"`
}

type nodeStats struct {
	NodeName string       `json:"nodeName"`
	CPU      *cpuStats    `json:"cpu"`
	Memory   *memoryStats `json:"memory"`
	Network  networkStats `json:"network"`
	FS       *fsStats     `json:"fs"`
}

type podStats struct {
	PodRef     podRef           `json:"podRef"`
	Containers []containerStats `json:"containers"`
	Network    networkStats     `json:"network"`
	Volumes    []volumeStats    `json:"volume"`
}

type podRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type containerStats struct {
	Name   string       `json:"name"`
	CPU    *cpuStats    `json:"cpu"`
	Memory *memoryStats `json:"memory"`
}

type cpuStats struct {
	Time                 time.Time `json:"time"`
	UsageNanoCores       *uint64   `json:"usageNanoCores"`
	UsageCoreNanoSeconds *uint64   `json:"usageCoreNanoSeconds"`
}

type memoryStats struct {
	Time            time.Time `json:"time"`
	UsageBytes      *uint64   `json:"usageBytes"`
	WorkingSetBytes *uint64   `json:"workingSetBytes"`
	RSSBytes        *uint64   `json:"rssBytes"`
	AvailableBytes  *uint64   `json:"availableBytes"`
	PageFaults      *uint64   `json:"pageFaults"`
	MajorPageFaults *uint64   `json:"majorPageFaults"`
}

// networkStats matches the kubelet stats/summary shape. Besides the
// per-interface breakdown, the block carries top-level fields for the
// default interface (usually eth0). Some kubelets (notably docker-desktop
// for pod-level stats) populate only the top-level fields and leave
// interfaces[] empty — we treat that as a fallback so metrics land either
// way.
type networkStats struct {
	Name       string         `json:"name"`
	RxBytes    *uint64        `json:"rxBytes"`
	RxErrors   *uint64        `json:"rxErrors"`
	TxBytes    *uint64        `json:"txBytes"`
	TxErrors   *uint64        `json:"txErrors"`
	Interfaces []netInterface `json:"interfaces"`
}

type netInterface struct {
	Name     string  `json:"name"`
	RxBytes  *uint64 `json:"rxBytes"`
	RxErrors *uint64 `json:"rxErrors"`
	TxBytes  *uint64 `json:"txBytes"`
	TxErrors *uint64 `json:"txErrors"`
}

// asFallbackInterface projects the top-level network block into a single
// "default" interface entry. Used when interfaces[] is empty.
func (n networkStats) asFallbackInterface() (netInterface, bool) {
	if n.RxBytes == nil && n.TxBytes == nil && n.RxErrors == nil && n.TxErrors == nil {
		return netInterface{}, false
	}
	name := n.Name
	if name == "" {
		name = "eth0"
	}
	return netInterface{
		Name:     name,
		RxBytes:  n.RxBytes,
		TxBytes:  n.TxBytes,
		RxErrors: n.RxErrors,
		TxErrors: n.TxErrors,
	}, true
}

type fsStats struct {
	AvailableBytes *uint64 `json:"availableBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	UsedBytes      *uint64 `json:"usedBytes"`
	InodesFree     *uint64 `json:"inodesFree"`
	InodesUsed     *uint64 `json:"inodesUsed"`
}

// volumeStats matches the kubelet stats/summary volume entry. The kubelet
// embeds FsStats here, which gives us inode counts in addition to bytes.
type volumeStats struct {
	Name           string  `json:"name"`
	UsedBytes      *uint64 `json:"usedBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	AvailableBytes *uint64 `json:"availableBytes"`
	Inodes         *uint64 `json:"inodes"`
	InodesFree     *uint64 `json:"inodesFree"`
	InodesUsed     *uint64 `json:"inodesUsed"`
	PVCRef         *pvcRef `json:"pvcRef"`
}

type pvcRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// --- sample helpers ---------------------------------------------------------

// sample builds an agentv2.Sample with the given metric name, value, and
// labels. Counter vs gauge is resolved server-side by metric name lookup;
// not transmitted on the wire.
func sample(name string, value float64, labels map[string]string, ts *timestamppb.Timestamp) *agentv2.Sample {
	return &agentv2.Sample{
		Timestamp:  ts,
		MetricName: name,
		Value:      value,
		Labels:     labels,
	}
}

func mergeLabels(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
