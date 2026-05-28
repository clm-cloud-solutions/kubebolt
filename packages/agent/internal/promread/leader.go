package promread

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	"github.com/kubebolt/kubebolt/packages/agent/internal/collector"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// LeaseName is the namespace-scoped Lease object name promread elects
// against. Different from flows' "kubebolt-flow-collector" so both
// can elect independently — a single pod can hold both leases at
// once or one each; K8s doesn't care.
const LeaseName = "kubebolt-promread"

// emitLeaderStatus writes a single sample marking whether THIS pod
// holds the promread Lease. One series per agent pod: non-leaders
// hold steady at 0, the leader's series flips to 1. Mirrors the
// flows.emitLeaderStatus pattern — see that file's "Known artifact"
// note about VM lookback retention.
func emitLeaderStatus(buf *buffer.Ring, clusterID, clusterName, nodeName, podName, tenantID string, leading bool) {
	labels := map[string]string{
		"cluster_id": clusterID,
		"node":       nodeName,
		"pod":        podName,
	}
	if clusterName != "" {
		labels["cluster_name"] = clusterName
	}
	if tenantID != "" {
		labels["tenant_id"] = tenantID
	}
	value := 0.0
	if leading {
		value = 1.0
	}
	buf.Push([]*agentv2.Sample{{
		Timestamp:  timestamppb.Now(),
		MetricName: "kubebolt_promread_leader",
		Value:      value,
		Labels:     labels,
	}})
}

// RunLeaderElectedReader starts the promread Reader behind a
// Kubernetes Lease so only ONE agent pod in the cluster polls the
// customer's Prometheus at a time. The other pods stand by, ready
// to take over if the leader dies.
//
// Why: Mode C reads from a CENTRAL Prom that aggregates ALL nodes.
// If every DaemonSet pod polled it independently, N nodes → N× query
// load on the customer's Prom (real $$$ on AMP/Azure/GMP), N× CPU,
// N× shipper bandwidth, and a stream of near-duplicate samples that
// can disturb rate()/increase() in the UI when timestamps drift.
// Lease-elected leader keeps the load to 1× regardless of node count.
//
// Same pattern as flows.RunLeaderElectedCollector — different Lease
// name (kubebolt-promread vs kubebolt-flow-collector) so the two
// elect independently. A single pod can hold both leases or one
// each; K8s doesn't care.
func RunLeaderElectedReader(
	ctx context.Context,
	reader *Reader,
	buf *buffer.Ring,
	pods *collector.PodsCache,
	kube kubernetes.Interface,
	clusterID, clusterName, nodeName, leaseNamespace, tenantID string,
) {
	identity := os.Getenv("POD_NAME")
	if identity == "" {
		identity = nodeName
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      LeaseName,
			Namespace: leaseNamespace,
		},
		Client: kube.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}

	// Nested context so OnStoppedLeading can cancel the poll loop
	// without cancelling the outer agent context.
	var pollCancel context.CancelFunc

	// Initial heartbeat: this pod is alive but not yet leading.
	// Dashboards don't go blank during the election phase.
	emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, tenantID, false)

	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	var leadingNow bool
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-tick.C:
				emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, tenantID, leadingNow)
			}
		}
	}()

	runOnce := func(ctx context.Context) {
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			ReleaseOnCancel: true,
			// Standard K8s defaults — same as flows. Rotates within a
			// minute if the leader crashes, which for promread means at
			// most one missed poll cycle (default 30s) before a new
			// leader picks up.
			LeaseDuration: 15 * time.Second,
			RenewDeadline: 10 * time.Second,
			RetryPeriod:   2 * time.Second,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					slog.Info("promread: acquired reader lease",
						slog.String("identity", identity),
						slog.Duration("poll_interval", reader.PollInterval()))
					leadingNow = true
					emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, tenantID, true)
					pollCtx, cancel := context.WithCancel(leaderCtx)
					pollCancel = cancel
					runPollLoop(pollCtx, reader, pods, buf)
				},
				OnStoppedLeading: func() {
					slog.Info("promread: lost reader lease", slog.String("identity", identity))
					leadingNow = false
					emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, tenantID, false)
					if pollCancel != nil {
						pollCancel()
						pollCancel = nil
					}
				},
				OnNewLeader: func(leader string) {
					if leader != identity {
						slog.Info("promread: reader leader", slog.String("pod", leader))
					}
				},
			},
		})
	}

	// Backoff retry loop — same shape as flows.runElectionLoop. Without
	// this, a transient apiserver renew failure would drop the lease
	// permanently and Mode C would stop ingesting until pod restart.
	runElectionLoop(ctx, runOnce, 30*time.Second, time.Second, 30*time.Second)
}

// runPollLoop fires Reader.Collect on the reader's PollInterval. Runs
// while ctx (the leader-context) is alive; returns on cancellation.
// Mirrors the cadvisor / stats / self-metrics goroutine pattern in
// main.go's collectAndBuffer but inlined here because the lifecycle
// is tied to leadership, not to the agent's global goroutine pool.
func runPollLoop(ctx context.Context, reader *Reader, pods *collector.PodsCache, buf *buffer.Ring) {
	// Immediate first batch so VM gets data within seconds of the
	// lease acquisition, not on the next tick.
	collectOnce(ctx, reader, pods, buf)
	tick := time.NewTicker(reader.PollInterval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			collectOnce(ctx, reader, pods, buf)
		}
	}
}

// collectOnce iterates matchers and pushes EACH matcher's result to
// the ring buffer separately rather than accumulating all matchers
// into one mega-batch.
//
// Why per-matcher: a single Collect over 4 matchers in a multi-node
// cluster can produce 30k+ samples. Pushing that as one batch to a
// 10k-default ring buffer triggers Ring.Push's overflow path which
// drops the alphabetically-earliest samples in the batch (sort order
// from Convert). In practice that silently murders the middle of
// matcher 3 (node_*) — node_load*, node_cpu_*, node_memory_* all
// disappear; only the late-alphabet node_netstat_*, node_namespace_*
// survive. By pushing per-matcher (~5-7k each) we stay well under
// buffer capacity and the shipper drains between pushes.
//
// Surfaced by the S1 multi-node kind smoke 2026-05-26 — single-node
// smoke didn't trigger it because total samples (~12k) only modestly
// exceeded the buffer.
func collectOnce(ctx context.Context, reader *Reader, pods *collector.PodsCache, buf *buffer.Ring) {
	totalSamples := 0
	for _, matcher := range reader.Matchers() {
		samples, err := reader.CollectMatcher(ctx, matcher)
		if err != nil {
			slog.Warn("promread matcher failed",
				slog.String("matcher", matcher),
				slog.String("error", err.Error()))
			continue
		}
		if len(samples) == 0 {
			continue
		}
		// pods is nil in Deployment topology (1.13 split): the promread
		// Deployment pod doesn't run a kubelet pods cache because its
		// samples already arrive with pod/namespace labels from Prom
		// and don't carry a pod_uid the cache could enrich anyway.
		if pods != nil {
			pods.Enrich(samples)
		}
		buf.Push(samples)
		totalSamples += len(samples)
	}
	if totalSamples > 0 {
		slog.Info("samples collected",
			slog.String("collector", "promread"),
			slog.Int("count", totalSamples))
	}
}

// runElectionLoop calls `attempt` repeatedly until ctx is canceled.
// Each invocation runs one full election cycle: win the lease, hold
// it as long as renewals succeed, lose it (or never win), return.
//
// Identical pattern to flows.runElectionLoop — duplicated here to
// avoid the cross-package dep. If both grow features in the same
// direction, consider extracting to a shared internal/leaderelect
// package.
//
// Backoff strategy:
//   - Starts at backoffInitial, doubles after each cycle, caps at
//     backoffMax.
//   - Resets to backoffInitial after any cycle that held the lease
//     for at least minHeldToReset. Distinguishes "we lost a real
//     lease after holding it" (transient blip — try again with one
//     short wait) from "we never won and the loop is spinning"
//     (escalate to backoffMax to avoid hammering the apiserver).
func runElectionLoop(
	ctx context.Context,
	attempt func(context.Context),
	minHeldToReset time.Duration,
	backoffInitial, backoffMax time.Duration,
) {
	backoff := backoffInitial
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		attempt(ctx)
		held := time.Since(start)
		if ctx.Err() != nil {
			return
		}
		if held >= minHeldToReset {
			slog.Info("promread: election cycle ended after a real term, resetting backoff",
				slog.Duration("held", held))
			backoff = backoffInitial
		} else {
			slog.Info("promread: election cycle ended quickly, will re-attempt with backoff",
				slog.Duration("held", held),
				slog.Duration("backoff", backoff))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

// ResolveLeaseNamespace returns the namespace the Lease should live
// in. Tries POD_NAMESPACE (downward API) first, then falls back to
// the mounted ServiceAccount namespace file. Mirrors
// flows.ResolveLeaseNamespace — duplicated to keep promread free of
// the flows package dep.
func ResolveLeaseNamespace() (string, error) {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("resolve lease namespace: %w", err)
	}
	return string(data), nil
}
