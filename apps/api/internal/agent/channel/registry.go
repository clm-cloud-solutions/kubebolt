package channel

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// Sender is the abstraction over the gRPC server stream that the Agent
// uses to push BackendMessages back to the agent. The Channel handler
// in apps/api/internal/agent/server.go provides the concrete impl by
// wrapping the agentv2.AgentChannel_ChannelServer it received.
//
// Extracted as an interface so tests can plug a chan-backed stub in
// without spinning up a real gRPC stream.
type Sender interface {
	Send(*agentv2.BackendMessage) error
}

// ErrAgentClosed is returned by Send when the underlying stream has
// been torn down. Transport callers should treat it as a signal to
// fail the in-flight RoundTrip and let the upper layer retry.
var ErrAgentClosed = errors.New("channel: agent closed")

// Agent is the backend's in-memory record of a single connected agent.
// One per open AgentChannel stream. The Multiplexor is owned by the
// Agent — when the agent disconnects, every in-flight request via that
// agent is cancelled.
type Agent struct {
	ClusterID string
	AgentID   string
	NodeName  string
	Identity  *auth.AgentIdentity
	Connected time.Time

	// Pending owns request_id correlation for kube_request issued via
	// this agent.
	Pending *Multiplexor

	// sendMu serializes Send calls — gRPC ServerStreams are not safe
	// for concurrent Send. The handler's main loop (heartbeat ack) and
	// the AgentProxyTransport.RoundTrip path both go through here.
	sendMu sync.Mutex
	sender Sender // nil after Close

	closeOnce sync.Once
	closed    chan struct{}
}

// NewAgent builds an Agent record. sender may be nil when the caller
// only needs the registry+pending machinery (typical in unit tests
// that don't exercise the outbound path); production code from
// server.go always passes a real Sender wrapping the stream.
func NewAgent(clusterID, agentID, nodeName string, identity *auth.AgentIdentity, sender Sender) *Agent {
	return &Agent{
		ClusterID: clusterID,
		AgentID:   agentID,
		NodeName:  nodeName,
		Identity:  identity,
		Connected: time.Now().UTC(),
		Pending:   NewMultiplexor(),
		sender:    sender,
		closed:    make(chan struct{}),
	}
}

// Send pushes one BackendMessage to the agent's stream. Safe for
// concurrent callers. Returns ErrAgentClosed when Close() has run or
// the agent was constructed without a sender.
func (a *Agent) Send(msg *agentv2.BackendMessage) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if a.sender == nil {
		return ErrAgentClosed
	}
	return a.sender.Send(msg)
}

// Closed returns a channel that is closed when Close() is called.
// Goroutines that hold a reference to the agent (e.g. background
// watchers) should select on it to exit cleanly.
func (a *Agent) Closed() <-chan struct{} { return a.closed }

// Close marks the agent disconnected, drops the sender so any
// concurrent Send calls fail fast, and cancels every pending request.
// Idempotent.
func (a *Agent) Close() {
	a.closeOnce.Do(func() {
		a.sendMu.Lock()
		a.sender = nil
		a.sendMu.Unlock()
		close(a.closed)
		if a.Pending != nil {
			a.Pending.CancelAll()
		}
	})
}

// AgentSummary is the public shape used by List() — leaves out the
// Multiplexor and the close machinery so admin handlers can serialize
// it directly.
type AgentSummary struct {
	ClusterID string    `json:"clusterId"`
	AgentID   string    `json:"agentId"`
	NodeName  string    `json:"nodeName"`
	TenantID  string    `json:"tenantId,omitempty"`
	AuthMode  string    `json:"authMode,omitempty"`
	Connected time.Time `json:"connected"`
}

// AgentRegistry indexes connected agents by (cluster_id, agent_id).
//
// A DaemonSet is the canonical agent deployment shape: one Pod per
// node, all reporting the same cluster_id but different agent_ids
// (sha256 of tenant|cluster|node). Indexing by cluster_id alone would
// have them evict each other on every Hello. Instead the registry
// keeps one bucket per cluster_id with one slot per agent_id; reconnects
// from the same agent_id (same node) evict the previous record, which
// is the legitimate case the eviction-on-Register contract was meant
// to cover.
//
// Get(clusterID) returns ANY one of the connected agents — they're
// peers from the apiserver's perspective, all hitting the same
// in-cluster config when serving kube_request. Sprint A.5 doesn't
// need leader election for the proxy path; the first connected
// agent is fine. Sprint B+ may swap in a leased leader to align with
// the flow-collector pattern.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]map[string]*Agent // cluster_id → agent_id → Agent

	// store is the persistent backing for restart-survival. Optional —
	// when nil, the registry is purely in-memory (the legacy
	// behavior). main.go wires a BoltAgentStore when auth is enabled,
	// since that's the only mode where the BoltDB file exists.
	store AgentStore

	// helloMeta carries the extra context (capabilities, display
	// name, agent version) that the server's handler captured at
	// Hello time but didn't fit into the Agent struct. The registry
	// reads it on Register so the persisted record is complete.
	// Populated via SetHelloMeta before Register; cleared on
	// Unregister.
	helloMeta map[string]map[string]HelloMeta // cluster_id → agent_id → meta
}

// HelloMeta is the subset of the Hello envelope the registry persists
// alongside the Agent record. The handler in agent/server.go captures
// these from the Hello message at registration time.
type HelloMeta struct {
	Capabilities []string
	DisplayName  string // from Hello.Labels["kubebolt.io/cluster-name"]
	AgentVersion string
}

func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		agents:    make(map[string]map[string]*Agent),
		helloMeta: make(map[string]map[string]HelloMeta),
	}
}

// SetStore enables persistence. Calling with nil disables it. Idempotent;
// safe to call after construction (typical: main.go wires it post-auth-
// store-init). Existing records in the store are NOT auto-loaded — the
// caller drives boot-time restoration via store.List() so the cluster.
// Manager can stage AddAgentProxyCluster calls in the right phase.
func (r *AgentRegistry) SetStore(s AgentStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = s
}

// SetHelloMeta records the per-agent metadata captured from Hello so
// it can flow into the persisted AgentRecord on the upcoming Register.
// Cleared on Unregister. No-op when the registry has no store.
func (r *AgentRegistry) SetHelloMeta(clusterID, agentID string, meta HelloMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket, ok := r.helloMeta[clusterID]
	if !ok {
		bucket = make(map[string]HelloMeta)
		r.helloMeta[clusterID] = bucket
	}
	bucket[agentID] = meta
}

// Register inserts a, replacing any existing entry for the same
// (cluster_id, agent_id). Returns the evicted Agent (or nil) — that's
// the previous-session record from the SAME node, which is the only
// case where eviction is appropriate. Different nodes (different
// agent_id) coexist in the same cluster_id bucket.
//
// Caller MUST Close() the evicted Agent before its own handler returns,
// otherwise the stale Agent's Multiplexor leaks.
func (r *AgentRegistry) Register(a *Agent) (evicted *Agent) {
	if a == nil {
		return nil
	}
	r.mu.Lock()
	bucket, ok := r.agents[a.ClusterID]
	if !ok {
		bucket = make(map[string]*Agent)
		r.agents[a.ClusterID] = bucket
	}
	evicted = bucket[a.AgentID]
	bucket[a.AgentID] = a
	store := r.store
	var meta HelloMeta
	if mb, ok := r.helloMeta[a.ClusterID]; ok {
		meta = mb[a.AgentID]
	}
	r.mu.Unlock()

	// Persist OUTSIDE the mutex — BoltDB writes are slow (~ms) and we
	// don't want them blocking concurrent Get/Unregister. The store
	// is internally synchronized.
	if store != nil {
		now := time.Now().UTC()
		rec := &AgentRecord{
			ClusterID:    a.ClusterID,
			AgentID:      a.AgentID,
			NodeName:     a.NodeName,
			FirstSeen:    a.Connected,
			LastSeen:     now,
			Capabilities: meta.Capabilities,
			DisplayName:  meta.DisplayName,
			AgentVersion: meta.AgentVersion,
		}
		if a.Identity != nil {
			rec.TenantID = a.Identity.TenantID
			rec.AuthMode = string(a.Identity.Mode)
		}
		// Errors are non-fatal — persistence is "nice to have"; the
		// in-memory registry already holds the live state. Logging is
		// the responsibility of the caller (server.go) which has the
		// slog context for the registration.
		_ = store.Upsert(rec)
	}
	return evicted
}

// Get returns one connected agent for cluster_id, or nil if none.
// Multi-agent clusters: the choice is currently arbitrary (map
// iteration order). Sprint A.5 doesn't care which one services the
// kube_request — they all share the same in-cluster config. Sprint B+
// can layer health-checking / leader-election on top.
func (r *AgentRegistry) Get(clusterID string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.agents[clusterID] {
		return a
	}
	return nil
}

// GetByAgentID returns the agent with the exact (cluster_id, agent_id)
// pair, or nil. Used by admin handlers that want to address a
// specific node.
func (r *AgentRegistry) GetByAgentID(clusterID, agentID string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if bucket, ok := r.agents[clusterID]; ok {
		return bucket[agentID]
	}
	return nil
}

// Unregister removes the agent ONLY if its bucket slot still points at
// the same instance (pointer-equal). Protects against the race where:
//
//  1. node-A connects (Agent A1),
//  2. node-A reconnects (Agent A2 with same cluster_id + agent_id),
//     A2's Register evicts A1,
//  3. A1's handler exits and runs defer-Unregister.
//
// Without the equality check the stale Unregister would silently
// remove A2.
//
// Idempotent.
func (r *AgentRegistry) Unregister(a *Agent) {
	if a == nil {
		return
	}
	r.mu.Lock()
	bucket, ok := r.agents[a.ClusterID]
	if !ok {
		r.mu.Unlock()
		return
	}
	var removed bool
	if cur, ok := bucket[a.AgentID]; ok && cur == a {
		delete(bucket, a.AgentID)
		if len(bucket) == 0 {
			delete(r.agents, a.ClusterID)
		}
		// Also clear the helloMeta cache for this slot — the
		// next Register from a brand-new connection will refresh.
		if mb, ok := r.helloMeta[a.ClusterID]; ok {
			delete(mb, a.AgentID)
			if len(mb) == 0 {
				delete(r.helloMeta, a.ClusterID)
			}
		}
		removed = true
	}
	store := r.store
	r.mu.Unlock()

	// Mark the persisted record as disconnected (don't delete — keep
	// it for forensics + cluster.Manager boot restore). Only fires
	// when we actually removed our entry from the live map; if the
	// pointer-equality check failed (stale Unregister from a previous
	// session — see comment above) we don't touch the store.
	if removed && store != nil {
		_ = store.MarkDisconnected(a.ClusterID, a.AgentID, time.Now().UTC())
	}
}

// List returns a snapshot of every connected agent across all clusters,
// sorted (cluster_id, agent_id) for stable test output.
func (r *AgentRegistry) List() []AgentSummary {
	r.mu.RLock()
	var out []AgentSummary
	for _, bucket := range r.agents {
		for _, a := range bucket {
			s := AgentSummary{
				ClusterID: a.ClusterID,
				AgentID:   a.AgentID,
				NodeName:  a.NodeName,
				Connected: a.Connected,
			}
			if a.Identity != nil {
				s.TenantID = a.Identity.TenantID
				s.AuthMode = string(a.Identity.Mode)
			}
			out = append(out, s)
		}
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClusterID != out[j].ClusterID {
			return out[i].ClusterID < out[j].ClusterID
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}

// Count returns the total number of currently connected agents
// across all clusters.
func (r *AgentRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, bucket := range r.agents {
		n += len(bucket)
	}
	return n
}

// CountByCluster returns the number of agents connected for the given
// cluster_id. 0 when no bucket exists.
func (r *AgentRegistry) CountByCluster(clusterID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents[clusterID])
}
