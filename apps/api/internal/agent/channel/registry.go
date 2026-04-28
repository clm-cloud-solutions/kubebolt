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
}

func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{agents: make(map[string]map[string]*Agent)}
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
	r.mu.Unlock()
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
	defer r.mu.Unlock()
	bucket, ok := r.agents[a.ClusterID]
	if !ok {
		return
	}
	if cur, ok := bucket[a.AgentID]; ok && cur == a {
		delete(bucket, a.AgentID)
		if len(bucket) == 0 {
			delete(r.agents, a.ClusterID)
		}
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
