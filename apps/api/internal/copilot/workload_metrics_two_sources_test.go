package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Finding #102. The OpenCost the agent chart bundles republishes the requests,
// allocatable and capacity with `source="opencost"`; on a cluster that also
// sends kube-state-metrics, the same figure reaches the store twice. A plain sum
// told Kobi coredns asked for 280Mi when its spec says 140Mi, and halved the
// usage against it. These run the real queries against a real VictoriaMetrics,
// with both sources written, because only the store can say what a PromQL
// expression adds up to. KUBEBOLT_TEST_VM_URL=http://localhost:8428
func TestRequestsLimits_TwoSourcesAreReadOnce(t *testing.T) {
	base := strings.TrimRight(os.Getenv("KUBEBOLT_TEST_VM_URL"), "/")
	if base == "" {
		t.Skip("KUBEBOLT_TEST_VM_URL not set")
	}
	cluster := "zz-finding-102-" + time.Now().Format("150405.000")
	ts := time.Now().Add(-time.Minute).UnixMilli()

	// Two replicas of api, each asking 100m and 70Mi with a 170Mi limit; one node
	// with 4 cores allocatable and 8 of capacity. KSM sends everything, OpenCost
	// republishes all but the limits, as it does in production.
	var lines []map[string]any
	add := func(name string, labels map[string]string, v float64) {
		m := map[string]string{"__name__": name, "cluster_id": cluster}
		for k, val := range labels {
			m[k] = val
		}
		lines = append(lines, map[string]any{"metric": m, "values": []float64{v}, "timestamps": []int64{ts}})
	}
	sources := []map[string]string{{"job": "kube-state-metrics"}, {"source": "opencost"}}
	for _, pod := range []string{"api-7d9f8b6c5-abcde", "api-7d9f8b6c5-fghij"} {
		for i, src := range sources {
			l := func(resource, unit string) map[string]string {
				out := map[string]string{"namespace": "shop", "pod": pod, "container": "api", "resource": resource, "unit": unit}
				for k, v := range src {
					out[k] = v
				}
				return out
			}
			add("kube_pod_container_resource_requests", l("cpu", "core"), 0.1)
			add("kube_pod_container_resource_requests", l("memory", "byte"), 70*1024*1024)
			if i == 0 {
				add("kube_pod_container_resource_limits", l("memory", "byte"), 170*1024*1024)
			}
		}
	}
	for _, src := range sources {
		node := func(resource, unit string) map[string]string {
			out := map[string]string{"node": "worker-1", "resource": resource, "unit": unit}
			for k, v := range src {
				out[k] = v
			}
			return out
		}
		add("kube_node_status_allocatable", node("cpu", "core"), 4)
		add("kube_node_status_capacity", node("cpu", "core"), 8)
	}
	vmImport(t, base, lines)
	t.Cleanup(func() { vmDelete(base, `{cluster_id="`+cluster+`"}`) })

	prev := vmURLTestOverride
	vmURLTestOverride = base
	t.Cleanup(func() { vmURLTestOverride = prev })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now()

	resp := workloadMetricsResponse{Metrics: map[string]metricResponse{
		"cpu":    {Summary: metricSummary{Max: 0.05}},
		"memory": {Summary: metricSummary{Max: 42 * 1024 * 1024}},
	}}
	attachRequestsLimits(ctx, &resp, promBuilder{kind: "Deployment", namespace: "shop", name: "api", clusterUID: cluster}, now)

	cpu, mem := resp.Metrics["cpu"], resp.Metrics["memory"]
	assertNear(t, "cpu request (2 × 100m)", cpu.Request, 0.2)
	assertNear(t, "memory request (2 × 70Mi)", mem.Request, 140*1024*1024)
	assertNear(t, "memory limit (2 × 170Mi, KSM only)", mem.Limit, 340*1024*1024)
	if mem.UtilizationPercent == nil {
		t.Fatal("memory utilization missing")
	}
	assertNear(t, "memory vsRequest (42Mi of 140Mi)", mem.UtilizationPercent.VsRequest, 30)

	node := workloadMetricsResponse{Metrics: map[string]metricResponse{"cpu": {Summary: metricSummary{Max: 1}}}}
	attachRequestsLimits(ctx, &node, promBuilder{kind: "Node", name: "worker-1", clusterUID: cluster}, now)
	assertNear(t, "node allocatable", node.Metrics["cpu"].Request, 4)
	assertNear(t, "node capacity", node.Metrics["cpu"].Limit, 8)
}

func assertNear(t *testing.T, what string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: missing, want %v", what, want)
		return
	}
	if math.Abs(*got-want) > want*1e-9+1e-9 {
		t.Errorf("%s = %v, want %v", what, *got, want)
	}
}

func vmImport(t *testing.T, base string, lines []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := http.Post(base+"/api/v1/import", "application/json", &buf)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("import: HTTP %d", resp.StatusCode)
	}
	if r, err := http.Get(base + "/internal/force_flush"); err == nil {
		r.Body.Close()
	}
}

func vmDelete(base, match string) {
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/admin/tsdb/delete_series?match[]="+url.QueryEscape(match), nil)
	if err != nil {
		return
	}
	if r, err := http.DefaultClient.Do(req); err == nil {
		r.Body.Close()
	}
}
