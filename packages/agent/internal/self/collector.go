// Package self emits kubebolt_agent_* metrics so the agent is observable in
// VictoriaMetrics as a first-class workload. The Collector reads from the
// agent's own ring buffer + Go runtime stats and produces a small fixed set
// of samples each tick — cardinality is exactly len(metrics) per agent
// instance (no per-target labels).
//
// Originally Phase 1 (v1.0) MVP shipped a small set centered on
// runtime.MemStats.Alloc (live heap). When operators reported
// container_memory_working_set_bytes climbing >>2× Alloc with no
// goroutine or buffer growth, we lacked the gauges to attribute the
// gap. This Collector now emits the full MemStats breakdown so the
// next memory-pressure investigation has the data in-band:
//
// Buffer + runtime basics (existing):
//   - kubebolt_agent_samples_collected_total  (counter, from buffer.Stats)
//   - kubebolt_agent_samples_dropped_total    (counter, from buffer.Stats)
//   - kubebolt_agent_buffer_size_current      (gauge,   from buffer.Stats)
//   - kubebolt_agent_buffer_size_max          (gauge,   from buffer config)
//   - kubebolt_agent_memory_bytes             (gauge,   ms.Alloc — back-compat name)
//   - kubebolt_agent_goroutines               (gauge,   runtime.NumGoroutine)
//   - kubebolt_agent_info                     (gauge=1, agent_version label)
//
// Deep MemStats — added so we can attribute a container working-set gap:
//   - kubebolt_agent_heap_alloc_bytes         (gauge,   ms.HeapAlloc)
//   - kubebolt_agent_heap_sys_bytes           (gauge,   ms.HeapSys — committed for heap)
//   - kubebolt_agent_heap_inuse_bytes         (gauge,   ms.HeapInuse — currently in use)
//   - kubebolt_agent_heap_idle_bytes          (gauge,   ms.HeapIdle — committed, free, could return)
//   - kubebolt_agent_heap_released_bytes      (gauge,   ms.HeapReleased — actually returned to OS)
//   - kubebolt_agent_total_sys_bytes          (gauge,   ms.Sys — TOTAL from OS, all arenas)
//   - kubebolt_agent_stack_sys_bytes          (gauge,   ms.StackSys)
//   - kubebolt_agent_mspan_sys_bytes          (gauge,   ms.MSpanSys)
//   - kubebolt_agent_mcache_sys_bytes         (gauge,   ms.MCacheSys)
//   - kubebolt_agent_other_sys_bytes          (gauge,   ms.OtherSys — off-heap mappings)
//   - kubebolt_agent_gc_num_total             (counter, ms.NumGC)
//   - kubebolt_agent_next_gc_bytes            (gauge,   ms.NextGC — heap size that triggers next GC)
//
// Read these as a diagnostic toolkit:
//   - heap_idle - heap_released = committed, unused, NOT returned → Go runtime retention
//   - total_sys vs container working_set_bytes — if close, attribute is Go runtime;
//     if total_sys << working_set, the gap is OFF the Go runtime (cgo / mmap / kernel buffers)
//   - heap_sys / heap_inuse — fragmentation
//   - next_gc / heap_alloc — GC pressure
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

// AggregatorSizer reports per-map key counts for the Hubble flow
// aggregator. Implemented by *flows.Aggregator's Sizes() method. Used
// to emit kubebolt_agent_aggregator_keys{type=…} gauges.
//
// Interface (instead of importing flows directly) keeps the self
// package light and avoids tying observability to a specific aggregator
// implementation — easy to swap in tests.
type AggregatorSizer interface {
	Sizes() map[string]int
}

// PodsCacheSizer reports the current pod count in the kubelet pod
// cache. Implemented by *collector.PodsCache's Size() method.
type PodsCacheSizer interface {
	Size() int
}

type Collector struct {
	buf          *buffer.Ring
	clusterID    string
	clusterName  string
	nodeName     string
	agentVersion string
	// tenantID stamped on every sample. See collector.CadvisorCollector
	// for the full Day 4.2 semantic.
	tenantID string

	// Optional sources of subsystem-level state size. Both nil-safe —
	// when a subsystem isn't wired in (eg. Hubble disabled) the
	// corresponding gauges are simply not emitted.
	aggregator AggregatorSizer
	podsCache  PodsCacheSizer
}

// Option mutates a Collector at construction time. Functional-options
// pattern so wiring new subsystem sizers (aggregator, pods cache,
// future ones) doesn't break the New() signature for existing callers
// + tests.
type Option func(*Collector)

// WithAggregator wires the Hubble flow aggregator's Sizes() into the
// emitted samples. nil is accepted and skips emission silently —
// matches the Hubble-disabled deployment.
func WithAggregator(a AggregatorSizer) Option {
	return func(c *Collector) { c.aggregator = a }
}

// WithPodsCache wires the kubelet pod cache's Size() into the emitted
// samples. nil-safe (skip emission if not wired).
func WithPodsCache(p PodsCacheSizer) Option {
	return func(c *Collector) { c.podsCache = p }
}

func New(buf *buffer.Ring, clusterID, clusterName, nodeName, agentVersion, tenantID string, opts ...Option) *Collector {
	c := &Collector{
		buf:          buf,
		clusterID:    clusterID,
		clusterName:  clusterName,
		nodeName:     nodeName,
		agentVersion: agentVersion,
		tenantID:     tenantID,
	}
	for _, o := range opts {
		o(c)
	}
	return c
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

	samples := []*agentv2.Sample{
		newSample("kubebolt_agent_samples_collected_total", float64(collected), base, now),
		newSample("kubebolt_agent_samples_dropped_total", float64(dropped), base, now),
		newSample("kubebolt_agent_buffer_size_current", float64(current), base, now),
		newSample("kubebolt_agent_buffer_size_max", float64(capacity), base, now),
		// kubebolt_agent_memory_bytes is the original name (kept for
		// back-compat with pre-existing dashboards). Same value as
		// kubebolt_agent_heap_alloc_bytes below.
		newSample("kubebolt_agent_memory_bytes", float64(ms.Alloc), base, now),
		newSample("kubebolt_agent_goroutines", float64(runtime.NumGoroutine()), base, now),
		// Heap arena breakdown — diagnoses Go-retention vs real growth.
		newSample("kubebolt_agent_heap_alloc_bytes", float64(ms.HeapAlloc), base, now),
		newSample("kubebolt_agent_heap_sys_bytes", float64(ms.HeapSys), base, now),
		newSample("kubebolt_agent_heap_inuse_bytes", float64(ms.HeapInuse), base, now),
		newSample("kubebolt_agent_heap_idle_bytes", float64(ms.HeapIdle), base, now),
		newSample("kubebolt_agent_heap_released_bytes", float64(ms.HeapReleased), base, now),
		// Total Sys is the bytes Go has obtained from the OS across ALL
		// arenas (heap + stack + mspan + mcache + buckhash + gc + other).
		// Compare against container_memory_working_set_bytes: a large
		// gap (working_set >> total_sys) points OUTSIDE the Go runtime
		// — kernel socket buffers, cgo, file mappings.
		newSample("kubebolt_agent_total_sys_bytes", float64(ms.Sys), base, now),
		newSample("kubebolt_agent_stack_sys_bytes", float64(ms.StackSys), base, now),
		newSample("kubebolt_agent_mspan_sys_bytes", float64(ms.MSpanSys), base, now),
		newSample("kubebolt_agent_mcache_sys_bytes", float64(ms.MCacheSys), base, now),
		newSample("kubebolt_agent_other_sys_bytes", float64(ms.OtherSys), base, now),
		// GC scheduler state — high next_gc relative to heap_alloc means
		// GC is postponed, so heap keeps growing before reclaim.
		newSample("kubebolt_agent_gc_num_total", float64(ms.NumGC), base, now),
		newSample("kubebolt_agent_next_gc_bytes", float64(ms.NextGC), base, now),
		// kubebolt_agent_info=1 is a "virtual" gauge whose only job is to
		// carry the agent_version label so dashboards can group/filter
		// by version. Standard Prom pattern (see kube_pod_info).
		newSample("kubebolt_agent_info", 1, infoLabels, now),
	}

	// Subsystem state sizes — appended only when the corresponding
	// source was wired in via Option (Hubble may be disabled; pods
	// cache may not be live yet on a cold start).
	if c.aggregator != nil {
		for kind, n := range c.aggregator.Sizes() {
			labels := mergeLabels(base, map[string]string{"type": kind})
			samples = append(samples, newSample("kubebolt_agent_aggregator_keys", float64(n), labels, now))
		}
	}
	if c.podsCache != nil {
		samples = append(samples, newSample("kubebolt_agent_pods_cache_size", float64(c.podsCache.Size()), base, now))
	}

	return samples, nil
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
