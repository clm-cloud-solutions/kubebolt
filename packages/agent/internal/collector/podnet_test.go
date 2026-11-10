package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

type podNetSample struct{ metric, namespace, pod, iface string }

func keyOf(s *agentv2.Sample) podNetSample {
	return podNetSample{s.MetricName, s.Labels["namespace"], s.Labels["pod"], s.Labels["interface"]}
}

// TestStatsThenCadvisor_EachPodInterfaceOnce runs the two collectors the way
// the agent's loop does (cmd/agent/main.go): stats, then cAdvisor, sharing a
// PodNetworkSeen. Together they must send each (metric, namespace, pod,
// interface) once, and lose nothing stats left out.
func TestStatsThenCadvisor_EachPodInterfaceOnce(t *testing.T) {
	seen := NewPodNetworkSeen()
	fromStats := collectStats(t, newFixtureServer(t, "stats_summary.json"), seen)
	fromCadvisor := collectCadvisor(t, "cadvisor_metrics.txt", WithStatsNetwork(seen))

	sentBy := map[podNetSample][]string{}
	for _, s := range fromStats {
		if strings.HasPrefix(s.MetricName, "container_network_") {
			sentBy[keyOf(s)] = append(sentBy[keyOf(s)], "stats")
		}
	}
	for _, s := range fromCadvisor {
		sentBy[keyOf(s)] = append(sentBy[keyOf(s)], "cadvisor")
	}
	for k, from := range sentBy {
		if len(from) > 1 {
			t.Errorf("%+v sent %d times (%v), want once", k, len(from), from)
		}
	}

	for _, tc := range []struct {
		why  string
		k    podNetSample
		from string
	}{
		{"stats reports nginx eth0", podNetSample{"container_network_receive_bytes_total", "default", "nginx-abc123", "eth0"}, "stats"},
		{"stats reports nginx eth0", podNetSample{"container_network_transmit_bytes_total", "default", "nginx-abc123", "eth0"}, "stats"},
		{"stats reports nginx eth0", podNetSample{"container_network_receive_errors_total", "default", "nginx-abc123", "eth0"}, "stats"},
		{"stats reports nginx eth0", podNetSample{"container_network_transmit_errors_total", "default", "nginx-abc123", "eth0"}, "stats"},
		{"the summary has no packets_dropped", podNetSample{"container_network_receive_packets_dropped_total", "default", "nginx-abc123", "eth0"}, "cadvisor"},
		{"the summary has no packets_dropped", podNetSample{"container_network_transmit_packets_dropped_total", "default", "nginx-abc123", "eth0"}, "cadvisor"},
		{"a second interface stats does not list is real traffic", podNetSample{"container_network_receive_bytes_total", "default", "nginx-abc123", "net1"}, "cadvisor"},
		{"a second interface stats does not list is real traffic", podNetSample{"container_network_transmit_bytes_total", "default", "nginx-abc123", "net1"}, "cadvisor"},
		{"stats has no network for coredns", podNetSample{"container_network_receive_bytes_total", "kube-system", "coredns-xyz", "eth0"}, "cadvisor"},
		{"stats has no network for coredns", podNetSample{"container_network_transmit_bytes_total", "kube-system", "coredns-xyz", "eth0"}, "cadvisor"},
	} {
		from := sentBy[tc.k]
		if len(from) != 1 || from[0] != tc.from {
			t.Errorf("%+v sent by %v, want only %s (%s)", tc.k, from, tc.from, tc.why)
		}
	}
}

// TestCadvisor_SendsEverythingAfterAFailedStatsPass: a stats pass that fails
// reported nothing, so it must not make cAdvisor skip anything.
func TestCadvisor_SendsEverythingAfterAFailedStatsPass(t *testing.T) {
	seen := NewPodNetworkSeen()
	collectStats(t, newFixtureServer(t, "stats_summary.json"), seen)

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kubelet busy", http.StatusServiceUnavailable)
	}))
	t.Cleanup(failing.Close)
	stats := NewStats(testKubelet(failing.URL), "cid", "cn", "node", "", WithPodNetworkSeen(seen))
	if _, err := stats.Collect(context.Background()); err == nil {
		t.Fatal("expected the failing stats pass to return an error")
	}

	fromCadvisor := collectCadvisor(t, "cadvisor_metrics.txt", WithStatsNetwork(seen))
	want := podNetSample{"container_network_receive_bytes_total", "default", "nginx-abc123", "eth0"}
	for _, s := range fromCadvisor {
		if keyOf(s) == want {
			return
		}
	}
	t.Errorf("cAdvisor skipped %+v after a failed stats pass; nothing else reports it", want)
}

// TestCadvisor_NilStatsNetworkSendsEverything keeps the old behaviour when
// the option is not wired.
func TestCadvisor_NilStatsNetworkSendsEverything(t *testing.T) {
	all := collectCadvisor(t, "cadvisor_metrics.txt")
	withEmpty := collectCadvisor(t, "cadvisor_metrics.txt", WithStatsNetwork(NewPodNetworkSeen()))
	if len(all) != len(withEmpty) {
		t.Errorf("an empty PodNetworkSeen changed the output: %d samples vs %d", len(withEmpty), len(all))
	}
}

// TestCadvisor_PodUIDOnlyOnFamiliesStatsCannotSend: packets_dropped gets the
// pod_uid PodsCache.Enrich needs for the workload labels. Bytes and errors
// never do, or a pod stats reports from its next pass on would get two
// readings in one counter series.
func TestCadvisor_PodUIDOnlyOnFamiliesStatsCannotSend(t *testing.T) {
	pods := &PodsCache{uids: map[podKey]string{
		{"default", "nginx-abc123"}:    "11111111-aaaa-bbbb-cccc-111111111111",
		{"kube-system", "coredns-xyz"}: "22222222-aaaa-bbbb-cccc-222222222222",
	}}
	samples := collectCadvisor(t, "cadvisor_metrics.txt", WithPods(pods))

	var dropped int
	for _, s := range samples {
		_, cadvisorOnly := cadvisorOnlyMetrics[s.MetricName]
		uid := s.Labels["pod_uid"]
		switch {
		case cadvisorOnly && uid != "11111111-aaaa-bbbb-cccc-111111111111":
			t.Errorf("%s for %s: pod_uid = %q, want nginx's UID", s.MetricName, s.Labels["pod"], uid)
		case cadvisorOnly:
			dropped++
		case uid != "":
			t.Errorf("%s for %s/%s carries pod_uid %q; only cAdvisor-only families may", s.MetricName, s.Labels["pod"], s.Labels["interface"], uid)
		}
	}
	if dropped != 2 {
		t.Errorf("got %d packets_dropped samples with pod_uid, want 2", dropped)
	}
}

func TestPodsCache_UIDFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pods" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"web-1","namespace":"shop","uid":"uid-web-1"}},{"metadata":{"name":"no-uid","namespace":"shop"}}]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewPods(testKubelet(srv.URL))
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := c.UIDFor("shop", "web-1"); got != "uid-web-1" {
		t.Errorf("UIDFor(shop, web-1) = %q, want uid-web-1", got)
	}
	if got := c.UIDFor("other", "web-1"); got != "" {
		t.Errorf("UIDFor(other, web-1) = %q, want empty: the namespace is part of the key", got)
	}
	if got := c.UIDFor("shop", "no-uid"); got != "" {
		t.Errorf("UIDFor(shop, no-uid) = %q, want empty", got)
	}
	var nilCache *PodsCache
	if got := nilCache.UIDFor("shop", "web-1"); got != "" {
		t.Errorf("nil cache UIDFor = %q, want empty", got)
	}
}

// --- helpers ---------------------------------------------------------------

func testKubelet(url string) *kubelet.Client {
	return kubelet.New("127.0.0.1", kubelet.WithBaseURL(url), kubelet.WithTokenPath(""))
}

func collectStats(t *testing.T, srv *httptest.Server, seen *PodNetworkSeen) []*agentv2.Sample {
	t.Helper()
	t.Cleanup(srv.Close)
	samples, err := NewStats(testKubelet(srv.URL), "cid", "cn", "node", "", WithPodNetworkSeen(seen)).Collect(context.Background())
	if err != nil {
		t.Fatalf("stats Collect: %v", err)
	}
	return samples
}

func collectCadvisor(t *testing.T, fixture string, opts ...CadvisorOption) []*agentv2.Sample {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
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
	samples, err := NewCadvisor(testKubelet(srv.URL), "cid", "cn", "node", "", nil, opts...).Collect(context.Background())
	if err != nil {
		t.Fatalf("cadvisor Collect: %v", err)
	}
	return samples
}
