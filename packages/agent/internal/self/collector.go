// Package self emits kubebolt_agent_* metrics so the agent is observable in
// VictoriaMetrics as a first-class workload. The Collector reads from the
// agent's own ring buffer + Go runtime stats and produces a small fixed set
// of samples each tick — cardinality is exactly len(metrics) per agent
// instance (no per-target labels).
//
// Phase 1 (v1.0) MVP scope:
//   - kubebolt_agent_samples_collected_total  (counter, from buffer.Stats)
//   - kubebolt_agent_samples_dropped_total    (counter, from buffer.Stats)
//   - kubebolt_agent_buffer_size_current      (gauge,   from buffer.Stats)
//   - kubebolt_agent_buffer_size_max          (gauge,   from buffer config)
//   - kubebolt_agent_memory_bytes             (gauge,   runtime.MemStats.Alloc)
//   - kubebolt_agent_goroutines               (gauge,   runtime.NumGoroutine)
//   - kubebolt_agent_info                     (gauge=1, agent_version label)
//
// Deferred to a future iteration (need more code surface to track):
//   - kubebolt_agent_samples_sent_total       (would require shipper instrumentation)
//   - kubebolt_agent_collection_errors_total  (would require collector instrumentation)
//   - kubebolt_agent_cpu_cores                (would require /proc/self/stat polling)
//
// Naming: kubebolt_agent_* prefix follows the convention prometheus_*,
// nginx_* etc. — service-namespaced for clarity. Avoids collision with
// process_* / go_* metrics that vmagent will export when it lands in
// Phase 2 (Universal Data Plane Plan).
package self

import (
	"context"
	"runtime"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

type Collector struct {
	buf          *buffer.Ring
	clusterID    string
	clusterName  string
	nodeName     string
	agentVersion string
	// tenantID stamped on every sample. See collector.CadvisorCollector
	// for the full Day 4.2 semantic.
	tenantID string
}

func New(buf *buffer.Ring, clusterID, clusterName, nodeName, agentVersion, tenantID string) *Collector {
	return &Collector{
		buf:          buf,
		clusterID:    clusterID,
		clusterName:  clusterName,
		nodeName:     nodeName,
		agentVersion: agentVersion,
		tenantID:     tenantID,
	}
}

// Name implements the agent's Collector interface (cmd/agent/main.go).
func (c *Collector) Name() string { return "agent_self" }

// Collect produces the kubebolt_agent_* sample set. The context is
// accepted for interface compatibility but isn't used — the read paths
// (buffer.Stats, runtime.ReadMemStats) are local and bounded.
func (c *Collector) Collect(_ context.Context) ([]*agentv2.Sample, error) {
	collected, dropped, current, capacity := c.buf.Stats()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	now := timestamppb.Now()
	base := c.baseLabels()

	infoLabels := mergeLabels(base, map[string]string{"agent_version": c.agentVersion})

	return []*agentv2.Sample{
		newSample("kubebolt_agent_samples_collected_total", float64(collected), base, now),
		newSample("kubebolt_agent_samples_dropped_total", float64(dropped), base, now),
		newSample("kubebolt_agent_buffer_size_current", float64(current), base, now),
		newSample("kubebolt_agent_buffer_size_max", float64(capacity), base, now),
		newSample("kubebolt_agent_memory_bytes", float64(ms.Alloc), base, now),
		newSample("kubebolt_agent_goroutines", float64(runtime.NumGoroutine()), base, now),
		// kubebolt_agent_info=1 is a "virtual" gauge whose only job is to
		// carry the agent_version label so dashboards can group/filter
		// by version. Standard Prom pattern (see kube_pod_info).
		newSample("kubebolt_agent_info", 1, infoLabels, now),
	}, nil
}

func (c *Collector) baseLabels() map[string]string {
	out := map[string]string{
		"cluster_id": c.clusterID,
		"node":       c.nodeName,
	}
	if c.clusterName != "" {
		out["cluster_name"] = c.clusterName
	}
	if c.tenantID != "" {
		out["tenant_id"] = c.tenantID
	}
	return out
}

// --- helpers ---------------------------------------------------------------

func newSample(name string, value float64, labels map[string]string, ts *timestamppb.Timestamp) *agentv2.Sample {
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
