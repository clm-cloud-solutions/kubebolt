package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// ClusterHeader carries the cluster the frontend selected for this request.
// OSS may omit it (→ the active context); the EE/SaaS UI sends it per
// request so one backend can serve many (tenant, cluster) pairs
// concurrently (W2).
const ClusterHeader = "X-KubeBolt-Cluster"

// resolveCluster stashes the request's (tenant, cluster) RuntimeKey in
// context, mounted after RequireAuth + ResolveTenant (W0). It is
// behavior-neutral today: the Manager still serves the single active
// cluster until the connector pool (W2 Fase A.3) reads the key. Threading
// the key now lets that pool land WITHOUT touching the 56 handler call
// sites again. See internal/kubebolt-w2-connector-pool-design.md.
func (h *handlers) resolveCluster(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := cluster.RuntimeKey{
			Tenant:  auth.ContextTenantID(r), // W0 — "default" in OSS
			Cluster: r.Header.Get(ClusterHeader),
		}
		if org, narrow := orgScoped(r); narrow {
			if org == "" {
				// Multi-tenant, no org: no cluster at all. The manager reads the
				// DefaultTenantName sentinel as the operator's own org
				// (canonTenant), so this request would otherwise be served the
				// operator's selected cluster.
				key = cluster.RuntimeKey{Tenant: key.Tenant, NoCluster: true}
			} else if key.Cluster == "" {
				key = h.headerlessCluster(r, key)
			}
		} else if _, tokenNarrowed := tokenClusterSet(r); tokenNarrowed && key.Cluster == "" {
			// Single-tenant has no org or team to narrow by, but an API key's
			// cluster list still does: without this a narrowed key calling with
			// no header read whichever cluster the install had selected.
			key = h.headerlessCluster(r, key)
		}
		next.ServeHTTP(w, r.WithContext(cluster.WithRuntimeKey(r.Context(), key)))
	})
}

// headerlessCluster decides the cluster of a request that names none. The
// manager falls back to the ORG's selection (or its first reachable cluster),
// and that selection is one per org, not per user: after an admin switched to
// team Y's cluster, a team-X member calling without the header — an external
// MCP host, a script — read team Y's cluster, because requireClusterAccess
// only checks a cluster the request names. When the caller may not read the
// fallback, the request goes to the first cluster the caller may read (a
// connected one first, by context), or to no cluster. An agent-proxy context
// the manager cannot resolve is left alone: that is a routing failure, which
// downstream answers 503 "waiting for agent", not a cluster to reroute from.
func (h *handlers) headerlessCluster(r *http.Request, key cluster.RuntimeKey) cluster.RuntimeKey {
	effective := h.manager.ActiveContextFor(cluster.WithRuntimeKey(r.Context(), key))
	if effective == "" {
		return key
	}
	_, tokenNarrowed := tokenClusterSet(r)
	if !tokenNarrowed && strings.HasPrefix(effective, cluster.AgentProxyContextPrefix) && h.manager.ClusterIDForContext(effective) == "" {
		return key
	}
	if h.canAccessContextByTeam(r, effective) {
		// Pin the cluster that was CHECKED. Left empty, every later read of
		// this request (each tool call of a long chat stream) re-resolved the
		// org's selection, and an admin switching it mid-request moved the
		// caller onto a cluster nobody had checked.
		key.Cluster = effective
		return key
	}
	visible := h.visibleClusters(r, h.manager.ListClusters(r.Context()))
	sort.Slice(visible, func(i, j int) bool { return visible[i].Context < visible[j].Context })
	pick := ""
	for _, c := range visible {
		if c.Status == "connected" {
			pick = c.Context
			break
		}
		if pick == "" {
			pick = c.Context
		}
	}
	if pick == "" {
		key.NoCluster = true
		return key
	}
	key.Cluster = pick
	return key
}
