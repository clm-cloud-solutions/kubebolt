package collector

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
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
}

func NewCadvisor(client *kubelet.Client, clusterID, clusterName, nodeName string) *CadvisorCollector {
	return &CadvisorCollector{
		client:      client,
		clusterID:   clusterID,
		clusterName: clusterName,
		nodeName:    nodeName,
	}
}

func (c *CadvisorCollector) Name() string { return "kubelet_cadvisor_network" }

// cadvisorToPodMetric maps the upstream metric name to our agent schema.
// Empty string means the metric is ignored.
var cadvisorToPodMetric = map[string]string{
	"container_network_receive_bytes_total":            "pod_network_receive_bytes_total",
	"container_network_transmit_bytes_total":           "pod_network_transmit_bytes_total",
	"container_network_receive_errors_total":           "pod_network_receive_errors_total",
	"container_network_transmit_errors_total":          "pod_network_transmit_errors_total",
	"container_network_receive_packets_dropped_total":  "pod_network_receive_packets_dropped_total",
	"container_network_transmit_packets_dropped_total": "pod_network_transmit_packets_dropped_total",
}

func (c *CadvisorCollector) Collect(ctx context.Context) ([]*agentv1.Sample, error) {
	body, err := c.client.Get(ctx, "/metrics/cadvisor")
	if err != nil {
		return nil, err
	}

	now := timestamppb.Now()
	var samples []*agentv1.Sample

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
		outName, want := cadvisorToPodMetric[metric]
		if !want {
			continue
		}
		podNs := labels["namespace"]
		podName := labels["pod"]
		if podNs == "" || podName == "" {
			continue
		}
		iface := labels["interface"]
		// All containers in a pod share a network namespace, so every
		// cAdvisor row for a pod/iface reports the same counter value.
		// We drop the container label entirely and let VM collapse the
		// duplicates. Using a seen-set would save a few bytes per scrape
		// but costs complexity; not worth it at our scale.
		sampleLabels := map[string]string{
			"cluster_id":    c.clusterID,
			"node":          c.nodeName,
			"pod_namespace": podNs,
			"pod_name":      podName,
			"interface":     iface,
		}
		if c.clusterName != "" {
			sampleLabels["cluster_name"] = c.clusterName
		}
		samples = append(samples, &agentv1.Sample{
			Timestamp:  now,
			MetricName: outName,
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
