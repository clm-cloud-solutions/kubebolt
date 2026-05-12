package agent

import (
	"log/slog"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

// ClusterRegistrar is the subset of cluster.Manager that the Channel
// handler needs to expose agent-proxy clusters. Defined here, not in
// the cluster package, to keep the import direction one-way: agent
// → channel → (nothing further). Defining the interface in the
// consumer's package is the standard Go workaround for what would
// otherwise be a cycle (apps/api/internal/cluster already imports
// apps/api/internal/agent/channel for the AgentRegistry / Transport).
//
// The cluster.Manager satisfies this implicitly via its
// AddAgentProxyCluster / RemoveAgentProxyCluster methods (commit 6).
type ClusterRegistrar interface {
	// AddAgentProxyCluster registers cluster_id as reachable via the
	// agent-proxy transport. Returns the contextName under which the
	// cluster is exposed in the manager's listing.
	AddAgentProxyCluster(clusterID, displayName string) (string, error)
	// RemoveAgentProxyCluster removes the agent-proxy registration.
	// Idempotent — a no-op if cluster_id wasn't registered.
	RemoveAgentProxyCluster(clusterID string)
}

// maybeAutoRegisterCluster registers clusterID with the manager when
// auto-register is enabled AND the agent advertises the kube-proxy
// capability AND a registrar is wired AND the agent's cluster_id is
// NOT the backend's own cluster (which is already exposed via the
// in-cluster kubeconfig context). The decision lives in one place so
// the Channel handler stays small and the rule is easy to pin in
// tests.
//
// selfClusterID is the kube-system namespace UID of the cluster the
// backend itself runs in, as discovered by DiscoverClusterID at boot.
// Empty when the backend isn't running in a Kubernetes cluster (e.g.
// dev runs with kubeconfig-on-disk, or in-cluster discovery failed):
// the self-skip is gated off and the function preserves its prior
// behavior. When non-empty, an agent that reports a matching
// cluster_id is treated as the same cluster the backend already
// represents via its in-cluster context, so registering it again as
// an agent-proxy would surface the cluster TWICE in the UI selector.
// This matches the topology of an operator installing both the
// backend and an agent in the same single cluster — the obvious
// happy-path of OSS self-hosted.
//
// Returns true when the cluster was registered (so the caller knows
// to schedule the cleanup defer).
func maybeAutoRegisterCluster(reg ClusterRegistrar, registry *channel.AgentRegistry, autoRegister bool, clusterID, displayName string, capabilities []string, selfClusterID string) bool {
	if reg == nil || !autoRegister {
		return false
	}
	if selfClusterID != "" && clusterID == selfClusterID {
		// Agent reports the backend's own cluster — already exposed
		// via the in-cluster context. Skip to avoid the duplicate
		// row in the UI cluster selector that operators surfaced in
		// cluster-validation BUG-2 (post-rc.2 retest, when Bug-1's
		// cluster_hint fix made the duplication visually obvious).
		slog.Debug("auto-register skipped: agent reports backend's own cluster_id (already in-cluster)",
			slog.String("cluster_id", clusterID),
		)
		return false
	}
	if registry == nil {
		// The registrar requires a wired registry on the manager side
		// (SetAgentRegistry), but defensively skip rather than panic.
		slog.Warn("auto-register skipped: agent registry not wired into manager",
			slog.String("cluster_id", clusterID),
		)
		return false
	}
	if !hasCapability(capabilities, "kube-proxy") {
		return false
	}
	contextName, err := reg.AddAgentProxyCluster(clusterID, displayName)
	if err != nil {
		slog.Warn("auto-register agent-proxy cluster failed",
			slog.String("cluster_id", clusterID),
			slog.String("error", err.Error()),
		)
		return false
	}
	slog.Info("auto-registered agent-proxy cluster",
		slog.String("cluster_id", clusterID),
		slog.String("context", contextName),
		slog.String("display_name", displayName),
	)
	return true
}

// maybeAutoUnregisterCluster removes clusterID from the manager only
// when no other agent for that cluster_id is still connected.
// Multi-node clusters (DaemonSet) connect N agents per cluster_id;
// the cluster must stay reachable as long as at least one peer is
// still up.
//
// MUST be called AFTER the disconnecting agent has been Unregister'd
// from the AgentRegistry so CountByCluster reflects the post-removal
// state.
func maybeAutoUnregisterCluster(reg ClusterRegistrar, registry *channel.AgentRegistry, clusterID string) {
	if reg == nil {
		return
	}
	if registry != nil && registry.CountByCluster(clusterID) > 0 {
		slog.Debug("agent-proxy cluster keeping registration: peers still connected",
			slog.String("cluster_id", clusterID),
			slog.Int("remaining", registry.CountByCluster(clusterID)),
		)
		return
	}
	reg.RemoveAgentProxyCluster(clusterID)
	slog.Info("removed agent-proxy cluster after last agent disconnected",
		slog.String("cluster_id", clusterID),
	)
}

func hasCapability(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
