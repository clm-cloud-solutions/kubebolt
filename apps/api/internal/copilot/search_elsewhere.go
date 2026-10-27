package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// When search_resources finds nothing in the selected cluster, it searches the
// other clusters the caller may see. An empty {"results":[]} was all Kobi got
// when the operator named a workload of another cluster: asked in the backend
// cluster about shop-web (which runs in the frontend one), it concluded "not
// here, not verified" and offered no switch, twice; the one time it went on to
// list_clusters by itself, it found it and offered the switch (three-tier-shop
// lab, 2026-10-10). Where the name is now comes back as data, next to the
// switch the prompt already describes.

const (
	// otherClusterSearchWait bounds the whole fan-out. A cluster with no warm
	// runtime connects on first use — seconds through an agent tunnel — and
	// Manager.Connector does not take a deadline: such a cluster is reported
	// as not answered rather than waited for, and keeps warming for the next
	// search.
	otherClusterSearchWait = 6 * time.Second
	// Per cluster and in total, as the fleet search behind ⌘K
	// (api/search_fleet.go).
	otherClusterSearchPerCluster = 25
	otherClusterSearchLimit      = 50
)

// searchTargets is the caller's clusters a search can reach, the selected one
// left out: a live resource connector is needed, so metrics-only clusters are
// out, and so is anything neither connected nor agent-live. Mirrors
// api.searchableCluster.
func searchTargets(clusters []cluster.ClusterInfo, current string) []cluster.ClusterInfo {
	out := make([]cluster.ClusterInfo, 0, len(clusters))
	for _, ci := range clusters {
		if ci.Context == current || ci.Mode == "metrics-only" {
			continue
		}
		if ci.Status == "connected" || ci.AgentConnected {
			out = append(out, ci)
		}
	}
	return out
}

// clusterSearchLabel is how a hit names its cluster: the display name when
// set, the context otherwise — both are accepted by offer_cluster_switch.
func clusterSearchLabel(ci cluster.ClusterInfo) string {
	if ci.DisplayName != "" {
		return ci.DisplayName
	}
	return ci.Context
}

// clusterSearch searches one cluster; ok is false when it could not be read.
type clusterSearch func(ci cluster.ClusterInfo) (hits []map[string]interface{}, ok bool)

// searchOtherClusters runs search over every target at once and waits at most
// wait. It returns the hits, each labelled with its cluster, how many
// clusters answered, and which did not (unreadable, or not in time).
func searchOtherClusters(targets []cluster.ClusterInfo, wait time.Duration, search clusterSearch) ([]map[string]interface{}, int, []string) {
	type answer struct {
		label, context string
		hits           []map[string]interface{}
		ok             bool
	}
	answers := make(chan answer, len(targets))
	for _, ci := range targets {
		go func(ci cluster.ClusterInfo) {
			hits, ok := search(ci)
			answers <- answer{label: clusterSearchLabel(ci), context: ci.Context, hits: hits, ok: ok}
		}(ci)
	}

	pending := map[string]bool{}
	for _, ci := range targets {
		pending[clusterSearchLabel(ci)] = true
	}
	var hits []map[string]interface{}
	answered := 0
	deadline := time.After(wait)
collect:
	for range targets {
		select {
		case a := <-answers:
			if !a.ok {
				continue
			}
			delete(pending, a.label)
			answered++
			for _, h := range a.hits {
				h["cluster"] = a.label
				// Unique where display names repeat; what offer_cluster_switch
				// and the dependency lookup take.
				h["clusterContext"] = a.context
				hits = append(hits, h)
			}
		case <-deadline:
			break collect
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		for _, k := range []string{"cluster", "kind", "namespace", "name"} {
			if x, y := fmt.Sprint(a[k]), fmt.Sprint(b[k]); x != y {
				return x < y
			}
		}
		return false
	})
	if len(hits) > otherClusterSearchLimit {
		hits = hits[:otherClusterSearchLimit]
	}
	late := make([]string, 0, len(pending))
	for l := range pending {
		late = append(late, l)
	}
	sort.Strings(late)
	return hits, answered, late
}

// elsewherePayload is search_resources' answer when the selected cluster had
// no match: where the name is in the caller's other clusters, or that it is
// in none of those that answered.
func elsewherePayload(hits []map[string]interface{}, answered int, late []string) map[string]interface{} {
	payload := map[string]interface{}{"results": []map[string]interface{}{}}
	if answered == 0 && len(late) == 0 {
		return payload // nothing else to search: a single-cluster install
	}
	payload["otherClustersSearched"] = answered
	var note []string
	switch {
	case len(hits) > 0 && anyCallsHere(hits):
		payload["elsewhere"] = hits
		note = append(note, "Nothing by that name in this cluster; it is in another cluster you can see (elsewhere), and it calls workloads that run in this cluster (callsHere). Check those here first, at the time asked about: the cause may be here. The symptom asked about is read where it runs: then offer the switch to its cluster (load_skill fleet-and-switch).")
	case len(hits) > 0:
		payload["elsewhere"] = hits
		note = append(note, "Nothing by that name in this cluster; it is in another cluster you can see (elsewhere). The symptom asked about is read where it runs: offer the switch to that cluster with offer_cluster_switch (load_skill fleet-and-switch).")
	default:
		note = append(note, fmt.Sprintf("Nothing by that name in this cluster, nor in the %d other clusters that answered.", answered))
	}
	if len(late) > 0 {
		payload["otherClustersNotAnswered"] = late
		note = append(note, fmt.Sprintf("%s did not answer within %s (a cluster not read recently connects first): search again to cover it.",
			strings.Join(late, ", "), otherClusterSearchWait))
	}
	payload["note"] = strings.Join(note, " ")
	return payload
}

// searchElsewhere is search_resources' fallback when the selected cluster has
// no match: the same name search over the caller's other clusters — the same
// list list_clusters and offer_cluster_switch use, so a cluster of another
// org or team is never searched.
func (e *Executor) searchElsewhere(ctx context.Context, query string) map[string]interface{} {
	if e.manager == nil {
		return map[string]interface{}{"results": []map[string]interface{}{}}
	}
	clusters, ok := e.callerClusters(ctx)
	if !ok {
		return map[string]interface{}{"results": []map[string]interface{}{}}
	}
	current := e.manager.ActiveContextFor(ctx)
	targets := searchTargets(clusters, current)
	tenant := cluster.RuntimeKeyFromContext(ctx).Tenant
	hits, answered, late := searchOtherClusters(targets, otherClusterSearchWait, func(ci cluster.ClusterInfo) ([]map[string]interface{}, bool) {
		// The same per-cluster shape the X-KubeBolt-Cluster middleware
		// produces: same org, that cluster.
		conn := e.manager.Connector(cluster.WithRuntimeKey(context.WithoutCancel(ctx), cluster.RuntimeKey{Tenant: tenant, Cluster: ci.Context}))
		if conn == nil {
			return nil, false
		}
		return searchAllResources(conn, query, otherClusterSearchPerCluster), true
	})
	if e.external != nil && len(hits) > 0 {
		annotateCallsHere(hits, otherClusterDependencyWait, e.callsHereLookup(ctx, tenant, current))
	}
	return elsewherePayload(hits, answered, late)
}

// From the backend cluster, asked about shop-web, Kobi found it in the
// frontend cluster and offered the switch without reading a line here; the
// frontend traced its 503s to orders-api, back in the backend cluster, and
// offered the switch back (three-tier-shop lab, 2026-10-10). What a workload
// found elsewhere calls in THIS cluster is the lead that keeps the
// investigation here: the same lookup as get_external_connections, run where
// the workload is, keeping what this cluster exposes.

const (
	// otherClusterDependencyWait bounds the dependency lookups, run after
	// the search, on the clusters it just read.
	otherClusterDependencyWait = 5 * time.Second
	// maxDependencyLookups caps how many hits are looked up.
	maxDependencyLookups = 3
)

// dependencyKinds are the hits whose configuration and traffic name what
// they call.
var dependencyKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true}

// callsHereLookup returns what one hit calls in the current cluster, as
// "Kind namespace/name (via)".
type callsHereLookup func(hit map[string]interface{}) []string

// annotateCallsHere sets callsHere on up to maxDependencyLookups workload
// hits, waiting at most wait; a lookup that answers late is left out.
func annotateCallsHere(hits []map[string]interface{}, wait time.Duration, lookup callsHereLookup) {
	type answer struct {
		i     int
		calls []string
	}
	var idx []int
	for i, h := range hits {
		if len(idx) == maxDependencyLookups {
			break
		}
		if k, _ := h["kind"].(string); dependencyKinds[k] {
			idx = append(idx, i)
		}
	}
	answers := make(chan answer, len(idx))
	for _, i := range idx {
		go func(i int) { answers <- answer{i: i, calls: lookup(hits[i])} }(i)
	}
	deadline := time.After(wait)
	for range idx {
		select {
		case a := <-answers:
			if len(a.calls) > 0 {
				hits[a.i]["callsHere"] = a.calls
			}
		case <-deadline:
			return
		}
	}
}

func anyCallsHere(hits []map[string]interface{}) bool {
	for _, h := range hits {
		if h["callsHere"] != nil {
			return true
		}
	}
	return false
}

// callsHereLookup runs get_external_connections' source for a hit, in the
// hit's cluster and with the caller's identity (the fleet lookup inside is
// scoped by it), and keeps the addresses the current cluster exposes.
func (e *Executor) callsHereLookup(ctx context.Context, tenant, current string) callsHereLookup {
	return func(hit map[string]interface{}) []string {
		at, _ := hit["clusterContext"].(string)
		kind, _ := hit["kind"].(string)
		ns, _ := hit["namespace"].(string)
		name, _ := hit["name"].(string)
		if at == "" || current == "" || ns == "" || name == "" {
			return nil
		}
		res, err := e.external.ExternalConnections(
			cluster.WithRuntimeKey(context.WithoutCancel(ctx), cluster.RuntimeKey{Tenant: tenant, Cluster: at}),
			ExternalConnectionsQuery{Namespace: ns, WorkloadKind: kind, Workload: name, WindowMinutes: defaultExternalWindowMinutes},
		)
		if err != nil {
			return nil
		}
		return callsInto(res, current)
	}
}

// callsInto lists what a workload's connections reach in the cluster named
// by clusterContext, once each: from traffic first, then from its
// configuration.
func callsInto(res ExternalConnectionsResult, clusterContext string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(m *FleetMatch, via string) {
		if m == nil || m.ClusterContext != clusterContext {
			return
		}
		key := m.Kind + " " + m.Namespace + "/" + m.Name
		if seen[key] {
			return
		}
		seen[key] = true
		if via != "" {
			key += " (" + via + ")"
		}
		out = append(out, key)
	}
	for _, d := range res.Destinations {
		add(d.InFleet, "seen in its traffic")
	}
	for _, h := range res.DependencyHosts {
		add(h.InFleet, h.Source)
	}
	return out
}

// AnnotateSearchElsewhere rewrites the note of a search answer recorded
// before the note said what it says today, so a replay reads the current one.
func AnnotateSearchElsewhere(text string) string {
	var out map[string]interface{}
	if json.Unmarshal([]byte(text), &out) != nil || out["elsewhere"] == nil {
		return text
	}
	raw, _ := json.Marshal(out["elsewhere"])
	var hits []map[string]interface{}
	if json.Unmarshal(raw, &hits) != nil || len(hits) == 0 {
		return text
	}
	answered := 0
	if n, ok := out["otherClustersSearched"].(float64); ok {
		answered = int(n)
	}
	var late []string
	if l, ok := out["otherClustersNotAnswered"].([]interface{}); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				late = append(late, s)
			}
		}
	}
	out["note"] = elsewherePayload(hits, answered, late)["note"]
	return jsonString(out)
}
