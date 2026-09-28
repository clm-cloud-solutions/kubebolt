package api

import (
	"context"
	"net/http"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// The clusters a caller may see, decided once for every surface that names
// clusters on the caller's behalf: Kobi's list_clusters and
// offer_cluster_switch, and the switch and headerless-request checks.

// fleetScopeTimeout bounds the reads that decide which clusters the caller
// may see (the cluster list, ownership, team memberships): they take
// milliseconds, a slow store must not stall the tool, and a scope it could
// not finish is not used.
const fleetScopeTimeout = 2 * time.Second

// visibleClusters is the set of clusters a caller may see: the two filters
// GET /clusters applies, in the same order (org is the hard boundary, team
// narrows within it), with the org wall's synthetic-context rule
// (orgClusters). Kobi's list_clusters / offer_cluster_switch read through
// it, so neither can grow a second notion of "clusters I may see" (see
// handleFleetSearch for what that cost once).
func (h *handlers) visibleClusters(r *http.Request, all []cluster.ClusterInfo) []cluster.ClusterInfo {
	return h.filterClustersByToken(r, h.scopeClustersByTeam(r, h.orgClusters(r, all)))
}

// orgClusters is the org wall (filterClustersByOrg) without the synthetic
// agent contexts the caller's org does not hold.
//
// A metrics-only cluster is registered under the same agent:<uid> context as
// an agent-proxy one, but not in the manager's agent-proxy identity map, so
// for every org except its owner ListClusters labels it source "file" with no
// cluster id: the shape of the operator's own clusters, which
// filterClustersByOrg keeps for the operator org. With no cluster id no team
// rule narrows it either, so every member of the operator org, and a service
// token acting for it, was shown another org's agent:<uid>. An agent:<uid>
// context is an agent's, whatever its source says: it stays only for an org
// with a live agent there or a membership row for it, the rule
// filterClustersByOrg applies to agent-proxy rows.
//
// GET /clusters does not read through this yet (it calls filterClustersByOrg
// directly), so it still shows such a row to the operator org.
func (h *handlers) orgClusters(r *http.Request, all []cluster.ClusterInfo) []cluster.ClusterInfo {
	// Single-tenant: the org wall is filterClustersByOrg, which passes every
	// cluster through. The Enterprise build also drops agent contexts the
	// caller's org does not hold.
	return h.filterClustersByOrg(r, all)
}

// fleetScope is the clusters a lookup made for this caller may search and
// name, decided before any lookup. A person (a browser session, a personal
// token) gets what GET /clusters shows them. A service principal (kbs_:
// Autopilot, on either MCP door) gets its org's clusters: Autopilot is enabled
// by the org's admin and acts for the whole org, not for a team — the same
// exemption canAccessContextByTeam and allowedClusterIDs give it. The org is
// still the wall (orgClusters): never another org's cluster, nor an
// operator-owned one outside the operator org.
func (h *handlers) fleetScope(r *http.Request, all []cluster.ClusterInfo, current, currentUID string) []cluster.ClusterInfo {
	if isAPITokenCaller(r) {
		// Service tokens and API keys read their org (see isAPITokenCaller);
		// an API key's own cluster list narrows it.
		return h.filterClustersByToken(r, h.orgClusters(r, all))
	}
	return h.visibleClusters(r, all)
}

// callerScope is the clusters the caller of a tool call may see (fleetScope),
// decided from the call's context, and the cluster that context resolves to;
// false when the scope could not be decided, and then nothing may be named or
// searched. currentUID names the cluster being investigated when its context
// is not the one ctx resolves to.
func (h *handlers) callerScope(ctx context.Context, currentUID string) (string, []cluster.ClusterInfo, bool) {
	if h.manager == nil {
		return "", nil, false
	}
	// The scoping helpers read the caller (org, role, user, API principal)
	// from a request; this one carries the tool call's context, which is the
	// chat's or the MCP door's request context.
	sctx, cancel := context.WithTimeout(ctx, fleetScopeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodGet, "/", nil)
	if err != nil {
		return "", nil, false
	}
	// Without an identity both filters open up: ContextTenantID falls back to
	// the default org and ContextRole to admin. A call that did not come
	// through an authenticated request (a background job, Execute on
	// context.Background()) has no reader to scope to, so it sees nothing.
	//
	// "default" is no identity either. In multi-tenant a real org id is never
	// that string: ResolveTenant stamps it when the caller carries no tenant
	// claim (a JWT minted without an org, a service token with no
	// X-KubeBolt-Org), and the org and team filters read it as "no org" and
	// return every org's clusters.
	if auth.MultiTenantEnabled {
		if t := auth.TenantIDFromContext(ctx); t == "" || t == auth.DefaultTenantName || auth.ContextClaims(req) == nil {
			return "", nil, false
		}
	}
	current := h.manager.ActiveContextFor(ctx)
	scope := h.fleetScope(req, h.manager.ListClusters(sctx), current, currentUID)
	if sctx.Err() != nil {
		// A store that did not answer in time may have left in a cluster its
		// ownership would have taken out: no scope.
		return "", nil, false
	}
	return current, scope, true
}

// CallerClusters answers Kobi's list_clusters and offer_cluster_switch
// (copilot.ClusterListSource): the caller's clusters by the same rule
// GET /clusters applies, so a Kobi never lists, nor offers a switch to, a
// cluster its caller cannot open.
func (h *handlers) CallerClusters(ctx context.Context) ([]cluster.ClusterInfo, bool) {
	_, scope, ok := h.callerScope(ctx, "")
	if ok && scope == nil {
		// Decided, and empty: list_clusters answers [], not null.
		scope = []cluster.ClusterInfo{}
	}
	return scope, ok
}

var _ copilot.ClusterListSource = (*handlers)(nil)
