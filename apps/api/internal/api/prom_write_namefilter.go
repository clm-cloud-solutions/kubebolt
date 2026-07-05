package api

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// coreMetricPrefixes are the KubeBolt-consumed metric families — the
// "core K8s" contract from docs/kubebolt-metric-label-registry.md. A series
// whose __name__ starts with one of these (or equals a member of
// coreMetricExact) is core; everything else is "custom" — the customer's own
// app metrics arriving via remote_write, which KubeBolt's product doesn't
// read. Keep in LOCKSTEP with the agent scrape allowlist
// (deploy/helm/kubebolt-agent/values.yaml scrape.metricRelabelConfigs) and
// the Mode C matchers (packages/agent/internal/promread). A missed prefix
// silently drops a family the platform needs.
var coreMetricPrefixes = []string{
	"kube_",      // kube-state-metrics
	"node_",      // node-exporter + agent kubelet node metrics
	"container_", // cadvisor/kubelet per-container
	"kubelet_",   // kubelet volume stats
	"pod_flow_",  // Hubble L4/L7 flows
	"pod_dns_",   // Hubble DNS resolutions
	"hubble_",    // Hubble availability
	"kubebolt_",  // agent + backend self-metrics
	"process_",   // scraper self-metrics
}

// coreMetricExact holds full metric names that are core but don't carry a
// family prefix.
var coreMetricExact = map[string]struct{}{
	"up": {}, // per-target scrape health
}

// isCoreMetricName reports whether a metric name belongs to a KubeBolt
// family. Cheap: an exact-set lookup then a short prefix scan. Runs
// per-series on the ingest hot path.
func isCoreMetricName(name string) bool {
	if _, ok := coreMetricExact[name]; ok {
		return true
	}
	for _, p := range coreMetricPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// PromNameFilter is the ingest-time core/custom classifier — Layer 2 of the
// cardinality cost-control plan (finding #10) and the enforcement point for
// the core-vs-custom split in the active-series pricing proposal.
//
// When a tenant is on the core-only floor (EffectiveLimits.AllowCustomSeries
// == false, the default), it drops every TimeSeries whose __name__ isn't a
// KubeBolt family: the server-side backstop for a customer remote_write'ing
// their own Prometheus past the agent's scrape allowlist. When the tenant
// opts into custom telemetry (AllowCustomSeries == true), it's a no-op — the
// custom series flow to VM and count toward MaxActiveSeries (the billable
// tier). The global `enabled` flag is a fleet-wide incident kill-switch.
type PromNameFilter struct {
	enabled  bool
	defaults auth.EffectiveLimits
	metrics  *PromWriteMetrics
}

// NewPromNameFilter builds the filter. `enabled` is the global kill-switch
// (config.PromWriteLimitsConfig.NameFilterEnabled); `defaults` is the fleet
// EffectiveLimits used to resolve a tenant's AllowCustomSeries when the
// tenant has no override.
func NewPromNameFilter(enabled bool, defaults auth.EffectiveLimits, metrics *PromWriteMetrics) *PromNameFilter {
	return &PromNameFilter{enabled: enabled, defaults: defaults, metrics: metrics}
}

// Filter applies the core/custom policy for a tenant to the decoded
// WriteRequest. It returns the payload to forward, the count of dropped
// series and their samples, and whether it rewrote the payload.
//
// No-op paths (return the input unchanged, rewrote=false, so the caller
// forwards the original snappy body without re-encoding):
//   - the filter is globally disabled, OR
//   - the tenant's effective policy allows custom series.
//
// On the enforcing path it drops non-core TimeSeries, records the dropped
// count on the metrics, and returns the rewritten decoded bytes (the caller
// re-snappy-encodes before forwarding and subtracts droppedSamples from the
// metered accepted count).
func (f *PromNameFilter) Filter(tenantID string, overrides *auth.TenantLimits, decoded []byte) (out []byte, droppedSeries, droppedSamples int, rewrote bool, err error) {
	if f == nil || !f.enabled {
		return decoded, 0, 0, false, nil
	}
	if auth.ResolveLimits(overrides, f.defaults).AllowCustomSeries {
		return decoded, 0, 0, false, nil
	}
	filtered, ds, dsamp, ferr := filterNonCoreSeries(decoded)
	if ferr != nil {
		return nil, 0, 0, false, ferr
	}
	if ds > 0 {
		f.metrics.RecordDroppedByName(tenantID, ds)
	}
	return filtered, ds, dsamp, ds > 0, nil
}

// filterNonCoreSeries rewrites the decoded WriteRequest, keeping only the
// TimeSeries whose __name__ is a KubeBolt family. Mirrors injectTenantID's
// protowire walk: field-1 TimeSeries are inspected; non-core ones dropped;
// kept ones plus any non-TimeSeries top-level fields are copied
// byte-verbatim (so unknown/future WriteRequest fields survive). Returns the
// filtered payload and the counts of dropped series and their samples.
func filterNonCoreSeries(decoded []byte) (out []byte, droppedSeries, droppedSamples int, err error) {
	const fieldTimeSeries = 1
	out = make([]byte, 0, len(decoded))
	rem := decoded
	startLen := len(rem)
	for len(rem) > 0 {
		fieldStart := startLen - len(rem)
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			return nil, 0, 0, fmt.Errorf("filterNonCoreSeries tag: %w", protowire.ParseError(tagLen))
		}
		afterTag := rem[tagLen:]
		if num == fieldTimeSeries && typ == protowire.BytesType {
			tsBytes, n := protowire.ConsumeBytes(afterTag)
			if n < 0 {
				return nil, 0, 0, fmt.Errorf("filterNonCoreSeries ts bytes: %w", protowire.ParseError(n))
			}
			name, samples := inspectTimeSeries(tsBytes)
			if isCoreMetricName(name) {
				// Keep — copy the whole field (tag + length-prefix + body) verbatim.
				out = append(out, decoded[fieldStart:fieldStart+tagLen+n]...)
			} else {
				droppedSeries++
				droppedSamples += samples
			}
			rem = afterTag[n:]
			continue
		}
		// Non-TimeSeries top-level field — pass through verbatim.
		valLen := protowire.ConsumeFieldValue(num, typ, afterTag)
		if valLen < 0 {
			return nil, 0, 0, fmt.Errorf("filterNonCoreSeries skip: %w", protowire.ParseError(valLen))
		}
		out = append(out, decoded[fieldStart:fieldStart+tagLen+valLen]...)
		rem = afterTag[valLen:]
	}
	return out, droppedSeries, droppedSamples, nil
}

// inspectTimeSeries walks a single TimeSeries body once, returning its
// __name__ label value and its Sample count. A TimeSeries with no __name__
// label reads as "" (never core — dropped by the enforcing path, which is
// correct: a nameless series is not a KubeBolt metric).
func inspectTimeSeries(tsBytes []byte) (name string, sampleCount int) {
	const (
		fieldLabels     = 1 // TimeSeries.labels
		fieldSamples    = 2 // TimeSeries.samples
		metricNameLabel = "__name__"
	)
	inner := tsBytes
	for len(inner) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(inner)
		if tagLen < 0 {
			return name, sampleCount
		}
		inner = inner[tagLen:]
		if num == fieldLabels && typ == protowire.BytesType {
			labelBytes, m := protowire.ConsumeBytes(inner)
			if m < 0 {
				return name, sampleCount
			}
			inner = inner[m:]
			if lname, lvalue, ok := parseLabelNameValue(labelBytes); ok && lname == metricNameLabel {
				name = lvalue
			}
			continue
		}
		if num == fieldSamples && typ == protowire.BytesType {
			sampleCount++
		}
		skip := protowire.ConsumeFieldValue(num, typ, inner)
		if skip < 0 {
			return name, sampleCount
		}
		inner = inner[skip:]
	}
	return name, sampleCount
}
