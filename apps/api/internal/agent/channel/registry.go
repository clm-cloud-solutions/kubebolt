package channel

import (
	"sort"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

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
	// this agent. Empty until commit 5 wires the AgentProxyTransport.
	Pending *Multiplexor

	// closed is signalled when Close() runs. Goroutines spawned by the
	// server handler can select on Closed() to know when to exit.
	closeOnce sync.Once
	closed    chan struct{}
}

// NewAgent builds an Agent record. The Multiplexor is allocated up-front
// so transport callers can register pending requests as soon as Get()
// returns the agent.
func NewAgent(clusterID, agentID, nodeName string, identity *auth.AgentIdentity) *Agent {
	return &Agent{
		ClusterID: clusterID,
		AgentID:   agentID,
		NodeName:  nodeName,
		Identity:  identity,
		Connected: time.Now().UTC(),
		Pending:   NewMultiplexor(),
		closed:    make(chan struct{}),
	}
}

// Closed returns a channel that is closed when Close() is called.
// Goroutines that hold a reference to the agent (e.g. background
// watchers) should select on it to exit cleanly.
func (a *Agent) Closed() <-chan struct{} { return a.closed }

// Close marks the agent disconnected and cancels every pending request.
// Idempotent.
func (a *Agent) Close() {
	a.closeOnce.Do(func() {
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

// AgentRegistry indexes connected agents by cluster_id. One entry per
// cluster — a fresh handshake from the same cluster_id evicts the
// previous Agent (and the old handler is responsible for receiving
// Close() on the evicted record).
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*Agent
}

func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{agents: make(map[string]*Agent)}
}

// Register inserts a, replacing any existing entry for the same
// cluster_id. Returns the evicted Agent (or nil) so the caller can
// Close() it to make the previous handler's goroutines exit. The
// caller MUST do that before its own handler returns; otherwise the
// stale Agent's Multiplexor leaks.
func (r *AgentRegistry) Register(a *Agent) (evicted *Agent) {
	if a == nil {
		return nil
	}
	r.mu.Lock()
	evicted = r.agents[a.ClusterID]
	r.agents[a.ClusterID] = a
	r.mu.Unlock()
	return evicted
}

// Get returns the live agent for cluster_id, or nil.
func (r *AgentRegistry) Get(clusterID string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[clusterID]
}

// Unregister removes the agent ONLY if it is still the registered one
// (pointer-equal). This protects against a race where:
//
//  1. agent A1 is connected,
//  2. agent A2 (same cluster_id) connects, evicts A1, registers itself,
//  3. A1's handler exits and would otherwise un-register A2.
//
// Without the equality check Unregister would silently undo A2's
// registration. With it, the stale call is a no-op.
//
// Idempotent.
func (r *AgentRegistry) Unregister(a *Agent) {
	if a == nil {
		return
	}
	r.mu.Lock()
	if cur, ok := r.agents[a.ClusterID]; ok && cur == a {
		delete(r.agents, a.ClusterID)
	}
	r.mu.Unlock()
}

// List returns a snapshot of the current registry, sorted by ClusterID
// for stable test output. Cheap enough at Sprint A.5 scale (100 agents);
// SaaS scale will need a streaming variant.
func (r *AgentRegistry) List() []AgentSummary {
	r.mu.RLock()
	out := make([]AgentSummary, 0, len(r.agents))
	for _, a := range r.agents {
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
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ClusterID < out[j].ClusterID })
	return out
}

// Count returns the number of currently connected agents.
func (r *AgentRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}
