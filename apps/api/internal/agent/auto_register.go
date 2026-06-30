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
	// AddMetricsOnlyCluster registers cluster_id as a metrics-only cluster (the agent
	// ships metrics but advertises no kube-proxy, so there is no connector). Surfaces
	// it in ListClusters flagged Mode="metrics-only"; never starts a connector.
	AddMetricsOnlyCluster(clusterID, displayName string) (string, error)
	// RemoveAgentProxyCluster removes the agent-proxy registration.
	// Idempotent — a no-op if cluster_id wasn't registered.
	RemoveAgentProxyCluster(clusterID string)
}

// IsSelfCluster reports whether clusterID is the same cluster the
// backend itself runs in.
//
// selfClusterID is the backend's own kube-system namespace UID, as
// discovered by DiscoverClusterID at boot. Empty when the backend
// isn't running in a Kubernetes cluster (e.g. dev runs with
// kubeconfig-on-disk, or in-cluster discovery failed): the function
// returns false unconditionally — the self-skip is gated off and
// callers proceed with their normal registration logic.
//
// When non-empty AND clusterID matches, the function returns true.
// Callers MUST skip registering an agent-proxy cluster in that
// scenario: the cluster is already exposed via the in-cluster
// kubeconfig context, so a second registration would surface the
// cluster TWICE in the UI selector. This is the topology of an
// operator installing both the backend and an agent in the same
// single cluster — the obvious happy-path of OSS self-hosted.
//
// Lives in a shared helper so the rule is pinned ONCE for every
// site that calls into the cluster manager's AddAgentProxyCluster:
//   - live-connect path: maybeAutoRegisterCluster (below)
//   - boot-time restore path: cmd/server/main.go boot loop that
//     replays persisted AgentRecord entries.
//
// Both sites must use this helper. cluster-validation BUG-2 (the
// live path) and BUG-3 (the boot path) are the same conceptual
// bug applied to different call sites — the helper exists so a
// future third caller can't accidentally regress the contract.
func IsSelfCluster(clusterID, selfClusterID string) bool {
	return selfClusterID != "" && clusterID == selfClusterID
}

// maybeAutoRegisterCluster registers clusterID with the manager when
// auto-register is enabled AND the agent advertises the kube-proxy
// capability AND a registrar is wired AND the agent's cluster_id is
// NOT the backend's own cluster (which is already exposed via the
// in-cluster kubeconfig context). The decision lives in one place so
// the Channel handler stays small and the rule is easy to pin in
// tests.
//
// Returns true when the cluster was registered (so the caller knows
// to schedule the cleanup defer).
func maybeAutoRegisterCluster(reg ClusterRegistrar, registry *channel.AgentRegistry, autoRegister bool, clusterID, displayName string, capabilities []string, selfClusterID string) bool {
	if reg == nil || !autoRegister {
		return false
	}
	if IsSelfCluster(clusterID, selfClusterID) {
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
		// Metrics-only agent (no kube-proxy): surface the cluster as monitored-only
		// instead of leaving it invisible. Its metrics reach VM independently of the
		// proxy, so it gets a switchable, connector-less entry in the selector — the UI
		// shows the metrics dashboards and degrades the resource views.
		if _, err := reg.AddMetricsOnlyCluster(clusterID, displayName); err != nil {
			slog.Warn("auto-register metrics-only cluster failed",
				slog.String("cluster_id", clusterID),
				slog.String("error", err.Error()),
			)
			return false
		}
		slog.Info("auto-registered metrics-only cluster",
			slog.String("cluster_id", clusterID),
			slog.String("display_name", displayName),
		)
		return true
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
