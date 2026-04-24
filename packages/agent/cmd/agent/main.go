// kubebolt-agent — Sprint 1 Phase B.
//
// Pipeline (per node):
//
//   [kubelet /stats/summary] --collect 15s--> ┐
//                                             ├── enrich (pods cache) ──> [ring buffer] ──> [shipper] ──> gRPC stream
//   [kubelet /pods] --refresh 30s--> cache ───┘
//
// See internal/kubebolt-agent-technical-spec.md for the full design.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	"github.com/kubebolt/kubebolt/packages/agent/internal/collector"
	"github.com/kubebolt/kubebolt/packages/agent/internal/flows"
	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	"github.com/kubebolt/kubebolt/packages/agent/internal/shipper"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

const agentVersion = "0.0.7-cluster-ident"

func main() {
	backendURL := flag.String("backend", envOr("KUBEBOLT_BACKEND_URL", "localhost:9090"), "Backend gRPC address (host:port)")
	nodeName := flag.String("node", envOr("KUBEBOLT_AGENT_NODE_NAME", hostname()), "Node name (falls back to hostname)")
	nodeIP := flag.String("node-ip", envOr("KUBEBOLT_AGENT_NODE_IP", ""), "Node IP the kubelet listens on (downward API status.hostIP)")
	statsInterval := flag.Duration("stats-interval", 15*time.Second, "How often to poll kubelet /stats/summary")
	podsInterval := flag.Duration("pods-interval", 30*time.Second, "How often to refresh the pods metadata cache")
	bufferSize := flag.Int("buffer", 10_000, "Max samples buffered in memory before oldest are dropped")
	logLevel := flag.String("log-level", envOr("KUBEBOLT_AGENT_LOG_LEVEL", "info"), "Log level: debug|info|warn|error")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)})))

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		slog.Info("signal received, shutting down", slog.String("signal", sig.String()))
		rootCancel()
	}()

	kc := kubelet.New(*nodeIP)
	slog.Info("kubelet target", slog.String("url", kc.BaseURL()))

	clusterID, clusterName := resolveClusterIdent(rootCtx)
	slog.Info("cluster identity",
		slog.String("cluster_id", clusterID),
		slog.String("cluster_name", clusterName),
	)

	pods := collector.NewPods(kc)
	stats := collector.NewStats(kc, clusterID, clusterName, *nodeName)
	cadvisor := collector.NewCadvisor(kc, clusterID, clusterName, *nodeName)
	buf := buffer.New(*bufferSize)
	ship := shipper.New(*backendURL, *nodeName, agentVersion, buf)

	var wg sync.WaitGroup

	// Pods metadata refresher.
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Initial refresh so the first stats batch has enrichment data.
		if err := pods.Refresh(rootCtx); err != nil {
			slog.Warn("initial pods refresh failed", slog.String("error", err.Error()))
		} else {
			slog.Info("pods cache primed", slog.Int("pods", pods.Size()))
		}
		tick := time.NewTicker(*podsInterval)
		defer tick.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-tick.C:
				if err := pods.Refresh(rootCtx); err != nil {
					slog.Warn("pods refresh failed", slog.String("error", err.Error()))
				}
			}
		}
	}()

	// Stats collector.
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Send a first batch immediately so VM has data within seconds.
		collectAndBuffer(rootCtx, stats, pods, buf)
		tick := time.NewTicker(*statsInterval)
		defer tick.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-tick.C:
				collectAndBuffer(rootCtx, stats, pods, buf)
			}
		}
	}()

	// cAdvisor network collector — runs on the same cadence as stats.
	// Complements /stats/summary for kubelets that don't populate the
	// pod-level network block (e.g. docker-desktop).
	wg.Add(1)
	go func() {
		defer wg.Done()
		collectAndBuffer(rootCtx, cadvisor, pods, buf)
		tick := time.NewTicker(*statsInterval)
		defer tick.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-tick.C:
				collectAndBuffer(rootCtx, cadvisor, pods, buf)
			}
		}
	}()

	// Shipper — reconnects internally on failure.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ship.Run(rootCtx)
	}()

	// Hubble flow collector (Phase 2.1 Level 2). Elects a single-pod
	// leader via a Lease in the agent's own namespace and only that pod
	// streams from Hubble Relay; other pods stand by. Silent no-op when
	// we're not in-cluster (dev runs on host) or when the cluster
	// doesn't have Cilium installed.
	if leaseNs, err := flows.ResolveLeaseNamespace(); err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			flows.RunLeaderElectedCollector(rootCtx, buf, clusterID, clusterName, *nodeName, leaseNs)
		}()
	} else {
		slog.Debug("hubble: skipping flow collector (no lease namespace)",
			slog.String("reason", err.Error()))
	}

	// Periodic buffer stats log (every minute) — lets you see drops if they happen.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-tick.C:
				collected, dropped, current, capacity := buf.Stats()
				slog.Info("buffer stats",
					slog.Uint64("collected_total", collected),
					slog.Uint64("dropped_total", dropped),
					slog.Int("current", current),
					slog.Int("capacity", capacity),
					slog.Int("pods_cached", pods.Size()),
				)
			}
		}
	}()

	<-rootCtx.Done()
	slog.Info("waiting for goroutines to drain")
	wg.Wait()
	slog.Info("agent stopped")
}

// Collector is the minimal interface satisfied by stats and cadvisor
// collectors. Lets collectAndBuffer work with any source of samples.
type Collector interface {
	Name() string
	Collect(ctx context.Context) ([]*agentv1.Sample, error)
}

func collectAndBuffer(ctx context.Context, c Collector, pods *collector.PodsCache, buf *buffer.Ring) {
	samples, err := c.Collect(ctx)
	if err != nil {
		slog.Warn("collect failed", slog.String("collector", c.Name()), slog.String("error", err.Error()))
		return
	}
	pods.Enrich(samples)
	buf.Push(samples)
	// Info-level so the Phase B / Phase C bring-up is observable without
	// flipping log level. Gets noisy on steady state; revisit when we have
	// more than two collectors.
	slog.Info("samples collected", slog.String("collector", c.Name()), slog.Int("count", len(samples)))
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hostname() string {
	if n, err := os.Hostname(); err == nil {
		return n
	}
	if out, err := exec.Command("uname", "-n").Output(); err == nil {
		return string(out)
	}
	return "unknown-node"
}

// resolveClusterIdent determines the (cluster_id, cluster_name) pair
// that every sample this agent emits gets tagged with. The ID is the
// cornerstone of multi-cluster correctness — two agents running in
// different clusters must have different IDs, otherwise VM sums their
// samples together and dashboards lie.
//
// Priority for cluster_id:
//  1. KUBEBOLT_AGENT_CLUSTER_ID env var (operator override, e.g. to
//     migrate legacy installs that used "local" before this feature
//     existed).
//  2. Auto-discover: read the `kube-system` namespace UID from the
//     apiserver. Every K8s cluster has a unique, immutable UID there,
//     so no two clusters can ever collide.
//  3. Fallback to "local" when we can't reach the apiserver (e.g.
//     dev-mode host run without in-cluster credentials). Emits a
//     warn-level log so the operator notices.
//
// cluster_name is a pure display label, set via
// KUBEBOLT_AGENT_CLUSTER_NAME, empty when not configured. The UI uses
// whatever the backend knows from kubeconfig context instead, so this
// is mostly for operators who query VM directly.
func resolveClusterIdent(ctx context.Context) (clusterID, clusterName string) {
	clusterName = os.Getenv("KUBEBOLT_AGENT_CLUSTER_NAME")

	if override := os.Getenv("KUBEBOLT_AGENT_CLUSTER_ID"); override != "" {
		return override, clusterName
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Warn("cluster_id: no in-cluster config, falling back to 'local'",
			slog.String("error", err.Error()))
		return "local", clusterName
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Warn("cluster_id: kube client init failed, falling back to 'local'",
			slog.String("error", err.Error()))
		return "local", clusterName
	}
	// 5s is plenty for a single GET against the local apiserver; longer
	// would delay agent startup on a cluster with a flaky control plane.
	discoverCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ns, err := client.CoreV1().Namespaces().Get(discoverCtx, "kube-system", metav1.GetOptions{})
	if err != nil {
		slog.Warn("cluster_id: failed to read kube-system UID, falling back to 'local'",
			slog.String("error", err.Error()))
		return "local", clusterName
	}
	return string(ns.UID), clusterName
}
