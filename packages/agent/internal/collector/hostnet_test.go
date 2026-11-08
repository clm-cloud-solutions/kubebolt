package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// podsJSON is the kubelet /pods answer for the two pods in the fixtures:
// nginx-abc123 runs with hostNetwork, coredns-xyz does not.
const podsJSON = `{"items":[
 {"metadata":{"name":"nginx-abc123","namespace":"default","uid":"11111111-aaaa-bbbb-cccc-111111111111"},"spec":{"hostNetwork":true}},
 {"metadata":{"name":"coredns-xyz","namespace":"kube-system","uid":"22222222-aaaa-bbbb-cccc-222222222222"},"spec":{}}
]}`

// podsCacheFrom serves body as /pods and counts the calls.
func podsCacheFrom(t *testing.T, body string) (*PodsCache, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pods" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewPods(testKubelet(srv.URL)), &calls
}

func TestPodsCache_IsHostNetwork(t *testing.T) {
	pods, _ := podsCacheFrom(t, podsJSON)
	if err := pods.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !pods.IsHostNetwork("default", "nginx-abc123") {
		t.Error("nginx-abc123 has spec.hostNetwork=true; IsHostNetwork said false")
	}
	if pods.IsHostNetwork("kube-system", "coredns-xyz") {
		t.Error("coredns-xyz has no hostNetwork; IsHostNetwork said true")
	}
	if pods.IsHostNetwork("default", "not-cached") {
		t.Error("a pod the cache has not seen must keep its network (false)")
	}
	var nilCache *PodsCache
	if nilCache.IsHostNetwork("default", "nginx-abc123") {
		t.Error("nil cache must answer false")
	}
}

// TestHostNetworkPods_SendNoPodNetwork: neither collector sends pod network
// for a hostNetwork pod (its interfaces are the node's), and everything else
// is untouched: its CPU and memory, other pods' network, the node's network.
func TestHostNetworkPods_SendNoPodNetwork(t *testing.T) {
	pods, _ := podsCacheFrom(t, podsJSON)
	if err := pods.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	seen := NewPodNetworkSeen()
	srv := newFixtureServer(t, "stats_summary.json")
	t.Cleanup(srv.Close)
	fromStats, err := NewStats(testKubelet(srv.URL), "cid", "cn", "node", "",
		WithPodNetworkSeen(seen), WithHostNetworkPods(pods)).Collect(context.Background())
	if err != nil {
		t.Fatalf("stats Collect: %v", err)
	}
	fromCadvisor := collectCadvisor(t, "cadvisor_metrics.txt", WithStatsNetwork(seen), WithPods(pods))

	var nginxCPU, nodeNet, corednsNet bool
	for _, s := range append(fromStats, fromCadvisor...) {
		isPodNet := strings.HasPrefix(s.MetricName, "container_network_")
		switch {
		case isPodNet && s.Labels["pod"] == "nginx-abc123":
			t.Errorf("%s %s sent for hostNetwork pod nginx-abc123", s.MetricName, s.Labels["interface"])
		case isPodNet && s.Labels["pod"] == "coredns-xyz":
			corednsNet = true
		case s.MetricName == "container_cpu_usage_seconds_total" && s.Labels["pod"] == "nginx-abc123":
			nginxCPU = true
		case strings.HasPrefix(s.MetricName, "node_network_"):
			nodeNet = true
		}
	}
	if !nginxCPU {
		t.Error("the hostNetwork pod lost its CPU samples; only its network must go")
	}
	if !corednsNet {
		t.Error("coredns-xyz (no hostNetwork) lost its network")
	}
	if !nodeNet {
		t.Error("node_network_* missing; the node's traffic must still be reported")
	}
}

// TestStats_RefreshesPodsCacheForAPodItDoesNotKnow: a pod that started since
// the last refresh is recognised as hostNetwork in the same pass, and a pass
// where every pod is known makes no extra /pods call.
func TestStats_RefreshesPodsCacheForAPodItDoesNotKnow(t *testing.T) {
	pods, calls := podsCacheFrom(t, podsJSON)
	srv := newFixtureServer(t, "stats_summary.json")
	t.Cleanup(srv.Close)
	stats := NewStats(testKubelet(srv.URL), "cid", "cn", "node", "", WithHostNetworkPods(pods))

	samples, err := stats.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("/pods called %d times on the first pass, want 1", calls.Load())
	}
	for _, s := range samples {
		if strings.HasPrefix(s.MetricName, "container_network_") && s.Labels["pod"] == "nginx-abc123" {
			t.Fatalf("hostNetwork pod sent %s on the pass that discovered it", s.MetricName)
		}
	}

	if _, err := stats.Collect(context.Background()); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("/pods called again (%d total) with every pod already known", calls.Load())
	}
}

// TestStats_UnreachablePodsKeepsNetwork: without the pods cache the agent
// cannot tell a hostNetwork pod; it keeps every pod's network rather than
// losing any.
func TestStats_UnreachablePodsKeepsNetwork(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kubelet busy", http.StatusServiceUnavailable)
	}))
	t.Cleanup(failing.Close)
	pods := NewPods(testKubelet(failing.URL))
	srv := newFixtureServer(t, "stats_summary.json")
	t.Cleanup(srv.Close)
	samples, err := NewStats(testKubelet(srv.URL), "cid", "cn", "node", "", WithHostNetworkPods(pods)).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range samples {
		if s.MetricName == "container_network_receive_bytes_total" && s.Labels["pod"] == "nginx-abc123" {
			return
		}
	}
	t.Error("nginx-abc123 lost its network although the pods cache could not say it is hostNetwork")
}
