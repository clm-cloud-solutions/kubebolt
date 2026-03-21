package cluster

import (
	"sync"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// TopologyGraph is an in-memory graph of cluster resources and relationships.
type TopologyGraph struct {
	mu    sync.RWMutex
	nodes map[string]models.TopologyNode
	edges []models.TopologyEdge
}

// NewTopologyGraph creates an empty graph.
func NewTopologyGraph() *TopologyGraph {
	return &TopologyGraph{
		nodes: make(map[string]models.TopologyNode),
	}
}

// AddNode adds or replaces a node in the graph.
// It ensures Label and Kind are always populated.
func (g *TopologyGraph) AddNode(node models.TopologyNode) {
	if node.Label == "" {
		node.Label = node.Name
	}
	if node.Kind == "" {
		node.Kind = node.Type
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes[node.ID] = node
}

// UpdateNode updates an existing node if present.
func (g *TopologyGraph) UpdateNode(node models.TopologyNode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes[node.ID] = node
}

// RemoveNode removes a node and any edges referencing it.
func (g *TopologyGraph) RemoveNode(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.nodes, id)
	filtered := g.edges[:0]
	for _, e := range g.edges {
		if e.Source != id && e.Target != id {
			filtered = append(filtered, e)
		}
	}
	g.edges = filtered
}

// AddEdge appends an edge (deduplication is handled during rebuild).
func (g *TopologyGraph) AddEdge(edge models.TopologyEdge) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.edges = append(g.edges, edge)
}

// SetEdges replaces all edges (used during a full rebuild).
func (g *TopologyGraph) SetEdges(edges []models.TopologyEdge) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.edges = edges
}

// GetTopology returns a snapshot of the current topology.
func (g *TopologyGraph) GetTopology() models.Topology {
	g.mu.RLock()
	defer g.mu.RUnlock()
	nodes := make([]models.TopologyNode, 0, len(g.nodes))
	for _, n := range g.nodes {
		nodes = append(nodes, n)
	}
	edges := make([]models.TopologyEdge, len(g.edges))
	copy(edges, g.edges)
	return models.Topology{
		Nodes: nodes,
		Edges: edges,
	}
}
