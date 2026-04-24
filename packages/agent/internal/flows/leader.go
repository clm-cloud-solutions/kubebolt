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

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
)

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
	clusterID, nodeName, leaseNamespace string,
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
				collCtx, cancel := context.WithCancel(leaderCtx)
				collectorCancel = cancel
				RunCollector(collCtx, relayAddr, buf, clusterID, nodeName)
			},
			OnStoppedLeading: func() {
				slog.Info("hubble: lost flow-collector lease", slog.String("identity", identity))
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
