package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// An API key's cluster allow-list (auth.APIToken.Clusters).
//
// Path scopes say which ROUTES a token may call; this says which CLUSTERS it
// may read. Empty means every cluster of the token's org — only an org admin
// can issue an API key, and the key acts for the org, not for a person or a
// team. A list narrows it to those clusters, on every read path:
//
//   - canAccessContextByTeam — the per-request cluster (the header, the
//     header-less fallback, cluster switch, the WebSocket) and with it every
//     connector read: REST, per-cluster metrics, Kobi / MCP tools.
//   - allowedClusterIDs — the org-level reads behind WithClusterScope
//     (findings, runtime events, insight summary and episodes, cluster names,
//     the fleet PromQL path) and the tools that read through them.
//   - the cluster lists — GET /clusters, fleet search, list_clusters and
//     offer_cluster_switch, and the pick list of the header-less fallback.
//
// All three are checked BEFORE the org / team / admin shortcuts: the list is a
// property of the credential, so it holds in single-tenant installs and for a
// token whose role is admin.

// tokenClusterSet is the calling API key's allow-list; false when the caller
// is not a narrowed token (a session, a service token, a key with no list).
func tokenClusterSet(r *http.Request) (map[string]struct{}, bool) {
	p := auth.ContextAPIPrincipal(r)
	if p == nil || p.Type != auth.TokenTypeAPIKey || len(p.Clusters) == 0 {
		return nil, false
	}
	set := make(map[string]struct{}, len(p.Clusters))
	for _, id := range p.Clusters {
		set[id] = struct{}{}
	}
	return set, true
}

// isAPITokenCaller reports whether the request is made with an API token of
// either kind. Both read their org without the team refinement: a service
// token acts for the org by design, and an API key is issued by an org admin
// and narrowed, when that is wanted, by its own cluster list instead. Their
// synthetic identity (svc:<id>) belongs to no team, so the membership check
// would narrow them to nothing.
func isAPITokenCaller(r *http.Request) bool {
	return auth.ContextAPIPrincipal(r) != nil
}

// clusterIDOf is a cluster row's id, resolving it from the context name when
// the row does not carry one (a direct kubeconfig context).
func (h *handlers) clusterIDOf(ctx context.Context, c cluster.ClusterInfo) string {
	if c.ClusterID != "" {
		return c.ClusterID
	}
	if h.manager == nil {
		return ""
	}
	if id := h.manager.CanonicalClusterID(ctx, c.Context); id != c.Context {
		return id
	}
	return ""
}

// tokenAllowsContext reports whether the caller's allow-list admits the
// cluster behind contextName. An empty name ("no cluster named") is admitted:
// the header-less path checks the cluster it falls back to by name. A context
// whose id cannot be resolved is refused — the list cannot vouch for it.
func (h *handlers) tokenAllowsContext(r *http.Request, contextName string) bool {
	set, narrowed := tokenClusterSet(r)
	if !narrowed || contextName == "" {
		return true
	}
	if h.manager == nil {
		return false
	}
	id := h.manager.ClusterIDForContext(contextName)
	if id == "" {
		id = h.manager.CanonicalClusterID(r.Context(), contextName)
		if id == contextName {
			id = ""
		}
	}
	if id == "" {
		return false
	}
	_, ok := set[id]
	return ok
}

// filterClustersByToken keeps the rows the caller's allow-list admits.
func (h *handlers) filterClustersByToken(r *http.Request, clusters []cluster.ClusterInfo) []cluster.ClusterInfo {
	set, narrowed := tokenClusterSet(r)
	if !narrowed {
		return clusters
	}
	out := make([]cluster.ClusterInfo, 0, len(clusters))
	for _, c := range clusters {
		if _, ok := set[h.clusterIDOf(r.Context(), c)]; ok {
			out = append(out, c)
		}
	}
	return out
}

// narrowIDsByToken applies the allow-list to an allowedClusterIDs answer:
// "no narrowing" becomes the list itself, a narrowed set is intersected.
func narrowIDsByToken(r *http.Request, ids []string, narrowed bool) ([]string, bool) {
	set, restricted := tokenClusterSet(r)
	if !restricted {
		return ids, narrowed
	}
	if !narrowed {
		out := make([]string, 0, len(set))
		for id := range set {
			out = append(out, id)
		}
		return out, true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := set[id]; ok {
			out = append(out, id)
		}
	}
	return out, true
}

// checkTokenClusters validates an allow-list at issue / edit time: every id
// must be a cluster of the caller's org (the org wall, orgClusters). Wired
// into auth.Handlers with SetAPITokenClusterCheck.
func (h *handlers) checkTokenClusters(r *http.Request, ids []string) error {
	if h.manager == nil {
		return fmt.Errorf("cluster lists are not available on this install")
	}
	known := map[string]struct{}{}
	for _, c := range h.orgClusters(r, h.manager.ListClusters(r.Context())) {
		if id := h.clusterIDOf(r.Context(), c); id != "" {
			known[id] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("unknown cluster %q: pick clusters of this organization", id)
		}
	}
	return nil
}

// readableCluster is the per-row check for a handler outside WithClusterScope:
// allowedClusterIDs as a predicate (true for every id when nothing narrows).
func (h *handlers) readableCluster(r *http.Request) func(id string) bool {
	ids, narrowed := h.allowedClusterIDs(r)
	if !narrowed {
		return func(string) bool { return true }
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return func(id string) bool { _, ok := set[id]; return ok }
}

// filterEpisodes keeps the episodes of clusters the caller may read.
func filterEpisodes(eps []insights.Episode, may func(string) bool) []insights.Episode {
	out := make([]insights.Episode, 0, len(eps))
	for _, e := range eps {
		if may(e.ClusterID) {
			out = append(out, e)
		}
	}
	return out
}

// filterBursts keeps the operational episodes that touched a cluster the
// caller may read, and names only those clusters. A burst is cross-cluster by
// design; a caller who may read one of its clusters sees that it happened
// there, not where else it reached.
func filterBursts(ops []insights.OperationalEpisode, may func(string) bool) []insights.OperationalEpisode {
	out := make([]insights.OperationalEpisode, 0, len(ops))
	for _, o := range ops {
		var mine []string
		for _, c := range o.Clusters {
			if may(c) {
				mine = append(mine, c)
			}
		}
		if len(mine) == 0 {
			continue
		}
		if len(mine) < len(o.Clusters) {
			// Partly outside the caller's clusters: the blast figures and the
			// member ids were computed across all of them, and the worst
			// resource may well be in a cluster this caller cannot see. Keep
			// that it happened and when; drop what describes the other side.
			o.Blast = insights.BlastStats{}
			o.SeedIDs, o.MemberIDs = nil, nil
		}
		o.Clusters = mine
		out = append(out, o)
	}
	return out
}

// windowForClusters reads an episode window for a caller narrowed to some
// clusters. The store pages the whole org newest first, so filtering one org
// page afterwards could leave a narrowed caller with nothing at all while its
// own clusters had history (the page was full of other clusters' rows). One
// query per readable cluster, merged newest first (the store's order), then
// the page the caller asked for.
func windowForClusters(ctx context.Context, reader insights.EpisodeReader, org string, q insights.EpisodeQuery, ids []string) ([]insights.Episode, error) {
	limit, offset := q.Limit, q.Offset
	if limit <= 0 || limit > 200 {
		limit = 50 // the store's default
	}
	var all []insights.Episode
	for _, id := range ids {
		per := q
		per.ClusterID = id
		per.Offset = 0
		per.Limit = offset + limit
		if per.Limit > 200 {
			per.Limit = 200
		}
		eps, err := reader.Window(ctx, org, per)
		if err != nil {
			return nil, err
		}
		all = append(all, eps...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].LastSeen.After(all[j].LastSeen) })
	if int(offset) >= len(all) {
		return []insights.Episode{}, nil
	}
	all = all[offset:]
	if len(all) > int(limit) {
		all = all[:limit]
	}
	return all, nil
}
