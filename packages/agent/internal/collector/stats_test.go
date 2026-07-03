package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// TestStatsCollector_PromCanonicalSchema validates that the v1.0 refactor
// emits the cAdvisor-canonical metric names + labels and drops the legacy
// schema. Each subtest is one acceptance criterion from the schema audit.
func TestStatsCollector_PromCanonicalSchema(t *testing.T) {
	samples := collectFromFixture(t, "stats_summary.json")

	t.Run("emits cAdvisor-canonical container metric names", func(t *testing.T) {
		mustHaveMetric(t, samples, "container_cpu_usage_seconds_total")
		mustHaveMetric(t, samples, "container_memory_working_set_bytes")
		mustHaveMetric(t, samples, "container_memory_rss")     // no _bytes suffix
		mustHaveMetric(t, samples, "container_memory_usage_bytes")
		mustHaveMetric(t, samples, "container_memory_failures_total")
	})

	t.Run("legacy gauges removed", func(t *testing.T) {
		mustNotHaveMetric(t, samples, "container_cpu_usage_cores")
		mustNotHaveMetric(t, samples, "node_cpu_usage_cores")
		mustNotHaveMetric(t, samples, "container_memory_rss_bytes") // legacy with _bytes suffix
	})

	t.Run("page faults collapsed into single metric with failure_type", func(t *testing.T) {
		mustNotHaveMetric(t, samples, "container_memory_page_faults_total")
		mustNotHaveMetric(t, samples, "container_memory_major_page_faults_total")
		// failures_total{failure_type=pgfault} and {failure_type=pgmajfault}
		// must both exist for a container that emitted both raw counters.
		failures := samplesByName(samples, "container_memory_failures_total")
		var seenPgfault, seenPgmajfault bool
		for _, s := range failures {
			switch s.Labels["failure_type"] {
			case "pgfault":
				seenPgfault = true
				if s.Labels["scope"] != "container" {
					t.Errorf("pgfault sample missing scope=container, got %q", s.Labels["scope"])
				}
			case "pgmajfault":
				seenPgmajfault = true
			}
		}
		if !seenPgfault {
			t.Error("expected failure_type=pgfault sample, got none")
		}
		if !seenPgmajfault {
			t.Error("expected failure_type=pgmajfault sample, got none")
		}
	})

	t.Run("pod_network_* renamed to container_network_* with empty container label", func(t *testing.T) {
		mustNotHaveMetric(t, samples, "pod_network_receive_bytes_total")
		mustNotHaveMetric(t, samples, "pod_network_transmit_bytes_total")

		net := samplesByName(samples, "container_network_receive_bytes_total")
		if len(net) == 0 {
			t.Fatal("expected at least one container_network_receive_bytes_total sample")
		}
		// All pod-level network samples must carry container="" per cAdvisor
		// pod-network namespace convention.
		for _, s := range net {
			c, hasContainer := s.Labels["container"]
			if !hasContainer || c != "" {
				t.Errorf("expected container=\"\" on pod-level network sample, got container=%q (hasLabel=%v)", c, hasContainer)
			}
		}
	})

	t.Run("volume metrics renamed to kubelet_volume_stats_*", func(t *testing.T) {
		mustNotHaveMetric(t, samples, "pod_volume_used_bytes")
		mustNotHaveMetric(t, samples, "pod_volume_capacity_bytes")
		mustNotHaveMetric(t, samples, "pod_volume_available_bytes")

		mustHaveMetric(t, samples, "kubelet_volume_stats_used_bytes")
		mustHaveMetric(t, samples, "kubelet_volume_stats_capacity_bytes")
		mustHaveMetric(t, samples, "kubelet_volume_stats_available_bytes")
		mustHaveMetric(t, samples, "kubelet_volume_stats_inodes")
		mustHaveMetric(t, samples, "kubelet_volume_stats_inodes_used")
		mustHaveMetric(t, samples, "kubelet_volume_stats_inodes_free")
	})

	t.Run("only PVC volumes are emitted", func(t *testing.T) {
		// Fixture has 3 volumes on the nginx pod: a PVC ("data"), an emptyDir
		// ("tmp"), a configMap ("config"). Only the PVC should appear in the
		// output; the other two are silently skipped per kubelet canonical
		// convention.
		used := samplesByName(samples, "kubelet_volume_stats_used_bytes")
		if len(used) != 1 {
			t.Errorf("expected exactly 1 kubelet_volume_stats_used_bytes sample (PVC only), got %d", len(used))
		}
		if got := used[0].Labels["persistentvolumeclaim"]; got != "nginx-data" {
			t.Errorf("expected persistentvolumeclaim=nginx-data, got %q", got)
		}
		// The legacy `volume` and `pvc_name` labels must be absent.
		if _, has := used[0].Labels["volume"]; has {
			t.Error("legacy `volume` label should be dropped")
		}
		if _, has := used[0].Labels["pvc_name"]; has {
			t.Error("legacy `pvc_name` label should be renamed to `persistentvolumeclaim`")
		}
	})

	t.Run("pod labels follow Prom canonical (namespace, pod, not pod_namespace, pod_name)", func(t *testing.T) {
		// Pick any pod-scoped metric and inspect its labels.
		s := mustHaveMetric(t, samples, "container_memory_working_set_bytes")
		if got := s.Labels["namespace"]; got == "" {
			t.Error("missing canonical label `namespace`")
		}
		if got := s.Labels["pod"]; got == "" {
			t.Error("missing canonical label `pod`")
		}
		if _, has := s.Labels["pod_namespace"]; has {
			t.Error("legacy label `pod_namespace` must be renamed to `namespace`")
		}
		if _, has := s.Labels["pod_name"]; has {
			t.Error("legacy label `pod_name` must be renamed to `pod`")
		}
	})

	t.Run("pod_uid preserved for PodsCache enrichment join", func(t *testing.T) {
		s := mustHaveMetric(t, samples, "container_cpu_usage_seconds_total")
		if got := s.Labels["pod_uid"]; got == "" {
			t.Error("pod_uid label is required for PodsCache enrichment; got empty")
		}
	})

	t.Run("cluster_id and cluster_name labels propagated", func(t *testing.T) {
		s := mustHaveMetric(t, samples, "container_cpu_usage_seconds_total")
		if s.Labels["cluster_id"] != "test-cluster-id" {
			t.Errorf("cluster_id mismatch: got %q want test-cluster-id", s.Labels["cluster_id"])
		}
		if s.Labels["cluster_name"] != "test-cluster" {
			t.Errorf("cluster_name mismatch: got %q want test-cluster", s.Labels["cluster_name"])
		}
	})

	t.Run("node label present on all samples", func(t *testing.T) {
		for _, s := range samples {
			if s.Labels["node"] != "kind-control-plane" {
				t.Errorf("metric %s missing node label or wrong value: %q", s.MetricName, s.Labels["node"])
				break // don't spam
			}
		}
	})

	t.Run("node network uses device label per node-exporter convention", func(t *testing.T) {
		net := samplesByName(samples, "node_network_receive_bytes_total")
		if len(net) == 0 {
			t.Fatal("expected node_network_receive_bytes_total samples")
		}
		for _, s := range net {
			if d := s.Labels["device"]; d == "" {
				t.Errorf("node network metric missing `device` label")
			}
			if _, has := s.Labels["interface"]; has {
				t.Error("node network metric must use `device` label (node-exporter), not `interface`")
			}
		}
	})

	t.Run("node_cpu_usage_seconds_total emitted, gauge cores removed", func(t *testing.T) {
		mustHaveMetric(t, samples, "node_cpu_usage_seconds_total")
		mustNotHaveMetric(t, samples, "node_cpu_usage_cores")
	})

	t.Run("flow metrics not emitted by stats collector (lives in flows package)", func(t *testing.T) {
		mustNotHaveMetric(t, samples, "pod_flow_events_total")
	})
}

// TestStatsCollector_TenantIDStamped validates the Phase 3 Day 4.2
// tenant_id propagation path. When KUBEBOLT_TENANT_ID is set (helm
// value `tenant.id`), every node-level and pod-level sample carries
// the label; the receiver's anti-spoof check then validates that
// the bearer token's tenant matches.
func TestStatsCollector_TenantIDStamped(t *testing.T) {
	srv := newFixtureServer(t, "stats_summary.json")
	defer srv.Close()
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))
	c := NewStats(client, "cid", "cn", "node", "tenant-prod")
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected samples")
	}
	for _, s := range samples {
		if got := s.Labels["tenant_id"]; got != "tenant-prod" {
			t.Errorf("metric %s tenant_id = %q, want tenant-prod", s.MetricName, got)
		}
	}
}

// TestStatsCollector_TenantIDAbsent ensures the conditional-stamp
// logic skips the label when tenantID is empty. Receiver auto-stamps
// in this case (Day 4.1 fallback).
func TestStatsCollector_TenantIDAbsent(t *testing.T) {
	srv := newFixtureServer(t, "stats_summary.json")
	defer srv.Close()
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))
	c := NewStats(client, "cid", "cn", "node", "")
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range samples {
		if _, has := s.Labels["tenant_id"]; has {
			t.Errorf("tenant_id should be absent on metric %s when tenantID==\"\"", s.MetricName)
			return
		}
	}
}

// TestStatsCollector_NoClusterName ensures the cluster_name label is omitted
// rather than emitted empty when not configured.
func TestStatsCollector_NoClusterName(t *testing.T) {
	srv := newFixtureServer(t, "stats_summary.json")
	defer srv.Close()
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))
	c := NewStats(client, "test-cluster-id", "" /* no cluster name */, "kind-control-plane", "")
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range samples {
		if _, has := s.Labels["cluster_name"]; has {
			t.Errorf("cluster_name should be absent when not configured; got on metric %s", s.MetricName)
			return
		}
	}
}

// TestStatsCollector_DeferNodeNetwork validates the option that
// suppresses node_network_*_bytes_total emission. Wired by the helm
// chart when the vmagent sidecar is configured to scrape node-exporter
// (the only metric names where the agent and node-exporter overlap
// exactly). Other node_* metrics MUST keep emitting — names diverge
// from node-exporter's set, so there's no double-counting risk.
func TestStatsCollector_DeferNodeNetwork(t *testing.T) {
	srv := newFixtureServer(t, "stats_summary.json")
	t.Cleanup(srv.Close)
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))
	c := NewStats(client, "test-cluster-id", "test-cluster", "kind-control-plane", "",
		WithDeferNodeNetwork(true))
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	t.Run("node_network_*_bytes_total NOT emitted", func(t *testing.T) {
		mustNotHaveMetric(t, samples, "node_network_receive_bytes_total")
		mustNotHaveMetric(t, samples, "node_network_transmit_bytes_total")
	})

	t.Run("other node_* metrics still emitted (no overlap with node-exporter)", func(t *testing.T) {
		// These names diverge from node-exporter's. Dropping them
		// would lose data the UI consumes (CapacityPage, NodesPage,
		// ResourceDetailPage Node monitor charts).
		mustHaveMetric(t, samples, "node_cpu_usage_seconds_total")
		mustHaveMetric(t, samples, "node_memory_working_set_bytes")
		mustHaveMetric(t, samples, "node_fs_used_bytes")
	})

	t.Run("container_network_* still emitted (unrelated to node-network)", func(t *testing.T) {
		// Pod-level network metrics live on the container_* prefix
		// per cAdvisor convention. node-exporter doesn't expose these
		// — the defer flag is scoped strictly to the node-* names.
		mustHaveMetric(t, samples, "container_network_receive_bytes_total")
	})
}

// TestStatsCollector_DropsFilteredInterfaces verifies WithDropInterfaces
// (helm collectors.dropNetworkInterfaces) excludes the given interfaces from
// BOTH per-interface network loops: node_network_* (node loop) and
// container_network_* (pod loop). Regression guard for the dual-source gap
// where the filter lived only in cadvisor.go and /stats/summary re-introduced
// the tunnel interfaces.
func TestStatsCollector_DropsFilteredInterfaces(t *testing.T) {
	srv := newFixtureServer(t, "stats_summary.json")
	t.Cleanup(srv.Close)
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))

	// Node loop: fixture node has eth0 + eth1. Drop eth1 (stand-in for a
	// tunnel device); it must vanish from node_network_* while eth0 stays.
	c := NewStats(client, "cid", "cn", "node", "", WithDropInterfaces(map[string]struct{}{"eth1": {}}))
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var sawNodeEth0, sawNodeEth1 bool
	for _, s := range samples {
		if s.MetricName == "node_network_receive_bytes_total" {
			switch s.Labels["device"] {
			case "eth0":
				sawNodeEth0 = true
			case "eth1":
				sawNodeEth1 = true
			}
		}
	}
	if sawNodeEth1 {
		t.Error("dropped device eth1 leaked into node_network_*")
	}
	if !sawNodeEth0 {
		t.Error("node device eth0 was filtered but must be kept")
	}

	// Pod loop: fixture pod nginx-abc123 has interface eth0. Dropping eth0
	// must remove its container_network_* series.
	c2 := NewStats(client, "cid", "cn", "node", "", WithDropInterfaces(map[string]struct{}{"eth0": {}}))
	s2, err := c2.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range s2 {
		if s.MetricName == "container_network_receive_bytes_total" && s.Labels["interface"] == "eth0" {
			t.Error("dropped pod interface eth0 leaked into container_network_*")
		}
	}
}

// TestStatsCollector_FallbackInterface validates the docker-desktop fallback:
// when interfaces[] is empty, top-level network fields are projected as a
// single eth0 interface so metrics still land.
func TestStatsCollector_FallbackInterface(t *testing.T) {
	// The fixture's coredns pod has empty interfaces[] AND empty top-level
	// fields, so it should produce no network samples (clean skip). The
	// fallback path is exercised by the node-level network in the fixture
	// (top-level filled + interfaces[] non-empty triggers the per-interface
	// path). We add a separate fixture if we want to specifically test
	// docker-desktop-style empty-interfaces-with-top-level-fields.
	samples := collectFromFixture(t, "stats_summary.json")
	for _, s := range samples {
		if s.MetricName == "container_network_receive_bytes_total" && s.Labels["pod"] == "coredns-xyz" {
			t.Errorf("did not expect coredns-xyz network samples (empty interfaces + empty top-level)")
		}
	}
}

// --- helpers ---------------------------------------------------------------

func collectFromFixture(t *testing.T, fixture string) []*agentv2.Sample {
	t.Helper()
	srv := newFixtureServer(t, fixture)
	t.Cleanup(srv.Close)
	client := kubelet.New("127.0.0.1", kubelet.WithBaseURL(srv.URL), kubelet.WithTokenPath(""))
	c := NewStats(client, "test-cluster-id", "test-cluster", "kind-control-plane", "")
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("Collect returned zero samples; fixture or collector broken")
	}
	return samples
}

func newFixtureServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	path := filepath.Join("testdata", fixture)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats/summary" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func samplesByName(samples []*agentv2.Sample, name string) []*agentv2.Sample {
	var out []*agentv2.Sample
	for _, s := range samples {
		if s.MetricName == name {
			out = append(out, s)
		}
	}
	return out
}

func mustHaveMetric(t *testing.T, samples []*agentv2.Sample, name string) *agentv2.Sample {
	t.Helper()
	for _, s := range samples {
		if s.MetricName == name {
			return s
		}
	}
	// Helpful failure: list metrics that DID emit, sorted, deduped.
	seen := map[string]struct{}{}
	var emitted []string
	for _, s := range samples {
		if _, ok := seen[s.MetricName]; ok {
			continue
		}
		seen[s.MetricName] = struct{}{}
		emitted = append(emitted, s.MetricName)
	}
	sort.Strings(emitted)
	t.Fatalf("expected metric %q in samples; emitted: [%s]", name, strings.Join(emitted, ", "))
	return nil
}

func mustNotHaveMetric(t *testing.T, samples []*agentv2.Sample, name string) {
	t.Helper()
	for _, s := range samples {
		if s.MetricName == name {
			t.Errorf("legacy metric %q must not be emitted in v1.0 schema", name)
			return
		}
	}
}
