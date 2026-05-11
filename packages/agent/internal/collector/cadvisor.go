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
// counters. Exists because some kubelet deployments — docker-desktop
// among them — leave the `network` block empty in /stats/summary, while
// cAdvisor still reports it correctly from the pod's network namespace.
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
}

func NewCadvisor(client *kubelet.Client, clusterID, clusterName, nodeName, tenantID string) *CadvisorCollector {
	return &CadvisorCollector{
		client:      client,
		clusterID:   clusterID,
		clusterName: clusterName,
		nodeName:    nodeName,
		tenantID:    tenantID,
	}
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
		podNs := labels["namespace"]
		podName := labels["pod"]
		if podNs == "" || podName == "" {
			continue
		}
		// cAdvisor emits container_network_* per pod and per container in
		// the pod. The pod-level row carries container="" (the pause
		// container, owner of the network namespace); per-container rows
		// repeat the same counter values. We pass them all through with
		// canonical labels — VM stores them as distinct series and
		// dashboards filter on container="" for the pod-level view, per
		// cAdvisor mainstream convention. The 5x cardinality is the
		// accepted trade-off for canonical compatibility.
		sampleLabels := map[string]string{
			"cluster_id": c.clusterID,
			"node":       c.nodeName,
			"namespace":  podNs,
			"pod":        podName,
			"container":  labels["container"], // may be empty for pod-level row
			"interface":  labels["interface"],
		}
		if uid := labels["pod_uid"]; uid != "" {
			sampleLabels["pod_uid"] = uid
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
