package promread

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// Convert turns a Prometheus query_range matrix response into the
// agent's wire-format Sample slice. The agent's existing buffer.Ring
// and shipper then forward via the AgentChannel exactly as they do
// for cadvisor / kubelet / hubble samples — the backend doesn't
// differentiate the origin.
//
// clusterID + clusterName + tenantID are stamped on every sample's
// Labels map (mirrors the cadvisor convention). The __name__ entry
// from the Prom response becomes Sample.MetricName; the remaining
// labels merge into Sample.Labels.
//
// When nodeIdx is non-nil and a sample's __name__ starts with
// "node_", Convert tries to derive a `node=<k8s-node-name>` label
// from the series' `instance` label (host-network pod IP →
// Kubernetes node name). Required for UI parity with Mode A — the
// Node Monitor panels and the node-exporter coverage chip both
// filter by `node`, not `instance`.
func Convert(
	resp *QueryRangeResponse,
	clusterID, clusterName, tenantID string,
	nodeIdx NodeIndex,
) ([]*agentv2.Sample, error) {
	if resp == nil {
		return nil, errors.New("convert: response is nil")
	}
	if resp.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("convert: expected matrix result, got %q", resp.Data.ResultType)
	}

	var out []*agentv2.Sample
	for _, series := range resp.Data.Result {
		metricName := series.Metric["__name__"]
		if metricName == "" {
			// Series without a __name__ has no agent-side
			// interpretation — skip rather than emit blank rows.
			continue
		}
		baseLabels := make(map[string]string, len(series.Metric)+3)
		for k, v := range series.Metric {
			if k == "__name__" {
				continue
			}
			baseLabels[k] = v
		}
		if clusterID != "" {
			baseLabels["cluster_id"] = clusterID
		}
		if clusterName != "" {
			baseLabels["cluster_name"] = clusterName
		}
		if tenantID != "" {
			baseLabels["tenant_id"] = tenantID
		}
		// node_* enrichment — see func doc. Two-pass fallback:
		//   1. If the series already carries a `node` label (GMP
		//      auto-relabels server-side), respect it — don't override.
		//   2. Else try NodeByIP(StripPort(instance)) — AMP /
		//      node-exporter shape where instance is `<pod-IP>:9100`
		//      and the pod runs hostNetwork on a node, so pod IP
		//      equals node InternalIP.
		//   3. Else fall back to IsKnownNode(stripped) — Azure managed
		//      Prom shape where instance is already a node name like
		//      `aks-nodepool1-XXX-vmss000000`. We accept it as a node
		//      label only if it matches a real Node.metadata.name in
		//      the cluster (paranoid check: prevents an attacker who
		//      controls `instance` from injecting an arbitrary node
		//      label).
		// Skipped silently when nodeIdx is nil, when `instance` is
		// missing, or when both lookups miss (no false stamps).
		if nodeIdx != nil && strings.HasPrefix(metricName, "node_") {
			if _, alreadyHas := baseLabels["node"]; !alreadyHas {
				if instance := series.Metric["instance"]; instance != "" {
					stripped := StripPort(instance)
					nodeName := nodeIdx.NodeByIP(stripped)
					if nodeName == "" && nodeIdx.IsKnownNode(stripped) {
						nodeName = stripped
					}
					if nodeName != "" {
						baseLabels["node"] = nodeName
					}
				}
			}
		}

		for _, pair := range series.Values {
			if len(pair) != 2 {
				continue
			}
			tsFloat, ok := pair[0].(float64)
			if !ok {
				continue
			}
			valStr, ok := pair[1].(string)
			if !ok {
				continue
			}
			val, err := strconv.ParseFloat(valStr, 64)
			if err != nil {
				// Prom emits "NaN", "+Inf", "-Inf" for non-finite
				// values; ParseFloat accepts those. A failure here
				// means the payload was unparseable — skip the
				// point rather than poison the batch.
				continue
			}
			// Defensive copy: each Sample gets its own Labels map
			// to avoid aliasing across timestamps.
			labels := make(map[string]string, len(baseLabels))
			for k, v := range baseLabels {
				labels[k] = v
			}
			sec := int64(tsFloat)
			nano := int64((tsFloat - float64(sec)) * 1e9)
			ts := time.Unix(sec, nano)
			out = append(out, &agentv2.Sample{
				Timestamp:  timestamppb.New(ts),
				MetricName: metricName,
				Value:      val,
				Labels:     labels,
			})
		}
	}
	// Deterministic ordering for tests + buffer locality. Sorted by
	// metric name first, then timestamp.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MetricName != out[j].MetricName {
			return out[i].MetricName < out[j].MetricName
		}
		return out[i].Timestamp.AsTime().Before(out[j].Timestamp.AsTime())
	})
	return out, nil
}
