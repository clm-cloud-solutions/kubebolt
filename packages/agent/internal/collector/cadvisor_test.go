package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

func TestCadvisorCollector_PromCanonicalSchema(t *testing.T) {
	samples := collectCadvisorFromFixture(t, "cadvisor_metrics.txt")

	t.Run("emits container_network_* with canonical names (no rename)", func(t *testing.T) {
		mustHaveMetric(t, samples, "container_network_receive_bytes_total")
		mustHaveMetric(t, samples, "container_network_transmit_bytes_total")
		mustHaveMetric(t, samples, "container_network_receive_errors_total")
		mustHaveMetric(t, samples, "container_network_transmit_errors_total")
		mustHaveMetric(t, samples, "container_network_receive_packets_dropped_total")
		mustHaveMetric(t, samples, "container_network_transmit_packets_dropped_total")
	})

	t.Run("legacy pod_network_* names not emitted", func(t *testing.T) {
		mustNotHaveMetric(t, samples, "pod_network_receive_bytes_total")
		mustNotHaveMetric(t, samples, "pod_network_transmit_bytes_total")
		mustNotHaveMetric(t, samples, "pod_network_receive_errors_total")
		mustNotHaveMetric(t, samples, "pod_network_transmit_errors_total")
		mustNotHaveMetric(t, samples, "pod_network_receive_packets_dropped_total")
		mustNotHaveMetric(t, samples, "pod_network_transmit_packets_dropped_total")
	})

	t.Run("uses canonical pod labels (namespace, pod, not pod_namespace, pod_name)", func(t *testing.T) {
		s := mustHaveMetric(t, samples, "container_network_receive_bytes_total")
		if got := s.Labels["namespace"]; got == "" {
			t.Error("missing canonical label `namespace`")
		}
		if got := s.Labels["pod"]; got == "" {
			t.Error("missing canonical label `pod`")
		}
		if _, has := s.Labels["pod_namespace"]; has {
			t.Error("legacy label `pod_namespace` must not be emitted in v1.0")
		}
		if _, has := s.Labels["pod_name"]; has {
			t.Error("legacy label `pod_name` must not be emitted in v1.0")
		}
	})

	t.Run("preserves container label including empty string for pod-level rows", func(t *testing.T) {
		// Fixture has both pod-level (container="") and per-container
		// (container="nginx") rows for receive_bytes. Both must appear
		// distinctly so dashboards can filter on container="" for
		// pod-aggregated views.
		rxBytes := samplesByName(samples, "container_network_receive_bytes_total")
		var seenPodLevel, seenContainerLevel bool
		for _, s := range rxBytes {
			if s.Labels["pod"] != "nginx-abc123" {
				continue
			}
			switch s.Labels["container"] {
			case "":
				seenPodLevel = true
			case "nginx":
				seenContainerLevel = true
			}
		}
		if !seenPodLevel {
			t.Error("expected pod-level row with container=\"\" for nginx-abc123")
		}
		if !seenContainerLevel {
			t.Error("expected per-container row with container=\"nginx\" for nginx-abc123")
		}
	})

	t.Run("propagates pod_uid when cAdvisor emits it", func(t *testing.T) {
		s := mustHaveMetric(t, samples, "container_network_receive_bytes_total")
		// At least one of the matched samples must have pod_uid set
		// (cAdvisor on real kubelets always emits it for pod rows).
		any := samplesByName(samples, "container_network_receive_bytes_total")
		var seen bool
		for _, sample := range any {
			if sample.Labels["pod_uid"] != "" {
				seen = true
				break
			}
		}
		if !seen {
			t.Error("expected at least one sample with pod_uid label set")
		}
		_ = s
	})

	t.Run("rows without pod or namespace labels are dropped", func(t *testing.T) {
		// Fixture has a row for /kubepods cgroup root (no pod label).
		// It must not appear in output.
		all := samplesByName(samples, "container_network_receive_bytes_total")
		for _, s := range all {
			if s.Labels["namespace"] == "" || s.Labels["pod"] == "" {
				t.Errorf("emitted sample without namespace or pod label: %+v", s.Labels)
			}
		}
	})

	t.Run("filters non-allowlisted metrics", func(t *testing.T) {
		// container_memory_working_set_bytes IS in the cAdvisor output but
		// NOT in our allowlist (we get memory from stats.go via /stats/summary).
		mustNotHaveMetric(t, samples, "container_memory_working_set_bytes")
		mustNotHaveMetric(t, samples, "container_cpu_usage_seconds_total")
	})

	t.Run("cluster_id, cluster_name, node labels propagated", func(t *testing.T) {
		s := mustHaveMetric(t, samples, "container_network_receive_bytes_total")
		if s.Labels["cluster_id"] != "test-cluster-id" {
			t.Errorf("cluster_id = %q, want test-cluster-id", s.Labels["cluster_id"])
		}
		if s.Labels["cluster_name"] != "test-cluster" {
			t.Errorf("cluster_name = %q, want test-cluster", s.Labels["cluster_name"])
		}
		if s.Labels["node"] != "kind-control-plane" {
			t.Errorf("node = %q, want kind-control-plane", s.Labels["node"])
		}
	})

	t.Run("interface label preserved", func(t *testing.T) {
		s := mustHaveMetric(t, samples, "container_network_receive_bytes_total")
		if got := s.Labels["interface"]; got == "" {
			t.Error("interface label missing")
		}
	})

	t.Run("tenant_id label absent when not configured", func(t *testing.T) {
		// The fixture-collected samples were produced with tenantID="".
		// Validate the conditional-stamp logic: zero value → no label.
		// Receiver auto-stamps in this case (Phase 3 Day 4.1 fallback).
		s := mustHaveMetric(t, samples, "container_network_receive_bytes_total")
		if _, has := s.Labels["tenant_id"]; has {
			t.Errorf("tenant_id should be absent when tenantID==\"\", got %q", s.Labels["tenant_id"])
		}
	})
}

func TestCadvisorCollector_TenantIDStamped(t *testing.T) {
	// Same fixture path as the main test, but constructed with a
	// non-empty tenantID. Verifies the Day 4.2 stamping path.
	path := filepath.Join("testdata", "cadvisor_metrics.txt")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))
	c := NewCadvisor(client, "cid", "cn", "node", "tenant-acme", nil)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected samples")
	}
	for _, s := range samples {
		if got := s.Labels["tenant_id"]; got != "tenant-acme" {
			t.Errorf("metric %s tenant_id = %q, want tenant-acme", s.MetricName, got)
		}
	}
}

func collectCadvisorFromFixture(t *testing.T, fixture string) []*agentv2.Sample {
	t.Helper()
	path := filepath.Join("testdata", fixture)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics/cadvisor" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))
	c := NewCadvisor(client, "test-cluster-id", "test-cluster", "kind-control-plane", "", nil)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("Collect returned zero samples; fixture or collector broken")
	}
	return samples
}

// TestCadvisorCollector_DropsFilteredInterfaces verifies the dropInterfaces set
// (helm value collectors.dropNetworkInterfaces) excludes those interfaces from
// container_network_* while every other interface passes through unchanged.
func TestCadvisorCollector_DropsFilteredInterfaces(t *testing.T) {
	const body = `container_network_receive_bytes_total{namespace="ns1",pod="pod1",interface="eth0"} 12345
container_network_receive_bytes_total{namespace="ns1",pod="pod1",interface="sit0"} 0
container_network_transmit_bytes_total{namespace="ns1",pod="pod1",interface="gre0"} 0
container_network_transmit_bytes_total{namespace="ns1",pod="pod1",interface="eth0"} 678
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics/cadvisor" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))

	drop := map[string]struct{}{"sit0": {}, "gre0": {}}
	c := NewCadvisor(client, "cid", "cn", "node", "", drop)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// The two dropped-interface lines must be gone; only the two eth0 lines remain.
	var sawEth0 bool
	for _, s := range samples {
		switch s.Labels["interface"] {
		case "sit0", "gre0":
			t.Errorf("dropped interface %q leaked into samples", s.Labels["interface"])
		case "eth0":
			sawEth0 = true
		}
	}
	if !sawEth0 {
		t.Error("eth0 was filtered but must be kept")
	}
	if len(samples) != 2 {
		t.Errorf("expected 2 samples (both eth0) after filtering, got %d", len(samples))
	}
}

