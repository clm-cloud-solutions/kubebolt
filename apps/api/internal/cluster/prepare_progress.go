package cluster

import (
	"context"
	"reflect"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
)

// The progress a person sees while a cluster is prepared: the connect read
// without waiting for it. Nothing here changes what the API serves — the
// cluster's views still open when the whole cache has synced — it only says
// how far the connect is, so the wait has a meaning instead of a spinner.

// Prepare states.
const (
	PrepareConnecting   = "connecting"    // the runtime is being built
	PrepareReady        = "ready"         // the cluster's views are served
	PrepareWaitingAgent = "waiting_agent" // an agent-proxy cluster with no agent connected
	PrepareFailed       = "failed"        // the last connect failed; RetryAt says when the next may start
	PrepareMetricsOnly  = "metrics_only"  // no connector by design (Mode C)
	PrepareIdle         = "idle"          // nothing selected, or not connecting
)

// ConnectProgress is one cluster's connect, as far as it has gone.
type ConnectProgress struct {
	Context string `json:"context"`
	State   string `json:"state"`
	// StartedAt is when the runtime build began (connecting only).
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// ViaAgent is true for a cluster reached through its agents (agent-proxy);
	// Agents is how many are connected.
	ViaAgent bool `json:"viaAgent"`
	Agents   int  `json:"agents"`
	// AccessChecked is true once the permission probe is done; Readable of
	// Probed resource types may be listed. An optional CRD the cluster does not
	// have installed is not counted: it is not a permission KubeBolt lacks.
	AccessChecked bool `json:"accessChecked"`
	Readable      int  `json:"readable"`
	Probed        int  `json:"probed"`
	// Denied names the resource types KubeBolt may not list, sorted.
	Denied []string `json:"denied,omitempty"`
	// Types is every resource type the cache loads, with what has arrived.
	Types []TypeProgress `json:"types"`
	// BytesReceived is what the initial LISTs have brought so far.
	BytesReceived int64 `json:"bytesReceived"`
	// Error and RetryAt describe the last failed connect (failed only).
	Error   string     `json:"error,omitempty"`
	RetryAt *time.Time `json:"retryAt,omitempty"`
}

// TypeProgress is one resource type's initial load.
type TypeProgress struct {
	Kind   string `json:"kind"`
	Synced bool   `json:"synced"`
	// Loaded objects so far (from the pages; exact once synced when Total is
	// known) and Total, 0 while the API server has not said (a type that fits
	// in one page never says). Bytes is what its pages weighed so far: with
	// Loaded and Total it says how much is left in bytes, which is what the
	// tunnel takes time to carry.
	Loaded int64 `json:"loaded"`
	Total  int64 `json:"total"`
	Bytes  int64 `json:"bytes"`
}

// ConnectProgressFor is the connect of the request's cluster, resolved like
// every per-request accessor (contextForKeyLocked) and never waiting for it:
// it reads the placeholder a connect builds, it does not start one.
func (m *Manager) ConnectProgressFor(ctx context.Context) ConnectProgress {
	key := RuntimeKeyFromContext(ctx)
	if key.NoCluster {
		return ConnectProgress{State: PrepareIdle}
	}
	m.mu.RLock()
	contextName := m.contextForKeyLocked(key)
	out := ConnectProgress{Context: contextName, State: PrepareIdle}
	if contextName == "" {
		m.mu.RUnlock()
		return out
	}
	if _, ok := m.metricsOnlyContexts[contextName]; ok {
		m.mu.RUnlock()
		out.State = PrepareMetricsOnly
		return out
	}
	cid := m.agentProxyContexts[contextName]
	out.ViaAgent = cid != ""
	if cid != "" && m.agentRegistry != nil {
		out.Agents = m.agentRegistry.CountByCluster(cid)
	}
	pk, rt := m.buildingRuntimeLocked(key, contextName)
	var building *Connector
	switch {
	case contextName == m.activeContext && m.connector != nil:
		out.State = PrepareReady
	case rt != nil && rt.connector != nil:
		out.State = PrepareReady
	case rt != nil && rt.ready != nil && !isClosed(rt.ready):
		out.State = PrepareConnecting
		started := rt.startedAt
		out.StartedAt = &started
		building = rt.building.Load()
	case cid != "" && out.Agents == 0:
		out.State = PrepareWaitingAgent
	default:
		if f := m.spinFailures[pk]; f != nil {
			out.State = PrepareFailed
			out.Error = f.err.Error()
			retry := f.at.Add(spinBackoff(f.attempts))
			out.RetryAt = &retry
		}
	}
	m.mu.RUnlock()

	if building != nil {
		building.fillPrepareProgress(&out)
	}
	return out
}

// buildingRuntimeLocked finds the pooled runtime of contextName: the one a
// switch builds (keyed by the manager's tenant) or the one a request for a
// non-active cluster builds (keyed by the request's). Caller holds m.mu.
func (m *Manager) buildingRuntimeLocked(key RuntimeKey, contextName string) (poolKey, *clusterRuntime) {
	pk := poolKey{tenant: m.tenantID, cluster: contextName}
	if rt := m.runtimes[pk]; rt != nil {
		return pk, rt
	}
	alt := poolKey{tenant: key.Tenant, cluster: contextName}
	return alt, m.runtimes[alt]
}

// ConnectingClusterIDFor is the cluster_id of the request's cluster when it is
// an agent-proxy cluster whose runtime is being built, else "". The metrics
// path scopes VictoriaMetrics by it while the connect runs: the agents ship
// under that id before the cache has synced. Same resolution as
// ConnectProgressFor; never waits.
func (m *Manager) ConnectingClusterIDFor(ctx context.Context) string {
	key := RuntimeKeyFromContext(ctx)
	if key.NoCluster {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	contextName := m.contextForKeyLocked(key)
	if contextName == "" {
		return ""
	}
	_, rt := m.buildingRuntimeLocked(key, contextName)
	if rt == nil || rt.connector != nil || rt.ready == nil || isClosed(rt.ready) {
		return ""
	}
	return m.agentProxyContexts[contextName]
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// fillPrepareProgress adds what this connector has done so far: the probe's
// outcome and each informer type's load.
func (c *Connector) fillPrepareProgress(out *ConnectProgress) {
	perms := c.Permissions()
	out.AccessChecked = true
	for key, p := range perms {
		if p == nil || p.Absent {
			continue
		}
		out.Probed++
		if p.CanList {
			out.Readable++
		} else {
			out.Denied = append(out.Denied, key)
		}
	}
	sort.Strings(out.Denied)
	var lists map[string]ListCount
	if c.progress != nil {
		lists = c.progress.listCounts()
		out.BytesReceived = c.progress.listBytesReceived()
	}
	closed := make(chan struct{})
	close(closed)
	seen := map[string]bool{}
	for _, f := range append([]informers.SharedInformerFactory{c.factory}, c.nsFactories...) {
		if f == nil {
			continue
		}
		for typ, synced := range f.WaitForCacheSync(closed) {
			kind := informerKind(typ)
			if seen[kind] {
				// A namespace-scoped connection has one informer per namespace:
				// the type is synced when every one of them is.
				for i := range out.Types {
					if out.Types[i].Kind == kind {
						out.Types[i].Synced = out.Types[i].Synced && synced
					}
				}
				continue
			}
			seen[kind] = true
			plural, _ := meta.UnsafeGuessKindToResource(schema.GroupVersionKind{Kind: kind})
			lc := lists[plural.Resource]
			tp := TypeProgress{Kind: kind, Synced: synced, Loaded: lc.Loaded, Total: lc.Total, Bytes: lc.Bytes}
			out.Types = append(out.Types, tp)
		}
	}
	for i := range out.Types {
		if out.Types[i].Synced && out.Types[i].Total > 0 {
			out.Types[i].Loaded = out.Types[i].Total
		}
	}
	sort.Slice(out.Types, func(i, j int) bool { return out.Types[i].Kind < out.Types[j].Kind })
}

// informerKind is an informer's object type as a Kind: *v1.ReplicaSet →
// ReplicaSet.
func informerKind(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}
