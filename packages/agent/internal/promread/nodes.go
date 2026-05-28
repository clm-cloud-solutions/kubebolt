package promread

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// NodeIndex resolves a Prometheus `instance` IP (the kind node-exporter
// scrapes use the hostNetwork pod IP, which equals the Kubernetes
// node's InternalIP) back to the canonical Kubernetes node name. Used
// by Convert to stamp a `node=<nodeName>` label on `node_*` series so
// the UI's PromQL (which filters by `node="..."`) matches.
//
// Implementations are safe for concurrent use. Returning "" means
// "unknown IP" — Convert skips the stamp rather than emit a wrong
// label.
type NodeIndex interface {
	// NodeByIP resolves the canonical Kubernetes node name for an
	// InternalIP. Used for AMP and node-exporter-style sources where
	// `instance=<pod-IP>:<port>` and we need to map IP → name.
	NodeByIP(ip string) string
	// IsKnownNode reports whether the given string is a known
	// Kubernetes node name in the cluster. Used as a fallback for
	// Azure-style sources where `instance=<aks-nodepool-...-vmss000000>`
	// is ALREADY the node name (not an IP:port). Convert calls
	// IsKnownNode only when NodeByIP returns empty, and only when the
	// series doesn't already carry a `node` label (e.g. GMP, which
	// auto-relabels server-side).
	IsKnownNode(name string) bool
}

// DefaultNodeRefreshInterval is the cadence at which K8sNodeIndex
// re-lists nodes. Nodes change slowly compared to pods (minutes vs
// seconds), so 5 minutes is plenty without putting pressure on the
// apiserver.
const DefaultNodeRefreshInterval = 5 * time.Minute

// K8sNodeIndex builds the IP→name map from the Kubernetes API. List
// on a ticker instead of an informer because the cardinality is low
// (one entry per node) and the refresh window doesn't need to be
// sub-second.
type K8sNodeIndex struct {
	client          kubernetes.Interface
	refreshInterval time.Duration

	mu       sync.RWMutex
	ipToName map[string]string
	nameSet  map[string]struct{}
}

// NewK8sNodeIndex constructs a K8sNodeIndex. Pass a nil-or-zero
// refreshInterval to use DefaultNodeRefreshInterval.
func NewK8sNodeIndex(client kubernetes.Interface, refreshInterval time.Duration) *K8sNodeIndex {
	if refreshInterval <= 0 {
		refreshInterval = DefaultNodeRefreshInterval
	}
	return &K8sNodeIndex{
		client:          client,
		refreshInterval: refreshInterval,
		ipToName:        make(map[string]string),
		nameSet:         make(map[string]struct{}),
	}
}

// NodeByIP returns the Kubernetes node name whose InternalIP matches
// the given IP, or "" if unknown. The IP must NOT include a port —
// callers strip via StripPort first.
func (n *K8sNodeIndex) NodeByIP(ip string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.ipToName[ip]
}

// IsKnownNode reports whether the given string is an exact match for
// a Kubernetes node name in the cluster (the `.metadata.name` of any
// Node object). Used by Convert as a fallback when the series'
// `instance` label is the node name itself (Azure managed Prom
// shape) rather than an IP:port (AMP / node-exporter shape).
func (n *K8sNodeIndex) IsKnownNode(name string) bool {
	if name == "" {
		return false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	_, ok := n.nameSet[name]
	return ok
}

// Refresh re-lists Nodes and rebuilds the InternalIP map. Errors are
// returned but the existing map stays intact — a transient apiserver
// blip doesn't blank out the index.
func (n *K8sNodeIndex) Refresh(ctx context.Context) error {
	nodes, err := n.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	nextIP := make(map[string]string, len(nodes.Items))
	nextNames := make(map[string]struct{}, len(nodes.Items))
	for i := range nodes.Items {
		node := &nodes.Items[i]
		nextNames[node.Name] = struct{}{}
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP && addr.Address != "" {
				nextIP[addr.Address] = node.Name
			}
		}
	}
	n.mu.Lock()
	n.ipToName = nextIP
	n.nameSet = nextNames
	n.mu.Unlock()
	return nil
}

// Run blocks until ctx is done, firing an immediate Refresh on entry
// and then re-Refreshing on the configured interval. A failure on the
// initial Refresh logs a warning but does NOT abort the loop — the
// next tick gets another shot. Designed to be launched in its own
// goroutine from the agent's main run loop.
func (n *K8sNodeIndex) Run(ctx context.Context) {
	if err := n.Refresh(ctx); err != nil {
		slog.Warn("promread node index initial refresh failed",
			slog.String("error", err.Error()))
	} else {
		slog.Info("promread node index ready",
			slog.Int("nodes", n.Size()))
	}
	tick := time.NewTicker(n.refreshInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := n.Refresh(ctx); err != nil {
				slog.Warn("promread node index refresh failed",
					slog.String("error", err.Error()))
			}
		}
	}
}

// Size returns the number of nodes currently indexed. Exposed for
// boot-time logging and tests.
func (n *K8sNodeIndex) Size() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.ipToName)
}

// StripPort returns the host portion of an `instance` label value.
// node-exporter typically reports instances as "<ip>:<port>"
// (e.g. "172.18.0.4:9100"); this strips the trailing ":port" so
// the result can be looked up in the IP→name map.
func StripPort(instance string) string {
	if i := strings.LastIndex(instance, ":"); i > 0 {
		return instance[:i]
	}
	return instance
}
