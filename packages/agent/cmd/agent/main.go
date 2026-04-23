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

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	"github.com/kubebolt/kubebolt/packages/agent/internal/collector"
	"github.com/kubebolt/kubebolt/packages/agent/internal/kubelet"
	"github.com/kubebolt/kubebolt/packages/agent/internal/shipper"
)

const agentVersion = "0.0.3-phaseB"

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

	pods := collector.NewPods(kc)
	stats := collector.NewStats(kc, "local", *nodeName)
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

	// Shipper — reconnects internally on failure.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ship.Run(rootCtx)
	}()

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

func collectAndBuffer(ctx context.Context, stats *collector.StatsCollector, pods *collector.PodsCache, buf *buffer.Ring) {
	samples, err := stats.Collect(ctx)
	if err != nil {
		slog.Warn("stats collect failed", slog.String("error", err.Error()))
		return
	}
	pods.Enrich(samples)
	buf.Push(samples)
	slog.Debug("samples collected", slog.Int("count", len(samples)))
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
