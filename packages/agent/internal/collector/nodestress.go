package collector

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// NodeStressCollector reads kernel-level node stress signals directly
// from /proc on the host: load averages (`/proc/loadavg`) and Pressure
// Stall Information (`/proc/pressure/{cpu,memory,io}`). Emitted with
// the standard node-exporter metric names so the UI's Node Monitor
// panels query a single name regardless of whether the data came from
// (a) this in-agent collector, or (b) Mode C reading an external
// Prometheus that scrapes node-exporter.
//
// Why this exists: managed Prometheus offerings differ in node-exporter
// coverage. AWS AMP and Azure AMW have it baked in (operators install
// kube-prom-stack including node-exporter by default). GKE's Managed
// Prometheus does NOT scrape node-exporter out of the box — only
// kubelet + cadvisor + a curated KSM subset. Under Mode C+GMP the
// UI's load-average and PSI panels stayed empty even though our default
// matchers asked GMP for those metrics; GMP returned empty because the
// data wasn't there to begin with. Adding this collector to Mode A
// (which runs as a DaemonSet on every node) means the panels work
// out of the box on every managed-Prom flavor, no operator action.
//
// Discovered in session 11-A v2 re-validation 2026-05-27 — see
// project_gmp_no_node_exporter for full diagnosis.
//
// Both /proc/loadavg and /proc/pressure/* are system-wide (NOT
// namespaced by PID namespace), so reading them from inside the
// agent container returns host values without any hostPath mount.
// Verified on containerd-on-GKE; should hold on every standard
// runtime. Gvisor / Kata sandboxes that virtualize /proc would
// surface their own (possibly zero) values — operators running
// those should disable this collector (env var below).
//
// /proc/pressure/* requires Linux kernel ≥ 4.20 (PSI feature
// gate). Older kernels lack the directory entirely; the collector
// degrades gracefully — load averages still emit, PSI metrics are
// silently skipped.
type NodeStressCollector struct {
	clusterID   string
	clusterName string
	nodeName    string
	tenantID    string

	// procPath is the filesystem root for /proc reads. Defaults to
	// "/proc" (works inside any container with default runtime).
	// Test seam: override to a fixture directory.
	procPath string

	// disabled short-circuits Collect to a no-op, used when the
	// operator's cluster ALSO runs a node-exporter scrape (kube-prom-
	// stack default). Without the toggle, two sources would emit
	// `node_load1` etc. with overlapping series → UI panels would
	// double-count (same pattern as the existing `node_network_*`
	// dedup via StatsCollector.deferNodeNetwork). Wired by the chart
	// via KUBEBOLT_AGENT_DEFER_NODE_STRESS env var.
	disabled bool
}

// NodeStressOption is a functional option for NewNodeStress.
type NodeStressOption func(*NodeStressCollector)

// WithDeferNodeStress disables the collector entirely. Use when a
// separate node-exporter source (vmagent scraping kube-prom-stack,
// PodMonitoring CR on managed Prom, etc.) is already shipping
// node_load* and node_pressure_* — keeping the agent's emission
// would pile a duplicate series into VictoriaMetrics with overlapping
// labels and the UI's Node Monitor panels would render both lines.
//
// The flag default is false (collector active) because the most
// common topology — GKE Standard + GMP, or self-managed Prom without
// node-exporter — has NO other source for these metrics, and the
// panels would stay empty without our emission. Operators on
// kube-prom-stack flip this to true via the chart's
// scrape.deferNodeStress value (mirrors scrape.deferNodeNetwork).
func WithDeferNodeStress(defer_ bool) NodeStressOption {
	return func(c *NodeStressCollector) { c.disabled = defer_ }
}

// NewNodeStress builds the collector. procPath="" defaults to "/proc".
func NewNodeStress(clusterID, clusterName, nodeName, tenantID, procPath string, opts ...NodeStressOption) *NodeStressCollector {
	if procPath == "" {
		procPath = "/proc"
	}
	c := &NodeStressCollector{
		clusterID:   clusterID,
		clusterName: clusterName,
		nodeName:    nodeName,
		tenantID:    tenantID,
		procPath:    procPath,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *NodeStressCollector) Name() string { return "node_stress" }

func (c *NodeStressCollector) nodeLabels() map[string]string {
	labels := map[string]string{
		"cluster_id": c.clusterID,
		"node":       c.nodeName,
		// `source="kubebolt-agent"` is the discriminator that lets
		// the backend's coverage probe tell our samples apart from
		// node-exporter's. We emit standard node-exporter metric
		// names (`node_load1`, `node_pressure_*`...) so the UI's
		// Node Monitor panel finds our data with the same query it
		// uses for real node-exporter. But that means a probe like
		// `count(node_load1)` matches BOTH sources and can't tell
		// them apart — so the kubebolt-node-stress coverage chip
		// would falsely light up on clusters that have node-exporter
		// (e.g. kube-prom-stack) but DON'T have Fix #10 running.
		// node-exporter doesn't stamp `source=...` by default;
		// adding it here makes the probe `count(node_load1{source="kubebolt-agent"})`
		// honest about who is actually emitting. Other label
		// selectors in the UI panels are unaffected — Prometheus
		// label matchers ignore unspecified labels.
		"source": "kubebolt-agent",
	}
	if c.clusterName != "" {
		labels["cluster_name"] = c.clusterName
	}
	if c.tenantID != "" {
		labels["tenant_id"] = c.tenantID
	}
	return labels
}

// Collect reads /proc/loadavg and the PSI files, returning samples.
// Errors from individual files are non-fatal — we degrade gracefully
// when the kernel lacks PSI (no /proc/pressure dir) or when load
// reading itself fails. A completely unreadable /proc returns the
// load error so the caller's log captures the root cause.
//
// When disabled (operator opted-out via WithDeferNodeStress, e.g.
// because kube-prom-stack node-exporter is already shipping these),
// returns no samples and no error — silent no-op.
func (c *NodeStressCollector) Collect(_ context.Context) ([]*agentv2.Sample, error) {
	if c.disabled {
		return nil, nil
	}
	now := timestamppb.Now()
	base := c.nodeLabels()
	var samples []*agentv2.Sample
	var firstErr error

	if loads, err := c.readLoadAvg(); err == nil {
		samples = append(samples,
			sample("node_load1", loads[0], base, now),
			sample("node_load5", loads[1], base, now),
			sample("node_load15", loads[2], base, now),
		)
	} else if firstErr == nil {
		firstErr = err
	}

	// PSI — best-effort, kernel ≥ 4.20. Skipped entirely on older
	// kernels (directory doesn't exist).
	for _, resource := range []string{"cpu", "memory", "io"} {
		waitSec, err := c.readPressureWaitingSeconds(resource)
		if err != nil {
			// File missing on kernel < 4.20 → ENOENT, not an error
			// worth surfacing. Anything else (permission, malformed)
			// gets stashed but doesn't block load reporting.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("pressure %s: %w", resource, err)
			}
			continue
		}
		samples = append(samples, sample(
			"node_pressure_"+resource+"_waiting_seconds_total",
			waitSec, base, now,
		))
	}

	return samples, firstErr
}

// readLoadAvg parses /proc/loadavg → [load1, load5, load15].
// Format: "0.15 0.10 0.05 1/250 12345" (3 floats, then runnable/total
// task counts, then last_pid). We only need the first 3.
func (c *NodeStressCollector) readLoadAvg() ([3]float64, error) {
	var out [3]float64
	data, err := os.ReadFile(c.procPath + "/loadavg")
	if err != nil {
		return out, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return out, fmt.Errorf("loadavg: expected ≥3 fields, got %d", len(fields))
	}
	for i := 0; i < 3; i++ {
		f, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return out, fmt.Errorf("loadavg field %d %q: %w", i, fields[i], err)
		}
		out[i] = f
	}
	return out, nil
}

// readPressureWaitingSeconds parses the "some" line of a PSI file
// and returns the total= value converted from microseconds to seconds.
//
// PSI file format (cpu / memory / io all the same shape):
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=12345
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=678    (optional, cpu may omit)
//
// We pick the "some" line because the node-exporter convention
// (`node_pressure_*_waiting_seconds_total`) corresponds to it —
// "at least one task waiting on this resource". The "full" line
// (all tasks waiting, i.e. true stall) maps to node-exporter's
// `node_pressure_*_stalled_seconds_total` which the UI doesn't
// query today; skipping it keeps cardinality minimal.
//
// `total=` is in microseconds per the kernel docs; divide by 1e6
// for the seconds gauge.
func (c *NodeStressCollector) readPressureWaitingSeconds(resource string) (float64, error) {
	data, err := os.ReadFile(c.procPath + "/pressure/" + resource)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if !strings.HasPrefix(field, "total=") {
				continue
			}
			raw := strings.TrimPrefix(field, "total=")
			usec, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse total= %q: %w", raw, err)
			}
			return float64(usec) / 1e6, nil
		}
		return 0, fmt.Errorf("no total= in some-line")
	}
	return 0, fmt.Errorf("no some-line in pressure/%s", resource)
}
