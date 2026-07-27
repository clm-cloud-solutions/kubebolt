package collector

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// Exporter scrapes an arbitrary in-cluster Prometheus exporter
// (`/metrics`, text exposition format) and forwards its samples
// through the agent's normal ring-buffer → gRPC shipment path,
// stamped with `source=<name>` plus the cluster/tenant identity
// labels every agent sample carries.
//
// First use: OpenCost (E1 WS-B) — its exporter emits the cost
// families (node_total_hourly_cost, container_cpu_allocation, …)
// behind KubeBolt's Cost dashboard. The mechanism is deliberately
// generic: any exporter an operator lists in
// KUBEBOLT_AGENT_EXPORTERS ships the same way.
//
// Exporters are cluster-scoped sources (one Service, not per-node),
// so this collector must run behind the Lease election — see
// RunLeaderElectedExporters. A DaemonSet's N pods all scraping the
// same Service would ship N duplicate sample streams.
type Exporter struct {
	name        string
	url         string
	clusterID   string
	clusterName string
	// tenantID mirrors the other collectors' stamping contract — see
	// CadvisorCollector.tenantID for the full semantics.
	tenantID string
	client   *http.Client
}

// ExporterTarget is one parsed `name=url` entry from
// KUBEBOLT_AGENT_EXPORTERS.
type ExporterTarget struct {
	Name string
	URL  string
}

// ParseExporterTargets parses the KUBEBOLT_AGENT_EXPORTERS env value:
// comma-separated `name=url` pairs, e.g.
//
//	opencost=http://opencost.opencost.svc.cluster.local:9003/metrics
//
// Empty input → (nil, nil): the feature is opt-in and off by default.
// A malformed entry is an error, not a skip — a typo'd exporter that
// silently ships nothing is exactly the failure mode an operator
// can't debug.
func ParseExporterTargets(env string) ([]ExporterTarget, error) {
	env = strings.TrimSpace(env)
	if env == "" {
		return nil, nil
	}
	var targets []ExporterTarget
	for _, entry := range strings.Split(env, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, url, found := strings.Cut(entry, "=")
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		if !found || name == "" || url == "" {
			return nil, fmt.Errorf("exporters: malformed entry %q (want name=url)", entry)
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return nil, fmt.Errorf("exporters: entry %q: url must start with http:// or https://", entry)
		}
		targets = append(targets, ExporterTarget{Name: name, URL: url})
	}
	return targets, nil
}

// NewExporter builds a collector for one exporter target. A nil
// client gets a default with a timeout well under the scrape
// interval so one hung exporter can't stall the whole tick.
func NewExporter(target ExporterTarget, clusterID, clusterName, tenantID string, client *http.Client) *Exporter {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Exporter{
		name:        target.Name,
		url:         target.URL,
		clusterID:   clusterID,
		clusterName: clusterName,
		tenantID:    tenantID,
		client:      client,
	}
}

func (e *Exporter) Name() string { return "exporter_" + e.name }

// droppedExporterPrefixes are the Go/Prometheus runtime families every
// exporter binary emits about itself. They're never the payload the
// operator asked for, and container_* runtime metrics already come
// from the kubelet collectors — shipping them again from every
// exporter would be pure cardinality waste.
var droppedExporterPrefixes = []string{"go_", "process_", "promhttp_"}

// reservedExporterLabels are the identity labels this agent stamps;
// an exporter emitting its own value for one of these must not be
// able to spoof another cluster/tenant's series (mirrors the W3a
// anti-spoof stance on the ingest side).
var reservedExporterLabels = map[string]struct{}{
	"source": {}, "cluster_id": {}, "cluster_name": {}, "tenant_id": {},
}

func (e *Exporter) Collect(ctx context.Context) ([]*agentv2.Sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url, nil)
	if err != nil {
		return nil, fmt.Errorf("exporter %s: build request: %w", e.name, err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exporter %s: %w", e.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("exporter %s: status %d", e.name, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("exporter %s: read body: %w", e.name, err)
	}

	now := timestamppb.Now()
	var samples []*agentv2.Sample

	scanner := bufio.NewScanner(bytes.NewReader(body))
	// Same headroom as the cadvisor scrape — exporter output can be
	// large on big clusters (per-container allocation rows).
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)

scan:
	for scanner.Scan() {
		metric, labels, value, ok := parsePromLine(scanner.Text())
		if !ok {
			continue
		}
		for _, prefix := range droppedExporterPrefixes {
			if strings.HasPrefix(metric, prefix) {
				continue scan
			}
		}

		sampleLabels := make(map[string]string, len(labels)+4)
		for k, v := range labels {
			if _, reserved := reservedExporterLabels[k]; reserved {
				continue // agent identity wins over exporter-emitted values
			}
			sampleLabels[k] = v
		}
		sampleLabels["source"] = e.name
		sampleLabels["cluster_id"] = e.clusterID
		if e.clusterName != "" {
			sampleLabels["cluster_name"] = e.clusterName
		}
		if e.tenantID != "" {
			sampleLabels["tenant_id"] = e.tenantID
		}

		samples = append(samples, &agentv2.Sample{
			Timestamp:  now,
			MetricName: metric, // upstream names pass through — Prom-canonical is our schema
			Value:      value,
			Labels:     sampleLabels,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("exporter %s: scan: %w", e.name, err)
	}
	return samples, nil
}
