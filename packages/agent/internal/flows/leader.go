package flows

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
)

// emitLeaderStatus writes a single sample to the ring buffer marking
// whether this pod currently holds the flow-collector Lease. One series
// per agent pod: series for non-leaders hold steady at 0, the leader's
// series flips to 1. Handy for dashboards that need to show which pod
// owns the stream, and for debugging lease churn in SaaS.
//
// Known artifact: when a pod is SIGTERM'd, OnStoppedLeading fires and
// emits a final value=0, but the shipper → backend → VM path is
// asynchronous and may not drain before the pod exits. In that case
// the dead pod's last sample (value=1) sticks around for up to 5 min
// (VM lookback). K8s Lease is the source of truth; consumers that
// need "actual current leader" should filter by sample age or combine
// with the Lease object. Acceptable for now; a future iteration can
// add a synchronous buffer-flush on shutdown if needed.
func emitLeaderStatus(buf *buffer.Ring, clusterID, clusterName, nodeName, podName string, leading bool) {
	labels := map[string]string{
		"cluster_id": clusterID,
		"node":       nodeName,
		"pod":        podName,
	}
	if clusterName != "" {
		labels["cluster_name"] = clusterName
	}
	value := 0.0
	if leading {
		value = 1.0
	}
	buf.Push([]*agentv1.Sample{{
		Timestamp:  timestamppb.Now(),
		MetricName: "kubebolt_flow_collector_leader",
		Value:      value,
		Labels:     labels,
	}})
}

// RunLeaderElectedCollector starts the Hubble flow collector behind a
// Kubernetes Lease so only one agent pod in the cluster is streaming
// from the relay at a time. Hubble Relay is cluster-wide; N agents all
// scraping it would N-times-count every flow.
//
// The caller (main.go) passes the agent's ring buffer so collected
// samples flow into the same gRPC shipment path the kubelet collectors
// already use. No backend changes needed — pod_flow_events_total is
// just another sample.
//
// Relay detection:
//   - If KUBEBOLT_HUBBLE_RELAY_ADDR is set, use it (override for custom
//     installs: non-default namespace, different Service name, mTLS
//     sidecar, etc.).
//   - Otherwise default to hubble-relay.kube-system.svc.cluster.local:80
//     which is what a stock `cilium hubble enable` produces.
//
// Returns cleanly (logging, no panic) when the cluster doesn't run
// Cilium — the leader elects fine but the stream fails fast, the
// collector logs and exits, and the agent keeps doing its main work.
func RunLeaderElectedCollector(
	ctx context.Context,
	buf *buffer.Ring,
	clusterID, clusterName, nodeName, leaseNamespace string,
) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Info("hubble: in-cluster config not available, skipping flow collection",
			slog.String("reason", err.Error()))
		return
	}

	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Warn("hubble: kube client init failed", slog.String("error", err.Error()))
		return
	}

	// Identity for the Lease holder. Pod name so logs show which pod has
	// the lease. Falls back to hostname if the downward-API env var
	// isn't set.
	identity := os.Getenv("POD_NAME")
	if identity == "" {
		identity = nodeName
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      "kubebolt-flow-collector",
			Namespace: leaseNamespace,
		},
		Client: kube.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}

	relayAddr := os.Getenv("KUBEBOLT_HUBBLE_RELAY_ADDR")
	if relayAddr == "" {
		relayAddr = "hubble-relay.kube-system.svc.cluster.local:80"
	}

	// Nested context so OnStoppedLeading can cancel the collector
	// without cancelling the outer agent context.
	var collectorCancel context.CancelFunc

	// Emit an initial non-leader sample so this pod's series exists in
	// VM even before the lease resolves — dashboards don't go blank
	// during the election phase.
	emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, false)

	// Periodic re-emit so VM's 5-minute staleness window never lets
	// the gauge fall off. Cheap: one sample per tick per agent pod.
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
				emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, leadingNow)
			}
		}
	}()

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		// Lease timing: 15s lease, 10s renew deadline, 2s retry.
		// Standard K8s defaults — rotates within a minute if the leader
		// crashes, which is fine for flow collection (15s of buffered
		// events is not catastrophic).
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				slog.Info("hubble: acquired flow-collector lease",
					slog.String("relay", relayAddr),
					slog.String("identity", identity))
				leadingNow = true
				emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, true)
				collCtx, cancel := context.WithCancel(leaderCtx)
				collectorCancel = cancel
				RunCollector(collCtx, relayAddr, buf, clusterID, clusterName, nodeName)
			},
			OnStoppedLeading: func() {
				slog.Info("hubble: lost flow-collector lease", slog.String("identity", identity))
				leadingNow = false
				emitLeaderStatus(buf, clusterID, clusterName, nodeName, identity, false)
				if collectorCancel != nil {
					collectorCancel()
					collectorCancel = nil
				}
			},
			OnNewLeader: func(leader string) {
				if leader != identity {
					slog.Info("hubble: flow-collector leader", slog.String("pod", leader))
				}
			},
		},
	})
}

// ResolveLeaseNamespace picks a namespace for the Lease from the
// downward-API env (POD_NAMESPACE) or the mounted ServiceAccount
// namespace file. Exported helper so main.go can resolve the namespace
// once at startup and pass it into RunLeaderElectedCollector.
func ResolveLeaseNamespace() (string, error) {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	// Standard in-cluster path.
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("resolve lease namespace: %w", err)
	}
	return string(data), nil
}
