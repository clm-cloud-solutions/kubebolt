package copilot

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ToolStats captures per-tool usage for one session.
type ToolStats struct {
	Calls      int   `json:"calls"`
	Bytes      int   `json:"bytes"`
	Errors     int   `json:"errors"`
	DurationMs int64 `json:"durationMs"`
}

// SessionRecord is one persisted copilot chat session. Written once per
// chat call on finish(). Kept small (~1-2KB) so we can hold thousands
// without bloating the db file.
type SessionRecord struct {
	ID         string               `json:"id"`
	Timestamp  time.Time            `json:"timestamp"`
	UserID     string               `json:"userId"`
	Cluster    string               `json:"cluster"`
	Provider   string               `json:"provider"`
	Model      string               `json:"model"`
	Trigger    string               `json:"trigger"`
	Reason     string               `json:"reason"` // "done" | "error"
	Rounds     int                  `json:"rounds"`
	Usage      Usage                `json:"usage"`
	ToolCalls  int                  `json:"toolCalls"`
	ToolBytes  int                  `json:"toolResultBytes"`
	DurationMs int64                `json:"durationMs"`
	Fallback   bool                 `json:"fallback"`
	Tools      map[string]ToolStats `json:"tools,omitempty"`
	// Compaction events that fired within this session (auto-compact
	// during the tool loop). Manual compacts are their own endpoint calls
	// and are not attached to a session.
	Compacts []CompactEvent `json:"compacts,omitempty"`
}

// CompactEvent captures an inline auto-compact.
type CompactEvent struct {
	TurnsFolded  int    `json:"turnsFolded"`
	TokensBefore int    `json:"tokensBefore"`
	TokensAfter  int    `json:"tokensAfter"`
	Model        string `json:"model"`
}

// UsageStore persists SessionRecords in BoltDB. Sessions are keyed by a
// 16-byte big-endian uint64 timestamp prefix + 8 random bytes, giving
// natural chronological ordering on iteration and avoiding id collisions
// even at very high session rates.
type UsageStore struct {
	db     *bolt.DB
	bucket []byte
	// Retention caps — after every write we prune records older than the
	// retention window AND keep at most maxRecords newest entries. Both
	// bounded to avoid db bloat and runaway memory on the aggregate side.
	retention  time.Duration
	maxRecords int

	mu   sync.Mutex
	rand func() []byte // injectable for tests
}

// NewUsageStore wires a UsageStore against the given BoltDB handle.
// The bucket must already exist (created by auth.NewStore).
func NewUsageStore(db *bolt.DB, bucket []byte) *UsageStore {
	return &UsageStore{
		db:         db,
		bucket:     bucket,
		retention:  30 * 24 * time.Hour,
		maxRecords: 5000,
		rand:       randomSuffix,
	}
}

func randomSuffix() []byte {
	var b [8]byte
	// Use time nanoseconds for collision resistance within ms; not
	// cryptographically random but good enough for ordering keys.
	n := time.Now().UnixNano()
	binary.BigEndian.PutUint64(b[:], uint64(n))
	return b[:]
}

func encodeKey(t time.Time, suffix []byte) []byte {
	key := make([]byte, 16)
	binary.BigEndian.PutUint64(key[:8], uint64(t.UnixMilli()))
	copy(key[8:], suffix)
	return key
}

// Record persists a session. Prunes on every write — cheap enough at our
// rate (a few calls/sec peak).
func (s *UsageStore) Record(rec *SessionRecord) error {
	if rec == nil {
		return nil
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	if rec.ID == "" {
		rec.ID = fmt.Sprintf("%d", rec.Timestamp.UnixNano())
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		if err := b.Put(encodeKey(rec.Timestamp, s.rand()), data); err != nil {
			return err
		}
		return s.pruneLocked(b)
	})
}

func (s *UsageStore) pruneLocked(b *bolt.Bucket) error {
	cutoff := time.Now().Add(-s.retention).UnixMilli()
	cutoffKey := make([]byte, 8)
	binary.BigEndian.PutUint64(cutoffKey, uint64(cutoff))

	// Drop by age: iterate from oldest, delete anything with timestamp < cutoff.
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if len(k) < 8 || string(k[:8]) >= string(cutoffKey) {
			break
		}
		if err := c.Delete(); err != nil {
			return err
		}
	}

	// Cap total: if still over max, drop oldest.
	stats := b.Stats()
	if stats.KeyN <= s.maxRecords {
		return nil
	}
	over := stats.KeyN - s.maxRecords
	c = b.Cursor()
	for k, _ := c.First(); k != nil && over > 0; k, _ = c.Next() {
		if err := c.Delete(); err != nil {
			return err
		}
		over--
	}
	return nil
}

// Query returns sessions within [from, to). Limit caps the number of
// records returned; 0 means no cap.
func (s *UsageStore) Query(from, to time.Time, limit int) ([]SessionRecord, error) {
	fromKey := make([]byte, 8)
	toKey := make([]byte, 8)
	binary.BigEndian.PutUint64(fromKey, uint64(from.UnixMilli()))
	binary.BigEndian.PutUint64(toKey, uint64(to.UnixMilli()))

	var out []SessionRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Seek(fromKey); k != nil; k, v = c.Next() {
			if len(k) < 8 {
				continue
			}
			if string(k[:8]) >= string(toKey) {
				break
			}
			var rec SessionRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			out = append(out, rec)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return nil
	})
	// Sort newest-first for UI consumption.
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out, err
}

// Count returns the total number of records stored.
func (s *UsageStore) Count() (int, error) {
	var n int
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n, err
}
