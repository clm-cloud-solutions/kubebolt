package collector

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// CadvisorCollector scrapes the kubelet's /metrics/cadvisor endpoint
// (Prometheus text exposition format) and extracts pod-level network
// counters. It fills what /stats/summary leaves out: packets_dropped, which
// the summary has no field for, and pods or interfaces the summary does not
// report (short-lived pods, kubelets that leave its `network` block empty).
// With WithStatsNetwork it sends only those: what the stats collector
// already sent is skipped, so each pod interface reaches the store once.
//
// The kubelet's endpoint reports pod network only for the pause sandbox
// (container="") and carries no pod_uid (testdata/cadvisor_metrics.txt
// mirrors a real kubelet).
type CadvisorCollector struct {
	client      *kubelet.Client
	clusterID   string
	clusterName string
	nodeName    string
	// tenantID is the per-tenant identifier stamped on every sample
	// for SaaS billing + cardinality scoping (Phase 3 Day 4.2).
	// Sourced from KUBEBOLT_TENANT_ID env var via helm value
	// `tenant.id`. Empty string means "no tenant" — samples ship
	// without a tenant_id label and the backend's receiver
	// auto-stamps from the bearer token's tenant (Day 4.1 fallback).
	// Once Day 4.3 lands and enforced mode requires the label,
	// operators MUST set tenant.id at install.
	tenantID string
	// dropInterfaces decides which network interface names to skip when
	// emitting container_network_* samples. Sourced from the helm value
	// `collectors.dropNetworkInterfaces` via KUBEBOLT_AGENT_DROP_NET_INTERFACES.
	// Exact entries cover kernel tunnel devices (sit0/gre0/tunl0/…) that
	// exist in every network namespace but never carry traffic; "name*"
	// prefix entries cover the per-pod veth peers on cloud CNIs
	// (azv<hash>/veth<hash>/eni<hash>/cali<hash>) whose hash-suffixed
	// names dominate container_network_* cardinality — ~85% of active
	// series on a 2k-pod AKS cluster. nil means "keep all interfaces".
	dropInterfaces *InterfaceMatcher
	// statsNetwork is what the stats collector sent in its last pass; those
	// samples are skipped. Nil = send everything.
	statsNetwork *PodNetworkSeen
	// podUIDs resolves a pod's UID by namespace and name, for the families
	// only this collector sends (cadvisorOnlyMetrics). Nil = no pod_uid.
	podUIDs *PodsCache
}

// CadvisorOption is a functional option for NewCadvisor.
type CadvisorOption func(*CadvisorCollector)

// WithStatsNetwork skips the pod network samples the stats collector sent
// in its last pass (see PodNetworkSeen). Run this collector right after
// stats, in the same loop, so the set it reads is that pass's.
func WithStatsNetwork(seen *PodNetworkSeen) CadvisorOption {
	return func(c *CadvisorCollector) { c.statsNetwork = seen }
}

// WithPodUIDs stamps pod_uid on the families only this collector sends, so
// PodsCache.Enrich adds their workload labels.
func WithPodUIDs(pods *PodsCache) CadvisorOption {
	return func(c *CadvisorCollector) { c.podUIDs = pods }
}

func NewCadvisor(client *kubelet.Client, clusterID, clusterName, nodeName, tenantID string, dropInterfaces *InterfaceMatcher, opts ...CadvisorOption) *CadvisorCollector {
	c := &CadvisorCollector{
		client:         client,
		clusterID:      clusterID,
		clusterName:    clusterName,
		nodeName:       nodeName,
		tenantID:       tenantID,
		dropInterfaces: dropInterfaces,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *CadvisorCollector) Name() string { return "kubelet_cadvisor_network" }

// allowedCadvisorMetrics is the set of cAdvisor metrics this collector
// surfaces. All upstream names pass through unchanged — Prom-canonical is
// our own schema since v1.0. The list is intentionally narrow: container
// CPU + memory come from stats.go (kubelet /stats/summary); cadvisor.go's
// only job is filling network gaps that /stats/summary leaves blank
// (notably packets_dropped_total, which the kubelet's network struct
// doesn't expose) and acting as a fallback on kubelets that don't populate
// the network block at all (docker-desktop pod-level, EKS Bottlerocket
// older versions).
var allowedCadvisorMetrics = map[string]struct{}{
	"container_network_receive_bytes_total":            {},
	"container_network_transmit_bytes_total":           {},
	"container_network_receive_errors_total":           {},
	"container_network_transmit_errors_total":          {},
	"container_network_receive_packets_dropped_total":  {},
	"container_network_transmit_packets_dropped_total": {},
}

// cadvisorOnlyMetrics are the families /stats/summary has no field for.
// Only these get a pod_uid (WithPodUIDs). Bytes and errors never do: for a
// pod stats reports from its next pass on, a cAdvisor sample with the same
// labels would land in stats' series, and two readings of one counter taken
// at different moments read as a counter reset.
var cadvisorOnlyMetrics = map[string]struct{}{
	"container_network_receive_packets_dropped_total":  {},
	"container_network_transmit_packets_dropped_total": {},
}

func (c *CadvisorCollector) Collect(ctx context.Context) ([]*agentv2.Sample, error) {
	body, err := c.client.Get(ctx, "/metrics/cadvisor")
	if err != nil {
		return nil, err
	}

	now := timestamppb.Now()
	var samples []*agentv2.Sample

	scanner := bufio.NewScanner(bytes.NewReader(body))
	// /metrics/cadvisor output can be large; bump the scan buffer past the
	// default 64KB limit.
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		metric, labels, value, ok := parsePromLine(line)
		if !ok {
			continue
		}
		if _, want := allowedCadvisorMetrics[metric]; !want {
			continue
		}
		// Skip network interfaces the operator has dropped (kernel tunnel
		// devices + per-pod veth peers — see dropInterfaces). Drops on a
		// nil matcher is safe and keeps everything.
		if c.dropInterfaces.Drops(labels["interface"]) {
			continue
		}
		podNs := labels["namespace"]
		podName := labels["pod"]
		if podNs == "" || podName == "" {
			continue
		}
		// Already sent by the stats collector this pass. The container label
		// is not part of the key: the kubelet reports pod network only for
		// the pause sandbox (container=""), and a per-container row from some
		// other cAdvisor would repeat the same counters.
		if c.statsNetwork.has(metric, podNs, podName, labels["interface"]) {
			continue
		}
		sampleLabels := map[string]string{
			"cluster_id": c.clusterID,
			"node":       c.nodeName,
			"namespace":  podNs,
			"pod":        podName,
			"container":  labels["container"], // "" for the pod-level row
			"interface":  labels["interface"],
		}
		if _, only := cadvisorOnlyMetrics[metric]; only {
			if uid := c.podUIDs.UIDFor(podNs, podName); uid != "" {
				sampleLabels["pod_uid"] = uid
			}
		}
		if c.clusterName != "" {
			sampleLabels["cluster_name"] = c.clusterName
		}
		if c.tenantID != "" {
			sampleLabels["tenant_id"] = c.tenantID
		}
		samples = append(samples, &agentv2.Sample{
			Timestamp:  now,
			MetricName: metric, // canonical name passed through
			Value:      value,
			Labels:     sampleLabels,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

// parsePromLine extracts metric name, labels, and sample value from a single
// Prometheus text exposition line. Returns ok=false for comments, blank
// lines, and anything it can't parse.
//
// This is a deliberately small parser — cAdvisor's output uses a
// well-behaved subset of the format (no exemplars, no histograms on the
// metrics we care about). If we ever need full fidelity, swap in
// prometheus/common/expfmt.
func parsePromLine(line string) (metric string, labels map[string]string, value float64, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", nil, 0, false
	}

	var valueStart int
	braceIdx := strings.IndexByte(line, '{')
	if braceIdx >= 0 {
		metric = line[:braceIdx]
		braceEnd := findLabelsEnd(line, braceIdx)
		if braceEnd < 0 {
			return "", nil, 0, false
		}
		labels = parsePromLabels(line[braceIdx+1 : braceEnd])
		valueStart = braceEnd + 1
	} else {
		spaceIdx := strings.IndexByte(line, ' ')
		if spaceIdx < 0 {
			return "", nil, 0, false
		}
		metric = line[:spaceIdx]
		valueStart = spaceIdx
	}

	fields := strings.Fields(line[valueStart:])
	if len(fields) == 0 {
		return "", nil, 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", nil, 0, false
	}
	return metric, labels, v, true
}

// findLabelsEnd walks from the opening brace to the matching closing brace,
// skipping quoted strings so that a quote-wrapped `}` doesn't confuse us.
func findLabelsEnd(s string, start int) int {
	inQuote := false
	for i := start + 1; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			i++ // skip escaped next byte
			continue
		}
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && ch == '}' {
			return i
		}
	}
	return -1
}

// parsePromLabels parses the comma-separated key="value" pairs inside the
// braces of a Prometheus line. Handles \", \\, and \n escapes in values.
func parsePromLabels(s string) map[string]string {
	out := map[string]string{}
	pos := 0
	for pos < len(s) {
		// skip leading whitespace / commas
		for pos < len(s) && (s[pos] == ' ' || s[pos] == ',') {
			pos++
		}
		if pos >= len(s) {
			break
		}
		eq := strings.IndexByte(s[pos:], '=')
		if eq < 0 {
			break
		}
		key := s[pos : pos+eq]
		pos += eq + 1
		if pos >= len(s) || s[pos] != '"' {
			break
		}
		pos++ // skip opening "
		var sb strings.Builder
		for pos < len(s) {
			ch := s[pos]
			if ch == '\\' && pos+1 < len(s) {
				switch s[pos+1] {
				case 'n':
					sb.WriteByte('\n')
				case '\\':
					sb.WriteByte('\\')
				case '"':
					sb.WriteByte('"')
				default:
					sb.WriteByte(s[pos+1])
				}
				pos += 2
				continue
			}
			if ch == '"' {
				pos++
				break
			}
			sb.WriteByte(ch)
			pos++
		}
		out[key] = sb.String()
	}
	return out
}
