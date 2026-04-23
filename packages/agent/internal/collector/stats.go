// Package collector contains the kubelet-reading data collectors.
//
// StatsCollector hits kubelet /stats/summary and emits per-container +
// per-volume + per-node samples. The kubelet response is a superset of what
// we surface: this MVP covers CPU, memory, network, filesystem. Detailed
// throttle / swap / page-fault metrics that live in cAdvisor's /metrics
// endpoint are a Phase-C addition.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

type StatsCollector struct {
	client    *kubelet.Client
	clusterID string
	nodeName  string
}

func NewStats(client *kubelet.Client, clusterID, nodeName string) *StatsCollector {
	return &StatsCollector{client: client, clusterID: clusterID, nodeName: nodeName}
}

func (c *StatsCollector) Name() string { return "kubelet_stats_summary" }

// Collect returns all samples produced by a single /stats/summary poll.
// The sample list is unenriched; the caller is expected to pass it through
// the pods metadata cache before shipping.
func (c *StatsCollector) Collect(ctx context.Context) ([]*agentv1.Sample, error) {
	body, err := c.client.Get(ctx, "/stats/summary")
	if err != nil {
		return nil, err
	}
	var summary statsSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, fmt.Errorf("decode stats/summary: %w", err)
	}

	now := timestamppb.Now()
	nodeLabels := map[string]string{
		"cluster_id": c.clusterID,
		"node":       c.nodeName,
	}

	var samples []*agentv1.Sample

	// Node-level metrics.
	if summary.Node.CPU != nil {
		if v := summary.Node.CPU.UsageNanoCores; v != nil {
			samples = append(samples, gauge("node_cpu_usage_cores", float64(*v)/1e9, nodeLabels, now))
		}
		if v := summary.Node.CPU.UsageCoreNanoSeconds; v != nil {
			samples = append(samples, counter("node_cpu_usage_seconds_total", float64(*v)/1e9, nodeLabels, now))
		}
	}
	if summary.Node.Memory != nil {
		if v := summary.Node.Memory.WorkingSetBytes; v != nil {
			samples = append(samples, gauge("node_memory_working_set_bytes", float64(*v), nodeLabels, now))
		}
		if v := summary.Node.Memory.AvailableBytes; v != nil {
			samples = append(samples, gauge("node_memory_available_bytes", float64(*v), nodeLabels, now))
		}
		if v := summary.Node.Memory.UsageBytes; v != nil {
			samples = append(samples, gauge("node_memory_usage_bytes", float64(*v), nodeLabels, now))
		}
	}
	if summary.Node.FS != nil {
		if v := summary.Node.FS.UsedBytes; v != nil {
			samples = append(samples, gauge("node_fs_used_bytes", float64(*v), nodeLabels, now))
		}
		if v := summary.Node.FS.CapacityBytes; v != nil {
			samples = append(samples, gauge("node_fs_capacity_bytes", float64(*v), nodeLabels, now))
		}
	}
	for _, iface := range summary.Node.Network.Interfaces {
		ifaceLabels := mergeLabels(nodeLabels, map[string]string{"interface": iface.Name})
		if iface.RxBytes != nil {
			samples = append(samples, counter("node_network_receive_bytes_total", float64(*iface.RxBytes), ifaceLabels, now))
		}
		if iface.TxBytes != nil {
			samples = append(samples, counter("node_network_transmit_bytes_total", float64(*iface.TxBytes), ifaceLabels, now))
		}
	}

	// Per-pod, per-container metrics.
	for _, pod := range summary.Pods {
		podLabels := map[string]string{
			"cluster_id":    c.clusterID,
			"node":          c.nodeName,
			"pod_namespace": pod.PodRef.Namespace,
			"pod_name":      pod.PodRef.Name,
			"pod_uid":       pod.PodRef.UID,
		}

		for _, container := range pod.Containers {
			containerLabels := mergeLabels(podLabels, map[string]string{"container": container.Name})
			if container.CPU != nil {
				if v := container.CPU.UsageNanoCores; v != nil {
					samples = append(samples, gauge("container_cpu_usage_cores", float64(*v)/1e9, containerLabels, now))
				}
				if v := container.CPU.UsageCoreNanoSeconds; v != nil {
					samples = append(samples, counter("container_cpu_usage_seconds_total", float64(*v)/1e9, containerLabels, now))
				}
			}
			if container.Memory != nil {
				if v := container.Memory.WorkingSetBytes; v != nil {
					samples = append(samples, gauge("container_memory_working_set_bytes", float64(*v), containerLabels, now))
				}
				if v := container.Memory.RSSBytes; v != nil {
					samples = append(samples, gauge("container_memory_rss_bytes", float64(*v), containerLabels, now))
				}
				if v := container.Memory.UsageBytes; v != nil {
					samples = append(samples, gauge("container_memory_usage_bytes", float64(*v), containerLabels, now))
				}
				if v := container.Memory.PageFaults; v != nil {
					samples = append(samples, counter("container_memory_page_faults_total", float64(*v), containerLabels, now))
				}
				if v := container.Memory.MajorPageFaults; v != nil {
					samples = append(samples, counter("container_memory_major_page_faults_total", float64(*v), containerLabels, now))
				}
			}
		}

		// Per-pod network is a sum of its network namespace interfaces.
		for _, iface := range pod.Network.Interfaces {
			ifaceLabels := mergeLabels(podLabels, map[string]string{"interface": iface.Name})
			if iface.RxBytes != nil {
				samples = append(samples, counter("pod_network_receive_bytes_total", float64(*iface.RxBytes), ifaceLabels, now))
			}
			if iface.TxBytes != nil {
				samples = append(samples, counter("pod_network_transmit_bytes_total", float64(*iface.TxBytes), ifaceLabels, now))
			}
			if iface.RxErrors != nil {
				samples = append(samples, counter("pod_network_receive_errors_total", float64(*iface.RxErrors), ifaceLabels, now))
			}
			if iface.TxErrors != nil {
				samples = append(samples, counter("pod_network_transmit_errors_total", float64(*iface.TxErrors), ifaceLabels, now))
			}
		}

		// Per-pod volumes.
		for _, vol := range pod.Volumes {
			volLabels := mergeLabels(podLabels, map[string]string{"volume": vol.Name})
			if vol.PVCRef != nil {
				volLabels["pvc_name"] = vol.PVCRef.Name
			}
			if vol.UsedBytes != nil {
				samples = append(samples, gauge("pod_volume_used_bytes", float64(*vol.UsedBytes), volLabels, now))
			}
			if vol.CapacityBytes != nil {
				samples = append(samples, gauge("pod_volume_capacity_bytes", float64(*vol.CapacityBytes), volLabels, now))
			}
			if vol.AvailableBytes != nil {
				samples = append(samples, gauge("pod_volume_available_bytes", float64(*vol.AvailableBytes), volLabels, now))
			}
		}
	}

	return samples, nil
}

// --- wire types matching the subset of /stats/summary we use ----------------

type statsSummary struct {
	Node nodeStats  `json:"node"`
	Pods []podStats `json:"pods"`
}

type nodeStats struct {
	NodeName string        `json:"nodeName"`
	CPU      *cpuStats     `json:"cpu"`
	Memory   *memoryStats  `json:"memory"`
	Network  networkStats  `json:"network"`
	FS       *fsStats      `json:"fs"`
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

type networkStats struct {
	Interfaces []netInterface `json:"interfaces"`
}

type netInterface struct {
	Name     string  `json:"name"`
	RxBytes  *uint64 `json:"rxBytes"`
	RxErrors *uint64 `json:"rxErrors"`
	TxBytes  *uint64 `json:"txBytes"`
	TxErrors *uint64 `json:"txErrors"`
}

type fsStats struct {
	AvailableBytes *uint64 `json:"availableBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	UsedBytes      *uint64 `json:"usedBytes"`
	InodesFree     *uint64 `json:"inodesFree"`
	InodesUsed     *uint64 `json:"inodesUsed"`
}

type volumeStats struct {
	Name           string  `json:"name"`
	UsedBytes      *uint64 `json:"usedBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	AvailableBytes *uint64 `json:"availableBytes"`
	PVCRef         *pvcRef `json:"pvcRef"`
}

type pvcRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// --- sample helpers ---------------------------------------------------------

func gauge(name string, value float64, labels map[string]string, ts *timestamppb.Timestamp) *agentv1.Sample {
	return &agentv1.Sample{
		Timestamp:  ts,
		MetricName: name,
		Value:      value,
		Labels:     labels,
	}
}

func counter(name string, value float64, labels map[string]string, ts *timestamppb.Timestamp) *agentv1.Sample {
	// Counters and gauges share the same wire shape; type is known server-side
	// by metric name. Kept as a separate helper for call-site clarity.
	return &agentv1.Sample{
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
