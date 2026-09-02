package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// Fleet-scoped search (E2 Fleet C1): one query, every searchable
// cluster in the caller's org. Reuses the per-cluster machinery
// end-to-end — ListClusters is already org-scoped (the EE build
// filters by org), and each cluster's connector resolves through the
// same RuntimeKey path the X-KubeBolt-Cluster header uses, so tenant
// isolation is inherited rather than re-implemented.

// fleetSearchPerClusterTimeout bounds one slow/hung cluster; the
// others' results still return. 3s is generous for informer-backed
// listers (they answer from cache).
const fleetSearchPerClusterTimeout = 3 * time.Second

// fleetSearchLimit caps the MERGED result set. Per-cluster sweeps use
// a smaller slice so one giant cluster can't crowd out the rest.
const (
	fleetSearchLimit           = 50
	fleetSearchPerClusterLimit = 25
)

// searchableCluster reports whether a fleet search should touch this
// cluster: it needs a live resource connector, so metrics-only mode
// is out, and anything not currently connected (nor agent-live) would
// force a cold connect just to answer a search — skipped, the UI
// shows what was covered.
func searchableCluster(ci cluster.ClusterInfo) bool {
	if ci.Mode == "metrics-only" {
		return false
	}
	return ci.Status == "connected" || ci.AgentConnected
}

// clusterLabel is what the UI shows on a hit: the operator-facing
// display name when set, the context name otherwise.
func clusterLabel(ci cluster.ClusterInfo) string {
	if ci.DisplayName != "" {
		return ci.DisplayName
	}
	return ci.Context
}

// mergeFleetResults flattens per-cluster hits into one stable,
// capped list: sorted by cluster label then name so identical
// deployments in two clusters render adjacent (the fleet-search
// promise), truncated at the global cap.
func mergeFleetResults(perCluster map[string][]searchResult, cap_ int) []searchResult {
	labels := make([]string, 0, len(perCluster))
	for label := range perCluster {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	merged := []searchResult{}
	for _, label := range labels {
		hits := perCluster[label]
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].Name != hits[j].Name {
				return hits[i].Name < hits[j].Name
			}
			return hits[i].ResourceType < hits[j].ResourceType
		})
		for _, hit := range hits {
			if len(merged) >= cap_ {
				return merged
			}
			hit.Cluster = label
			merged = append(merged, hit)
		}
	}
	return merged
}

func (h *handlers) handleFleetSearch(w http.ResponseWriter, r *http.Request, query string) {
	// Same two filters GET /clusters applies, in the same order, for the same
	// reason: org is the hard boundary, team narrows within it. This fan-out used
	// to take ListClusters raw and gate only on searchableCluster — which asks
	// "is it reachable", not "may this caller see it". A viewer in one team got
	// resource names, namespaces and Secret names back from every OTHER team's
	// clusters, the same ones GET /clusters hides from them.
	//
	// Proven in vivo 2026-08-02 with a Team-A-only viewer: GET /clusters returned
	// one cluster, GET /search?scope=fleet returned clustersSearched=2 and hits
	// from both, including ConfigMap / Secret / ServiceAccount names out of the
	// Team-B cluster.
	//
	// Reusing the sanctioned helpers rather than writing a parallel check is the
	// point — a second, slightly-different notion of "clusters I may see" is how
	// this gap opened in the first place.
	infos := h.filterClustersByOrg(r, h.manager.ListClusters())
	infos = h.scopeClustersByTeam(r, infos)
	tenant := cluster.RuntimeKeyFromContext(r.Context()).Tenant

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		hit = make(map[string][]searchResult)
	)
	searched := 0
	for _, ci := range infos {
		if !searchableCluster(ci) {
			continue
		}
		searched++
		wg.Add(1)
		go func(ci cluster.ClusterInfo) {
			defer wg.Done()
			// Per-cluster context: same tenant, that cluster — the exact
			// shape the X-KubeBolt-Cluster middleware produces, so the
			// connector resolution (active or pooled) is the normal path.
			cctx, cancel := context.WithTimeout(
				cluster.WithRuntimeKey(r.Context(), cluster.RuntimeKey{Tenant: tenant, Cluster: ci.Context}),
				fleetSearchPerClusterTimeout,
			)
			defer cancel()

			conn := h.manager.Connector(cctx)
			if conn == nil {
				return // raced a disconnect — covered clusters shrink by one
			}
			results := searchConnector(conn, query, fleetSearchPerClusterLimit)
			if len(results) == 0 {
				return
			}
			mu.Lock()
			hit[clusterLabel(ci)] = results
			mu.Unlock()
		}(ci)
	}
	wg.Wait()

	respondJSON(w, http.StatusOK, map[string]any{
		"results":          mergeFleetResults(hit, fleetSearchLimit),
		"clustersSearched": searched,
	})
}
