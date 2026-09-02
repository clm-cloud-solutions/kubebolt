package findings

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

// Runtime-event persistence (E2 SEC-E) — the `runtime_events` half of
// the security-signals family. Unlike findings, events are a
// POINT-IN-TIME stream: no fingerprint reconcile, no resolve — just
// append, list newest-first, and prune by age. Falco is the first
// producer (Decision B: the ingest endpoint stamps tenant/cluster
// from the bearer token and appends here).

// EventRecord wraps one normalized RuntimeEvent with identity.
type EventRecord struct {
	ID        string `json:"id"` // time-prefixed, unique — natural sort = chronological
	TenantID  string `json:"tenantId"`
	ClusterID string `json:"clusterId"`

	integrations.RuntimeEvent

	ReceivedAt time.Time `json:"receivedAt"`
}

// EventQuery filters ListEvents. Empty fields match everything.
type EventQuery struct {
	TenantID  string
	ClusterID string
	Source    string
	Priority  string
	Since     time.Time
	Limit     int
}

// EventStore is the persistence contract — Bolt in OSS, Postgres in
// EE, one behavior.
type EventStore interface {
	// Append writes one event. The caller (ingest layer) stamps
	// identity; ID/ReceivedAt are filled here when absent.
	Append(rec *EventRecord) error
	// ListEvents returns matching events, newest first.
	ListEvents(q EventQuery) ([]EventRecord, error)
	// PruneEvents deletes events older than `before` (retention).
	//
	// DO NOT use this on the EE/Postgres path — no org context means RLS
	// matches nothing and it deletes silently nothing. Use PruneEventsOrg.
	PruneEvents(before time.Time) (int, error)
	// PruneEventsOrg is PruneEvents scoped to ONE org.
	PruneEventsOrg(orgID string, before time.Time) (int, error)
}

// newEventID builds a time-prefixed unique id: RFC3339Nano keeps the
// Bolt key range chronologically ordered per (tenant, cluster); the
// random suffix breaks same-nanosecond ties.
//
// Callers that HAVE the raw payload should use EventIDFromPayload instead —
// this one is deliberately non-deterministic and cannot dedupe a redelivery.
func newEventID(at time.Time) string {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return at.UTC().Format(time.RFC3339Nano) + "-" + hex.EncodeToString(suffix)
}

// EventIDFromPayload derives the id from the payload itself, so the SAME event
// delivered twice is stored once.
//
// It exists because a push source retries. falcosidekick reposts byte-for-byte
// on any non-2xx or network error — observed doing exactly that while the API
// was down — and with a random suffix every retry minted a fresh id, so the
// `ON CONFLICT (tenant, cluster, id) DO NOTHING` guarding the table could never
// fire. A 5xx, a timeout or an API restart silently doubled the feed and the
// "threats in 24h" count.
//
// Keeps the RFC3339Nano prefix so Bolt's key range stays chronological; only
// the suffix changes, from randomness to content. Two genuinely distinct events
// differ in at least their nanosecond timestamp, so their payloads differ and
// they never collapse — the dedup is exact, not a heuristic window.
//
// `seq` distinguishes several events parsed out of ONE body. Falco posts one
// per request today, but a batching sender would otherwise see all but the
// first swallowed.
//
// NOT a defence against the same syscall observed by two Falco instances: each
// eBPF program timestamps independently, so those payloads differ by
// microseconds and hash apart. That shape only occurs when instances share a
// kernel (kind), and matching it would need a fuzzy time window — which drops
// real events, like one rule firing twice in a loop.
func EventIDFromPayload(at time.Time, body []byte, seq int) string {
	h := sha256.New()
	h.Write(body)
	fmt.Fprintf(h, "|%d", seq)
	return at.UTC().Format(time.RFC3339Nano) + "-" + hex.EncodeToString(h.Sum(nil)[:8])
}

func eventKey(tenantID, clusterID, id string) []byte {
	return []byte(tenantID + "/" + clusterID + "/" + id)
}

func eventMatches(rec *EventRecord, q EventQuery) bool {
	if q.TenantID != "" && rec.TenantID != q.TenantID {
		return false
	}
	if q.ClusterID != "" && rec.ClusterID != q.ClusterID {
		return false
	}
	if q.Source != "" && rec.Source != q.Source {
		return false
	}
	if q.Priority != "" && rec.Priority != q.Priority {
		return false
	}
	if !q.Since.IsZero() && rec.At.Before(q.Since) {
		return false
	}
	return true
}

// ─── BoltDB implementation (OSS) ─────────────────────────────────

// BoltEventStore holds JSON-encoded EventRecords in one bucket keyed
// `<tenantID>/<clusterID>/<time-ordered id>`.
type BoltEventStore struct {
	db     *bolt.DB
	bucket []byte
}

func NewBoltEventStore(db *bolt.DB, bucket []byte) *BoltEventStore {
	return &BoltEventStore{db: db, bucket: bucket}
}

func (s *BoltEventStore) Append(rec *EventRecord) error {
	if rec == nil {
		return fmt.Errorf("nil EventRecord")
	}
	if rec.ReceivedAt.IsZero() {
		rec.ReceivedAt = time.Now().UTC()
	}
	if rec.ID == "" {
		rec.ID = newEventID(rec.At)
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.Put(eventKey(rec.TenantID, rec.ClusterID, rec.ID), payload)
	})
}

func (s *BoltEventStore) ListEvents(q EventQuery) ([]EventRecord, error) {
	out := []EventRecord{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.ForEach(func(_, v []byte) error {
			var rec EventRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if eventMatches(&rec, q) {
				out = append(out, rec)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *BoltEventStore) PruneEvents(before time.Time) (int, error) {
	removed := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		var stale [][]byte
		if err := b.ForEach(func(k, v []byte) error {
			var rec EventRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if rec.At.Before(before) {
				kk := make([]byte, len(k))
				copy(kk, k)
				stale = append(stale, kk)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range stale {
			if err := b.Delete(k); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}
