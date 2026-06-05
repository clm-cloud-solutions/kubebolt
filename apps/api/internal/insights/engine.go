package insights

import (
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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

	// Sprint 0: persistence. store may be nil (no durable history — the
	// engine then behaves exactly as the pre-Sprint-0 in-memory version).
	// clusterID + tenantID scope every persisted record; tenantID is
	// "default" in OSS single-tenant.
	store     InsightStore
	clusterID string
	tenantID  string

	// broadcastGate, when non-nil and false, suppresses this engine's OUTBOUND
	// signals — both WebSocket broadcasts (insight:new / insight:resolved) AND
	// the notification hooks (onNew / onResolved → Slack/email). Set by the
	// manager when the engine's runtime is PARKED in the connector pool (W2). A
	// parked runtime keeps evaluating and persisting insights, but nobody is
	// viewing its cluster, so emitting either channel would surface events for a
	// cluster that isn't on screen. nil = always emit (the default for any
	// engine built without a gate).
	//
	// TEMPORARY — gating NOTIFICATIONS on "is this the active cluster" is a
	// stopgap, NOT the desired behavior. A notification should fire whenever an
	// event warrants it, independent of whether anyone has that cluster open in
	// the UI. The proper fix (event-driven notifications decoupled from the
	// active-cluster view) lands with A.4 / the EE notifications work. Until
	// then we keep the pre-pool behavior (only the active cluster notifies) so
	// the pool doesn't start spraying notifications for every parked cluster.
	// WS suppression here is correct long-term; notification suppression is the
	// temporary part. See internal/kubebolt-w2-connector-pool-design.md §4c.
	broadcastGate *atomic.Bool
}

// SetBroadcastGate wires the active/parked gate shared with this engine's
// connector. Called once at runtime construction, before the eval loop starts.
func (e *Engine) SetBroadcastGate(g *atomic.Bool) {
	e.broadcastGate = g
}

// outboundEnabled reports whether this engine's runtime is active, i.e. should
// emit outbound signals (WS broadcasts and notification hooks). false when the
// runtime is parked in the pool. See the broadcastGate field doc.
func (e *Engine) outboundEnabled() bool {
	return e.broadcastGate == nil || e.broadcastGate.Load()
}

// broadcast pushes to the WS hub unless this engine's runtime is parked.
func (e *Engine) broadcast(msgType string, data interface{}) {
	if !e.outboundEnabled() {
		return
	}
	e.wsHub.Broadcast(msgType, data)
}

// NewEngine creates a new insights engine with all rules. store may be nil
// (in-memory only). clusterID scopes persisted records to the cluster the
// engine evaluates; tenantID defaults to "default" (OSS single-tenant).
func NewEngine(wsHub *websocket.Hub, store InsightStore, clusterID, tenantID string) *Engine {
	if tenantID == "" {
		tenantID = "default"
	}
	return &Engine{
		rules:     AllRules(),
		wsHub:     wsHub,
		store:     store,
		clusterID: clusterID,
		tenantID:  tenantID,
	}
}

// SetStore wires (or replaces) the persistence store + tenant scope on a LIVE
// engine. Needed because the manager's initial cluster connection is async and
// can create the engine BEFORE main.go calls SetInsightStore — in that boot
// race the engine would otherwise capture a nil store for its whole lifetime,
// silently disabling history reads + persistence for the session (and only
// recovering on a luckier restart). The clusterID is intentionally left as the
// engine resolved it at connect time (the stable kube-system UID), so records
// key consistently across restarts regardless of which side won the race.
func (e *Engine) SetStore(store InsightStore, tenantID string) {
	if tenantID == "" {
		tenantID = "default"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store = store
	e.tenantID = tenantID
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

// Evaluate runs all rules against the current cluster state. Each produced
// insight is stamped with its rule's identity (RuleID), the engine's
// tenant/cluster, and a stable Fingerprint, then reconciled against the
// in-memory set and (when a store is wired) persisted: new identities open
// an occurrence, still-active ones bump LastSeen, and cleared ones are
// marked resolved. Persistence preserves FirstSeen across restarts and
// recurrences — the whole point of Sprint 0.
func (e *Engine) Evaluate(state *ClusterState) {
	var produced []models.Insight
	for _, rule := range e.rules {
		results := rule.Evaluate(state)
		for i := range results {
			results[i].RuleID = rule.ID
			results[i].TenantID = e.tenantID
			results[i].ClusterID = e.clusterID
			results[i].Fingerprint = Fingerprint(e.tenantID, e.clusterID, rule.ID, results[i].Resource)
		}
		produced = append(produced, results...)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()

	// Index this tick's production by fingerprint (first wins on the rare
	// dup — e.g. two crash-looping containers in one pod share a key).
	current := make(map[string]models.Insight)
	var order []string
	for _, ins := range produced {
		if _, dup := current[ins.Fingerprint]; !dup {
			current[ins.Fingerprint] = ins
			order = append(order, ins.Fingerprint)
		}
	}

	// Resolve: previously-active in-memory insights no longer present.
	for i := range e.insights {
		if e.insights[i].Resolved {
			continue
		}
		if _, ok := current[e.insights[i].Fingerprint]; !ok {
			e.insights[i].Resolved = true
			e.insights[i].ResolvedAt = &now
			e.persistResolved(e.insights[i], now)
			e.broadcast(websocket.InsightResolved, e.insights[i])
			// Parked runtimes still persist (above) but don't notify — see the
			// TEMPORARY note on broadcastGate.
			if e.outboundEnabled() && e.onResolved != nil {
				e.onResolved(e.insights[i])
			}
		}
	}

	// Index active in-memory insights by fingerprint.
	activeIdx := make(map[string]int)
	for i := range e.insights {
		if !e.insights[i].Resolved {
			activeIdx[e.insights[i].Fingerprint] = i
		}
	}

	for _, fp := range order {
		ins := current[fp]
		if idx, ok := activeIdx[fp]; ok {
			// Still active — refresh content + LastSeen, persist.
			e.insights[idx].Severity = ins.Severity
			e.insights[idx].Message = ins.Message
			e.insights[idx].Suggestion = ins.Suggestion
			e.insights[idx].LastSeen = now
			e.persistActive(&e.insights[idx], now)
		} else {
			// New to this engine instance — consult the store to preserve
			// FirstSeen and open/reopen an occurrence (restart survival).
			newIns, freshEpisode := e.admitNew(ins, now)
			e.insights = append(e.insights, newIns)
			e.broadcast(websocket.InsightNew, newIns)
			// Only fire the notification hook for a GENUINELY new episode
			// (brand-new identity or reopen-after-resolve). An insight that
			// merely survived a backend restart is a continuation, not a new
			// finding — firing onNew there is the restart-renotify spam bug.
			// outboundEnabled gates parked runtimes (TEMPORARY — see the
			// broadcastGate note; notifications shouldn't depend on the active
			// cluster long-term).
			if freshEpisode && e.outboundEnabled() && e.onNew != nil {
				e.onNew(newIns)
			}
		}
	}

	log.Printf("Insights evaluation complete: %d active, %d total", len(current), len(e.insights))
}

// admitNew stamps occurrence identity onto a freshly-detected insight,
// consulting the store so a recurring or restart-surviving identity keeps
// its original FirstSeen and either continues its open occurrence or opens
// a new one. Persists the resulting record. Returns the in-memory insight
// and whether this is a GENUINELY new episode (brand-new identity or
// reopen-after-resolve) vs a continuation of an episode that was already
// active in the store (a restart). Callers use the bool to decide whether
// to fire notification hooks — continuations must NOT re-notify.
func (e *Engine) admitNew(ins models.Insight, now time.Time) (models.Insight, bool) {
	occID := uuid.New().String()
	ins.LastSeen = now
	ins.Resolved = false
	ins.ResolvedAt = nil

	if e.store == nil {
		// No durable history — treat every detection as a fresh episode
		// (pre-Sprint-0 behavior; restart-dedup needs the store).
		ins.FirstSeen = now
		ins.ID = occID
		return ins, true
	}

	freshEpisode := true
	rec, found, err := e.store.Get(e.tenantID, e.clusterID, ins.Fingerprint)
	if err != nil || !found || rec == nil {
		// Brand-new identity.
		rec = &InsightRecord{
			Fingerprint: ins.Fingerprint,
			TenantID:    e.tenantID,
			ClusterID:   e.clusterID,
			RuleID:      ins.RuleID,
			Resource:    ins.Resource,
			Namespace:   ins.Namespace,
			FirstSeen:   now,
		}
		appendOccurrence(rec, occID, now)
		ins.FirstSeen = now
		ins.ID = occID
	} else {
		// Known identity — recurred or survived a restart.
		ins.FirstSeen = rec.FirstSeen
		if rec.Status == "active" && rec.CurrentOccurrenceID != "" {
			// Continuation after restart: keep the open occurrence and
			// suppress the new-insight notification.
			ins.ID = rec.CurrentOccurrenceID
			freshEpisode = false
		} else {
			// Reopen: a new episode on the existing identity.
			appendOccurrence(rec, occID, now)
			ins.ID = occID
		}
	}

	rec.Severity = ins.Severity
	rec.Category = ins.Category
	rec.Title = ins.Title
	rec.Message = ins.Message
	rec.Suggestion = ins.Suggestion
	rec.Status = "active"
	rec.ResolvedAt = nil
	rec.LastSeen = now
	if err := e.store.Upsert(rec); err != nil {
		log.Printf("insights: persist new %s failed: %v", ins.Fingerprint, err)
	}
	return ins, freshEpisode
}

// persistActive refreshes a still-active insight's record (content +
// LastSeen) without opening a new occurrence.
func (e *Engine) persistActive(ins *models.Insight, now time.Time) {
	if e.store == nil {
		return
	}
	rec, found, err := e.store.Get(e.tenantID, e.clusterID, ins.Fingerprint)
	if err != nil || !found || rec == nil {
		// Defensive rebuild — the record should already exist.
		rec = &InsightRecord{
			Fingerprint: ins.Fingerprint,
			TenantID:    e.tenantID,
			ClusterID:   e.clusterID,
			RuleID:      ins.RuleID,
			Resource:    ins.Resource,
			Namespace:   ins.Namespace,
			FirstSeen:   ins.FirstSeen,
		}
		appendOccurrence(rec, ins.ID, now)
	}
	rec.Severity = ins.Severity
	rec.Category = ins.Category
	rec.Title = ins.Title
	rec.Message = ins.Message
	rec.Suggestion = ins.Suggestion
	rec.Status = "active"
	rec.ResolvedAt = nil
	rec.LastSeen = now
	if err := e.store.Upsert(rec); err != nil {
		log.Printf("insights: persist active %s failed: %v", ins.Fingerprint, err)
	}
}

// persistResolved marks an insight's identity resolved in the store.
func (e *Engine) persistResolved(ins models.Insight, now time.Time) {
	if e.store == nil {
		return
	}
	if err := e.store.MarkResolved(e.tenantID, e.clusterID, ins.Fingerprint, now); err != nil {
		log.Printf("insights: persist resolved %s failed: %v", ins.Fingerprint, err)
	}
}

// ListHistory returns persisted insight records (active + resolved) for
// this engine's tenant/cluster, filtered by q. Returns an empty slice when
// no store is wired. Powers the /insights?history=true path.
func (e *Engine) ListHistory(q InsightQuery) ([]InsightRecord, error) {
	if e.store == nil {
		return []InsightRecord{}, nil
	}
	if q.TenantID == "" {
		q.TenantID = e.tenantID
	}
	if q.ClusterID == "" {
		q.ClusterID = e.clusterID
	}
	return e.store.List(q)
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
