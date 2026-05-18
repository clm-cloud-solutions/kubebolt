package insights

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// severityRank orders insight severities from most to least actionable.
// Lower number = renders first in the sorted list.
var severityRank = map[string]int{
	"critical": 0,
	"warning":  1,
	"info":     2,
}

func severityRankOf(s string) int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return 99 // unknown severities sink to the bottom
}

// Engine runs insight rules and tracks findings.
type Engine struct {
	rules      []Rule
	insights   []models.Insight
	mu         sync.RWMutex
	wsHub      *websocket.Hub
	onNew      func(models.Insight) // hook called when a new insight is detected
	onResolved func(models.Insight) // hook called when an insight transitions to resolved
}

// NewEngine creates a new insights engine with all rules.
func NewEngine(wsHub *websocket.Hub) *Engine {
	return &Engine{
		rules: AllRules(),
		wsHub: wsHub,
	}
}

// SetOnNewInsight registers a callback that is invoked (synchronously, under
// the engine lock) for every newly detected insight. Keep the callback fast
// or dispatch asynchronously inside it — the engine holds its write lock while
// calling this, so slow callbacks block further evaluations.
func (e *Engine) SetOnNewInsight(fn func(models.Insight)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onNew = fn
}

// SetOnResolvedInsight registers a callback invoked (under the engine lock)
// when an insight transitions from active to resolved. Same performance
// caveat as SetOnNewInsight.
func (e *Engine) SetOnResolvedInsight(fn func(models.Insight)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onResolved = fn
}

// Evaluate runs all rules against the current cluster state.
func (e *Engine) Evaluate(state *ClusterState) {
	var newInsights []models.Insight
	for _, rule := range e.rules {
		results := rule.Evaluate(state)
		newInsights = append(newInsights, results...)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Build a set of current resource keys for detecting resolved insights
	currentKeys := make(map[string]bool)
	for _, insight := range newInsights {
		currentKeys[insight.Resource+"|"+insight.Title] = true
	}

	// Mark previously unresolved insights as resolved if they no longer appear
	for i := range e.insights {
		if !e.insights[i].Resolved {
			key := e.insights[i].Resource + "|" + e.insights[i].Title
			if !currentKeys[key] {
				e.insights[i].Resolved = true
				now := time.Now()
				e.insights[i].ResolvedAt = &now
				e.wsHub.Broadcast(websocket.InsightResolved, e.insights[i])
				if e.onResolved != nil {
					e.onResolved(e.insights[i])
				}
			}
		}
	}

	// Add genuinely new insights (not already tracked)
	existingKeys := make(map[string]bool)
	for _, insight := range e.insights {
		if !insight.Resolved {
			existingKeys[insight.Resource+"|"+insight.Title] = true
		}
	}
	for _, insight := range newInsights {
		key := insight.Resource + "|" + insight.Title
		if !existingKeys[key] {
			e.insights = append(e.insights, insight)
			e.wsHub.Broadcast(websocket.InsightNew, insight)
			if e.onNew != nil {
				e.onNew(insight)
			}
		}
	}

	log.Printf("Insights evaluation complete: %d active, %d total", len(newInsights), len(e.insights))
}

// GetInsights returns insights filtered by severity and resolved status.
// Results are sorted by (severity rank ASC, FirstSeen DESC) so the most
// actionable items always lead. Without this sort the order was FIFO of
// detection — and because ClusterState's underlying maps iterate in
// non-deterministic order in Go, that meant a critical insight could be
// buried under older infos AND the ranking shifted between API restarts
// on the same cluster.
func (e *Engine) GetInsights(severity string, resolved bool) []models.Insight {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var result []models.Insight
	for _, insight := range e.insights {
		if severity != "" && insight.Severity != severity {
			continue
		}
		if !resolved && insight.Resolved {
			continue
		}
		result = append(result, insight)
	}
	if result == nil {
		result = []models.Insight{}
	}
	// Stable sort so two insights with identical severity AND FirstSeen
	// keep their insertion order rather than reshuffling between calls.
	sort.SliceStable(result, func(i, j int) bool {
		ri, rj := severityRankOf(result[i].Severity), severityRankOf(result[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return result[i].FirstSeen.After(result[j].FirstSeen)
	})
	return result
}

// GetAllInsights returns all unresolved insights.
func (e *Engine) GetAllInsights() []models.Insight {
	return e.GetInsights("", false)
}
