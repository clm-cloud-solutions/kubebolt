package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
