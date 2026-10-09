package api

import "context"

// sessionClusterAndTeam resolves the two identities a Kobi session record
// carries for observability (doc #67): the cluster's kube-system UID — the
// cluster_id VictoriaMetrics series use, where SessionRecord.Cluster keeps the
// context name — and the team that owns that cluster.
//
// noConnector: a metrics-only cluster has no connector and is known by its
// metrics id, not by a context. OSS has no cluster ownership, so the team is
// always "" — the field stays for parity with the Enterprise record.
func (h *handlers) sessionClusterAndTeam(ctx context.Context, contextName string, noConnector bool) (clusterID, teamID string) {
	clusterID = h.manager.CanonicalClusterID(ctx, contextName)
	if noConnector {
		if id := h.manager.MetricsOnlyClusterID(ctx); id != "" {
			clusterID = id
		}
	}
	return clusterID, ""
}
