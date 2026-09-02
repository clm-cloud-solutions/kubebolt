// Package audit persists a durable, queryable history of cluster mutations
// — UI-initiated, Kobi-proposed, and (later) Helm actions — in a single
// BoltDB bucket. It replaces the slog-only audit trail with a record that
// survives restarts and powers the admin action-history view (Sprint 1).
//
// The store mirrors the agent registry / insights store patterns:
// JSON-encoded values, sortable keys, time-based pruning, a Store interface
// with a Bolt impl for production and a Memory impl for tests.
package audit

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Record is one audited mutation. Source distinguishes "ui" from
// "copilot_proposal" (the X-KubeBolt-Action-Source header). When a Kobi
// chat was seeded from an insight, OriginatingInsightID carries that
// insight's occurrence id (Sprint 0 → closes the insight→action provenance
// loop).
type Record struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	// TenantID is the owning org. It is the isolation key: reads are scoped to
	// it and the Postgres RLS policy compares it to app.current_org. Stamped
	// from the request context at write time — never from anything the caller
	// sends, so a request cannot file a record into someone else's history.
	// Empty in single-tenant (OSS) installs, where there is one org by design.
	TenantID string `json:"tenantId,omitempty"`
	// Class separates the two kinds of event the trail carries. Empty means
	// ClassMutation — every record written before this field existed is one, so
	// the zero value reads correctly and no backfill is needed.
	Class                string         `json:"class,omitempty"`
	Source               string         `json:"source"`
	UserID               string         `json:"userId,omitempty"`
	Username             string         `json:"username,omitempty"`
	Role                 string         `json:"role,omitempty"`
	ClusterID            string         `json:"clusterId,omitempty"`
	Action               string         `json:"action"`
	TargetType           string         `json:"targetType,omitempty"`
	TargetNamespace      string         `json:"targetNamespace,omitempty"`
	TargetName           string         `json:"targetName,omitempty"`
	Params               map[string]any `json:"params,omitempty"`
	Result               string         `json:"result"` // "success" | "error"
	Error                string         `json:"error,omitempty"`
	OriginatingInsightID string         `json:"originatingInsightId,omitempty"`
	// ConversationID links a copilot_proposal-sourced action back to the Kobi
	// conversation that proposed it, so the admin action-history can jump to
	// the chat that produced the mutation ("why was this pod restarted?").
	ConversationID string `json:"conversationId,omitempty"`
}

// Record classes. ClassMutation is the zero value: something changed.
// ClassAccess is "someone reached into a running workload and could have seen
// or moved data" — a shell, a tunnel, a file read. Nothing changed, which is
// exactly why it needs its own class rather than a mutation with no target.
const (
	ClassMutation = "mutation"
	ClassAccess   = "access"
)

// ClassOf returns rec's class, resolving the empty zero value to ClassMutation.
func ClassOf(rec *Record) string {
	if rec == nil || rec.Class == "" {
		return ClassMutation
	}
	return rec.Class
}

// Store persists audit records. Safe for concurrent use.
//
// Every verb is org-scoped, deliberately. An earlier interface offered global
// List(limit) / Prune(before), and the read path shipped serving one org's
// mutation history to another's admin because reaching for the global verb was
// the path of least resistance. There is no unscoped verb to reach for now.
type Store interface {
	// Append writes one record. ID/Timestamp/TenantID are stamped by the caller.
	Append(rec *Record) error
	// ListOrg returns up to `limit` most-recent records belonging to orgID
	// (newest first). A limit <= 0 returns all of that org's records.
	ListOrg(orgID string, limit int) ([]Record, error)
	// PruneOrg deletes orgID's records older than `before`, returning the count
	// removed. Per-org rather than a single global DELETE for the same reason
	// the rest of retention is (see cmd/server/retention.go): under RLS a
	// tenant-less DELETE matches zero rows and reports success.
	PruneOrg(orgID string, before time.Time) (int, error)
}

// matchesOrg reports whether a record belongs to org, for the stores that
// filter in Go rather than in SQL.
//
// An empty record tenant counts as a match. Those are single-tenant or
// pre-upgrade records, and a Bolt-backed install has exactly one org — so
// treating them as unowned would hide an operator's own history from them at
// upgrade. Postgres deliberately does NOT share this leniency: there, an
// unattributed row is visible to no one, because "we could not tell whose this
// is" must not resolve to "everyone's" when there are several tenants.
func matchesOrg(rec *Record, orgID string) bool {
	return rec.TenantID == orgID || rec.TenantID == ""
}

// recordKey orders records chronologically in BoltDB byte order: a
// zero-padded unix-nano prefix + id (for uniqueness within the same nano).
func recordKey(rec *Record) []byte {
	return []byte(fmt.Sprintf("%020d-%s", rec.Timestamp.UnixNano(), rec.ID))
}

// ─── BoltDB implementation ────────────────────────────────────────

// BoltStore is the production Store, backed by the shared BoltDB file.
type BoltStore struct {
	db     *bolt.DB
	bucket []byte
}

// NewBoltStore wires the store to a BoltDB handle + bucket. The bucket must
// already exist — created at boot in auth.NewStore.
func NewBoltStore(db *bolt.DB, bucket []byte) *BoltStore {
	return &BoltStore{db: db, bucket: bucket}
}

func (s *BoltStore) Append(rec *Record) error {
	if rec == nil {
		return fmt.Errorf("nil audit record")
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.Put(recordKey(rec), payload)
	})
}

func (s *BoltStore) ListOrg(orgID string, limit int) ([]Record, error) {
	var out []Record
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		// Cursor in reverse = newest first (keys are time-ordered). The limit
		// is applied AFTER the org filter, so a foreign record can never
		// consume a slot and silently shorten this org's page.
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var rec Record
			if err := json.Unmarshal(v, &rec); err != nil {
				continue // skip corrupt records
			}
			if !matchesOrg(&rec, orgID) {
				continue
			}
			out = append(out, rec)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *BoltStore) PruneOrg(orgID string, before time.Time) (int, error) {
	var removed int
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		var toDelete [][]byte
		err := b.ForEach(func(k, v []byte) error {
			var rec Record
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if matchesOrg(&rec, orgID) && rec.Timestamp.Before(before) {
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

// MemoryStore is the in-memory Store for tests.
type MemoryStore struct {
	mu      sync.RWMutex
	records []Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (s *MemoryStore) Append(rec *Record) error {
	if rec == nil {
		return fmt.Errorf("nil audit record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, *rec)
	return nil
}

func (s *MemoryStore) ListOrg(orgID string, limit int) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Record
	for i := range s.records {
		if matchesOrg(&s.records[i], orgID) {
			out = append(out, s.records[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) PruneOrg(orgID string, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.records[:0]
	removed := 0
	for _, rec := range s.records {
		if matchesOrg(&rec, orgID) && rec.Timestamp.Before(before) {
			removed++
			continue
		}
		kept = append(kept, rec)
	}
	s.records = kept
	return removed, nil
}
