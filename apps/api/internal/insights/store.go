package insights

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// maxOccurrences bounds the per-fingerprint occurrence ring so a flapping
// insight (resolve→re-fire→resolve…) can't grow a single record without
// limit. The OSS BoltDB store keeps the last N episodes inline; the SaaS
// Postgres store can lift the cap by promoting occurrences to their own
// table behind the same InsightStore interface.
const maxOccurrences = 20

// Occurrence is one active episode of an insight identified by a stable
// Fingerprint. A fingerprint can recur over time (crash-loop clears, then
// the pod crashes again); each active window is a distinct occurrence with
// its own ID — that's what gives the historical timeline.
type Occurrence struct {
	ID       string     `json:"id"`             // opaque per-episode id (referenced by Kobi/Autopilot)
	OpenedAt time.Time   `json:"openedAt"`
	ClosedAt *time.Time `json:"closedAt,omitempty"`
}

// InsightRecord is the persistent shape of one insight identity (one
// Fingerprint). It survives restarts and aggregates the episode history,
// so operators can follow a non-recent insight without losing context.
//
// Mirrors the agent registry's AgentRecord pattern: JSON-encoded value,
// composite key, forward-compat (adding fields is a rolling upgrade, not a
// schema migration), time-based pruning of resolved records only.
type InsightRecord struct {
	// Identity. Fingerprint = sha256(tenantID|clusterID|ruleID|resource),
	// deterministic across restarts and recurrences.
	Fingerprint string `json:"fingerprint"`
	TenantID    string `json:"tenantId"`
	ClusterID   string `json:"clusterId"`
	RuleID      string `json:"ruleId"`
	Resource    string `json:"resource"`
	Namespace   string `json:"namespace,omitempty"`

	// Latest content snapshot (refreshed on each re-detection).
	Severity   string `json:"severity"`
	Category   string `json:"category,omitempty"`
	Title      string `json:"title"`
	Message    string `json:"message,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`

	// Lifecycle. Status is "active" while the condition holds, "resolved"
	// once it clears. FirstSeen is fixed at first-ever detection; LastSeen
	// bumps on every re-detection; ResolvedAt is set when it clears.
	Status     string     `json:"status"`
	FirstSeen  time.Time  `json:"firstSeen"`
	LastSeen   time.Time  `json:"lastSeen"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`

	// CurrentOccurrenceID is the open episode's id (empty when resolved);
	// it is the value threaded to consumers (the insight→Kobi trigger,
	// ActionProposal.OriginatingInsightID, Autopilot trigger_source_ref).
	CurrentOccurrenceID string `json:"currentOccurrenceId,omitempty"`

	// Occurrences is a bounded ring of the most recent episodes.
	Occurrences []Occurrence `json:"occurrences,omitempty"`
}

// Active reports whether the record's condition currently holds.
func (r *InsightRecord) Active() bool { return r.Status == "active" }

// Fingerprint computes the stable identity hash for an insight. Keyed on
// ruleID (NOT title) so rewording a rule's text doesn't fork identity;
// resource carries the Kind/Namespace/Name so two pods don't collide.
func Fingerprint(tenantID, clusterID, ruleID, resource string) string {
	h := sha256.Sum256([]byte(tenantID + "|" + clusterID + "|" + ruleID + "|" + resource))
	return hex.EncodeToString(h[:])
}

// InsightQuery filters a List call. Zero-valued fields are unbounded.
type InsightQuery struct {
	TenantID  string
	ClusterID string
	Severity  string    // "" = any
	Status    string    // "active" | "resolved" | "" = any
	Since     time.Time // filter on LastSeen >= Since when non-zero
	Until     time.Time // filter on LastSeen <= Until when non-zero
}

// InsightStore persists insight identities across restarts. The interface
// is extracted (like AgentStore) so tests use a memory impl and SaaS v1 can
// drop in a Postgres impl with zero rekey migration — keys are already
// tenant/cluster-prefixed.
//
// Implementations must be safe for concurrent use: the engine calls Upsert
// / MarkResolved from its evaluation goroutine while the API reads via List.
type InsightStore interface {
	// Upsert writes the full record, replacing any prior copy with the
	// same (tenantID, clusterID, fingerprint).
	Upsert(rec *InsightRecord) error
	// MarkResolved sets Status=resolved + ResolvedAt and closes the open
	// occurrence. No-op when the record doesn't exist.
	MarkResolved(tenantID, clusterID, fingerprint string, at time.Time) error
	// Get returns one record by identity.
	Get(tenantID, clusterID, fingerprint string) (*InsightRecord, bool, error)
	// List returns records matching the query, newest LastSeen first.
	List(q InsightQuery) ([]InsightRecord, error)
	// Prune deletes resolved records whose ResolvedAt is older than
	// `before`. Active records never expire. Returns the count removed.
	Prune(before time.Time) (int, error)
}

// insightKey is the BoltDB key: tenant/cluster/fingerprint. Tenant-prefixed
// so SaaS multi-tenant needs no rekey, and range scans by tenant/cluster
// stay efficient. Mirrors agent registry's recordKey.
func insightKey(tenantID, clusterID, fingerprint string) []byte {
	return []byte(tenantID + "/" + clusterID + "/" + fingerprint)
}

func matchesQuery(rec *InsightRecord, q InsightQuery) bool {
	if q.TenantID != "" && rec.TenantID != q.TenantID {
		return false
	}
	if q.ClusterID != "" && rec.ClusterID != q.ClusterID {
		return false
	}
	if q.Severity != "" && rec.Severity != q.Severity {
		return false
	}
	if q.Status != "" && rec.Status != q.Status {
		return false
	}
	if !q.Since.IsZero() && rec.LastSeen.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && rec.LastSeen.After(q.Until) {
		return false
	}
	return true
}

func sortRecordsNewestFirst(out []InsightRecord) {
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
}

// ─── BoltDB implementation ────────────────────────────────────────

// BoltInsightStore is the production InsightStore — backed by the same
// BoltDB file that holds users + tenants + agents. One bucket (`insights`)
// holds JSON-encoded InsightRecord values keyed by
// `<tenantID>/<clusterID>/<fingerprint>`.
type BoltInsightStore struct {
	db     *bolt.DB
	bucket []byte
}

// NewBoltInsightStore wires the store to a BoltDB handle + bucket name.
// The bucket must already exist — it's created at boot in auth.NewStore so
// the schema lives in one place.
func NewBoltInsightStore(db *bolt.DB, bucket []byte) *BoltInsightStore {
	return &BoltInsightStore{db: db, bucket: bucket}
}

func (s *BoltInsightStore) Upsert(rec *InsightRecord) error {
	if rec == nil {
		return fmt.Errorf("nil InsightRecord")
	}
	if rec.Fingerprint == "" {
		return fmt.Errorf("InsightRecord missing fingerprint")
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal InsightRecord: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.Put(insightKey(rec.TenantID, rec.ClusterID, rec.Fingerprint), payload)
	})
}

func (s *BoltInsightStore) MarkResolved(tenantID, clusterID, fingerprint string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		key := insightKey(tenantID, clusterID, fingerprint)
		raw := b.Get(key)
		if raw == nil {
			return nil // no-op when record doesn't exist
		}
		var rec InsightRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unmarshal InsightRecord: %w", err)
		}
		closeRecord(&rec, at)
		payload, err := json.Marshal(&rec)
		if err != nil {
			return fmt.Errorf("marshal InsightRecord: %w", err)
		}
		return b.Put(key, payload)
	})
}

func (s *BoltInsightStore) Get(tenantID, clusterID, fingerprint string) (*InsightRecord, bool, error) {
	var rec InsightRecord
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		raw := b.Get(insightKey(tenantID, clusterID, fingerprint))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unmarshal InsightRecord: %w", err)
		}
		found = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return &rec, true, nil
}

func (s *BoltInsightStore) List(q InsightQuery) ([]InsightRecord, error) {
	var out []InsightRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.ForEach(func(_, v []byte) error {
			var rec InsightRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				// Skip corrupt records but keep iterating — one bad
				// value shouldn't blank the whole history on read.
				return nil
			}
			if matchesQuery(&rec, q) {
				out = append(out, rec)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sortRecordsNewestFirst(out)
	return out, nil
}

func (s *BoltInsightStore) Prune(before time.Time) (int, error) {
	var removed int
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		var toDelete [][]byte
		err := b.ForEach(func(k, v []byte) error {
			var rec InsightRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			// Active records never get pruned. Resolved records whose
			// ResolvedAt is older than the horizon drop out.
			if rec.Status == "resolved" && rec.ResolvedAt != nil && rec.ResolvedAt.Before(before) {
				keyCopy := make([]byte, len(k))
				copy(keyCopy, k)
				toDelete = append(toDelete, keyCopy)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		removed = len(toDelete)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// ─── Memory implementation (tests) ────────────────────────────────

// MemoryInsightStore is the in-memory InsightStore for tests. Same
// semantics as BoltInsightStore; thread-safe.
type MemoryInsightStore struct {
	mu      sync.RWMutex
	records map[string]*InsightRecord // key = insightKey(...) as string
}

func NewMemoryInsightStore() *MemoryInsightStore {
	return &MemoryInsightStore{records: make(map[string]*InsightRecord)}
}

func (s *MemoryInsightStore) Upsert(rec *InsightRecord) error {
	if rec == nil {
		return fmt.Errorf("nil InsightRecord")
	}
	if rec.Fingerprint == "" {
		return fmt.Errorf("InsightRecord missing fingerprint")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rec
	cp.Occurrences = append([]Occurrence(nil), rec.Occurrences...)
	s.records[string(insightKey(rec.TenantID, rec.ClusterID, rec.Fingerprint))] = &cp
	return nil
}

func (s *MemoryInsightStore) MarkResolved(tenantID, clusterID, fingerprint string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[string(insightKey(tenantID, clusterID, fingerprint))]
	if !ok {
		return nil
	}
	closeRecord(rec, at)
	return nil
}

func (s *MemoryInsightStore) Get(tenantID, clusterID, fingerprint string) (*InsightRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[string(insightKey(tenantID, clusterID, fingerprint))]
	if !ok {
		return nil, false, nil
	}
	cp := *rec
	cp.Occurrences = append([]Occurrence(nil), rec.Occurrences...)
	return &cp, true, nil
}

func (s *MemoryInsightStore) List(q InsightQuery) ([]InsightRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []InsightRecord
	for _, rec := range s.records {
		if matchesQuery(rec, q) {
			cp := *rec
			cp.Occurrences = append([]Occurrence(nil), rec.Occurrences...)
			out = append(out, cp)
		}
	}
	sortRecordsNewestFirst(out)
	return out, nil
}

func (s *MemoryInsightStore) Prune(before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int
	for k, rec := range s.records {
		if rec.Status == "resolved" && rec.ResolvedAt != nil && rec.ResolvedAt.Before(before) {
			delete(s.records, k)
			removed++
		}
	}
	return removed, nil
}

// closeRecord marks a record resolved and closes its open occurrence. Shared
// by both store impls so the lifecycle transition is single-sourced.
func closeRecord(rec *InsightRecord, at time.Time) {
	if rec.Status == "resolved" {
		return
	}
	rec.Status = "resolved"
	rec.ResolvedAt = &at
	rec.LastSeen = at
	// Close the open occurrence (the last one, if it isn't already closed).
	if n := len(rec.Occurrences); n > 0 && rec.Occurrences[n-1].ClosedAt == nil {
		rec.Occurrences[n-1].ClosedAt = &at
	}
	rec.CurrentOccurrenceID = ""
}

// appendOccurrence opens a new episode on a record and trims the ring to the
// most recent maxOccurrences. Returns the new occurrence id.
func appendOccurrence(rec *InsightRecord, id string, at time.Time) {
	rec.Occurrences = append(rec.Occurrences, Occurrence{ID: id, OpenedAt: at})
	if len(rec.Occurrences) > maxOccurrences {
		rec.Occurrences = rec.Occurrences[len(rec.Occurrences)-maxOccurrences:]
	}
	rec.CurrentOccurrenceID = id
}
