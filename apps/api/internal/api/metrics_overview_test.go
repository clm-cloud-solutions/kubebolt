package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBuildMetricsOnlyOverview_MapsKSM points the copilot VM client at a stub that
// returns a fixed scalar for every instant query, and asserts the metrics-only overview
// wires the KSM counts + the cluster UID + the always-pass "metrics" health check.
func TestBuildMetricsOnlyOverview_MapsKSM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// VM instant-query shape: data.result[0].value = [ts, "<scalar>"].
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "vector",
				"result": []map[string]any{
					{"metric": map[string]string{}, "value": []any{float64(0), "5"}},
				},
			},
		})
	}))
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	h := &handlers{}
	ov := h.buildMetricsOnlyOverview(context.Background(), "uid-123")

	if ov.ClusterUID != "uid-123" {
		t.Errorf("ClusterUID = %q, want uid-123", ov.ClusterUID)
	}
	if ov.Pods.Total != 5 || ov.Nodes.Total != 5 || ov.Deployments.Total != 5 {
		t.Errorf("counts not wired: pods=%d nodes=%d deploys=%d", ov.Pods.Total, ov.Nodes.Total, ov.Deployments.Total)
	}
	// pod health: running=failed=pending=5 (stub returns 5 for everything).
	if ov.Pods.Ready != 5 || ov.Pods.NotReady != 10 {
		t.Errorf("pod phase breakdown wrong: ready=%d notReady=%d", ov.Pods.Ready, ov.Pods.NotReady)
	}
	var hasMetricsPass bool
	for _, c := range ov.Health.Checks {
		if c.Name == "metrics" && c.Status == "pass" {
			hasMetricsPass = true
		}
	}
	if !hasMetricsPass {
		t.Error("expected an always-pass 'metrics' health check")
	}
}

// TestBuildMetricsOnlyOverview_VMUnreachable degrades to a zero overview (no panics)
// when VM can't be reached — still carries the UID + the metrics health check.
func TestBuildMetricsOnlyOverview_VMUnreachable(t *testing.T) {
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", "http://127.0.0.1:1") // nothing listens
	h := &handlers{}
	ov := h.buildMetricsOnlyOverview(context.Background(), "uid-x")
	if ov.ClusterUID != "uid-x" {
		t.Errorf("ClusterUID = %q, want uid-x", ov.ClusterUID)
	}
	if ov.Pods.Total != 0 || ov.Nodes.Total != 0 {
		t.Errorf("expected zero counts when VM unreachable, got pods=%d nodes=%d", ov.Pods.Total, ov.Nodes.Total)
	}
}

// Finding #102: the OpenCost the agent chart bundles republishes allocatable and
// PVC info with `source="opencost"`. Every query over those families must keep
// one series per object before it sums or counts, or a cluster that also sends
// kube-state-metrics reports twice its allocatable and half its usage.
func TestBuildMetricsOnlyOverview_ReadsRepublishedFamiliesOnce(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("query"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success",
			"data": map[string]any{"resultType": "vector", "result": []any{}}})
	}))
	defer srv.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", srv.URL)

	(&handlers{}).buildMetricsOnlyOverview(context.Background(), "uid-123")

	seen := 0
	for _, q := range queries {
		for _, family := range []string{"kube_node_status_allocatable", "kube_persistentvolumeclaim_info"} {
			if strings.Contains(q, family) {
				seen++
				if !strings.Contains(q, "max by (") {
					t.Errorf("%s is summed without keeping one copy per object:\n  %s", family, q)
				}
			}
		}
	}
	if seen != 3 {
		t.Fatalf("expected the CPU and memory allocatable and the PVC count, saw %d queries over those families in %q", seen, queries)
	}
}

// The same against a real VictoriaMetrics with both sources written, through the
// cluster scoping the overview applies. KUBEBOLT_TEST_VM_URL=http://localhost:8428
func TestBuildMetricsOnlyOverview_TwoSourcesOnVM(t *testing.T) {
	base := strings.TrimRight(os.Getenv("KUBEBOLT_TEST_VM_URL"), "/")
	if base == "" {
		t.Skip("KUBEBOLT_TEST_VM_URL not set")
	}
	cluster := "zz-finding-102-ov-" + time.Now().Format("150405.000")
	ts := time.Now().Add(-time.Minute).UnixMilli()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, src := range []map[string]string{{"job": "kube-state-metrics"}, {"source": "opencost"}} {
		for _, s := range []struct {
			name   string
			labels map[string]string
			v      float64
		}{
			{"kube_node_status_allocatable", map[string]string{"node": "worker-1", "resource": "cpu", "unit": "core"}, 4},
			{"kube_node_status_allocatable", map[string]string{"node": "worker-2", "resource": "cpu", "unit": "core"}, 4},
			{"kube_node_status_allocatable", map[string]string{"node": "worker-1", "resource": "memory", "unit": "byte"}, 16e9},
			{"kube_persistentvolumeclaim_info", map[string]string{"namespace": "shop", "persistentvolumeclaim": "data-db-0"}, 1},
		} {
			m := map[string]string{"__name__": s.name, "cluster_id": cluster}
			for k, v := range s.labels {
				m[k] = v
			}
			for k, v := range src {
				m[k] = v
			}
			_ = enc.Encode(map[string]any{"metric": m, "values": []float64{s.v}, "timestamps": []int64{ts}})
		}
	}
	resp, err := http.Post(base+"/api/v1/import", "application/json", &buf)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	resp.Body.Close()
	if r, err := http.Get(base + "/internal/force_flush"); err == nil {
		r.Body.Close()
	}
	t.Cleanup(func() {
		if r, err := http.Post(base+"/api/v1/admin/tsdb/delete_series?match[]="+url.QueryEscape(`{cluster_id="`+cluster+`"}`), "", nil); err == nil {
			r.Body.Close()
		}
	})
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", base)

	ov := (&handlers{}).buildMetricsOnlyOverview(context.Background(), cluster)
	if ov.CPU.Allocatable != 8000 {
		t.Errorf("CPU allocatable = %dm, want 8000m (two nodes of 4 cores, each sent twice)", ov.CPU.Allocatable)
	}
	if ov.Memory.Allocatable != 16e9 {
		t.Errorf("memory allocatable = %d, want 16e9", ov.Memory.Allocatable)
	}
	if ov.PVCs.Total != 1 {
		t.Errorf("PVCs = %d, want 1", ov.PVCs.Total)
	}
}
