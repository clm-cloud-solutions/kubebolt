package collector

import (
	"context"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
)

// RunLeaderElectedExporters runs the exporter scrape loop behind a
// Kubernetes Lease so only ONE agent pod cluster-wide scrapes the
// configured exporters. Exporters are single Services (OpenCost runs
// one Deployment) — without the election, a DaemonSet's N pods would
// each scrape the same endpoint: N× scrape load on the exporter, N×
// shipper bandwidth, N duplicate streams for VM to dedup. Same
// Lease-based pattern as the Hubble flows collector and the Mode C
// promread reader.
//
// pods is optional metadata enrichment (adds workload owner labels to
// samples that carry namespace/pod — OpenCost's container_* families
// do); pass nil to skip.
//
// Blocks until ctx is canceled. Callers run it in a goroutine.
func RunLeaderElectedExporters(
	ctx context.Context,
	exporters []*Exporter,
	buf *buffer.Ring,
	pods *PodsCache,
	interval time.Duration,
	leaseNamespace, nodeName string,
) {
	if len(exporters) == 0 {
		return
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Info("exporters: in-cluster config not available, skipping exporter scraping",
			slog.String("reason", err.Error()))
		return
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Warn("exporters: kube client init failed", slog.String("error", err.Error()))
		return
	}

	identity := os.Getenv("POD_NAME")
	if identity == "" {
		identity = nodeName
	}
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      "kubebolt-exporter-scraper",
			Namespace: leaseNamespace,
		},
		Client: kube.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}

	runOnce := func(ctx context.Context) {
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			ReleaseOnCancel: true,
			// Standard K8s lease timing (same as flows/promread):
			// rotates within a minute if the leader crashes, which is
			// fine for cost/exporter data on a 30s scrape cadence.
			LeaseDuration: 15 * time.Second,
			RenewDeadline: 10 * time.Second,
			RetryPeriod:   2 * time.Second,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					slog.Info("exporters: acquired scraper lease",
						slog.String("identity", identity),
						slog.Int("exporters", len(exporters)))
					scrapeLoop(leaderCtx, exporters, buf, pods, interval)
				},
				OnStoppedLeading: func() {
					slog.Info("exporters: lost scraper lease", slog.String("identity", identity))
				},
				OnNewLeader: func(leader string) {
					if leader != identity {
						slog.Info("exporters: scraper leader", slog.String("pod", leader))
					}
				},
			},
		})
	}

	// Election retry loop. RunOrDie RETURNS when the lease is lost —
	// without this loop, one renew-deadline blip would kill exporter
	// scraping until the pod restarts (the exact failure the flows
	// collector hit in production: dead for two days after one blip).
	// Fixed short sleep instead of adaptive backoff: the election
	// primitives already pace themselves via RetryPeriod.
	for {
		runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// scrapeLoop ticks every interval while this pod holds the lease,
// collecting each exporter independently — one broken exporter must
// not starve the others (its error is logged; the loop moves on).
// First scrape fires immediately so a fresh leader doesn't sit idle
// for a full interval.
func scrapeLoop(ctx context.Context, exporters []*Exporter, buf *buffer.Ring, pods *PodsCache, interval time.Duration) {
	scrapeAll := func() {
		for _, e := range exporters {
			samples, err := e.Collect(ctx)
			if err != nil {
				slog.Warn("collect failed", slog.String("collector", e.Name()), slog.String("error", err.Error()))
				continue
			}
			if pods != nil {
				pods.Enrich(samples)
			}
			buf.Push(samples)
			slog.Info("samples collected", slog.String("collector", e.Name()), slog.Int("count", len(samples)))
		}
	}

	scrapeAll()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			scrapeAll()
		}
	}
}
