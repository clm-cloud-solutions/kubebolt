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
// capability AND a registrar is wired. The decision lives in one
// place so the Channel handler stays small and the rule is easy to
// pin in tests.
//
// Returns true when the cluster was registered (so the caller knows
// to schedule the cleanup defer).
func maybeAutoRegisterCluster(reg ClusterRegistrar, registry *channel.AgentRegistry, autoRegister bool, clusterID, displayName string, capabilities []string) bool {
	if reg == nil || !autoRegister {
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
