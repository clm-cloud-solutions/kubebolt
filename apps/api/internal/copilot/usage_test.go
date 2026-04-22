package copilot

import (
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func openTestDB(t *testing.T) (*bolt.DB, []byte) {
	t.Helper()
	dir := t.TempDir()
	db, err := bolt.Open(filepath.Join(dir, "test.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("bolt open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bucket := []byte("sessions")
	err = db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(bucket)
		return e
	})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return db, bucket
}

func TestUsageStore_RecordAndQuery(t *testing.T) {
	db, bucket := openTestDB(t)
	store := NewUsageStore(db, bucket)

	now := time.Now()
	records := []*SessionRecord{
		{Timestamp: now.Add(-2 * time.Hour), UserID: "u1", Model: "sonnet"},
		{Timestamp: now.Add(-1 * time.Hour), UserID: "u2", Model: "haiku"},
		{Timestamp: now, UserID: "u3", Model: "gpt-4o"},
	}
	for _, r := range records {
		if err := store.Record(r); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	if n, _ := store.Count(); n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}

	// Query last hour → only the 2 most recent
	got, err := store.Query(now.Add(-time.Hour-time.Minute), now.Add(time.Minute), 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("query returned %d records, want 2", len(got))
	}
	// Newest-first ordering
	if got[0].UserID != "u3" || got[1].UserID != "u2" {
		t.Errorf("ordering wrong: got %q, %q", got[0].UserID, got[1].UserID)
	}
}

func TestUsageStore_QueryLimit(t *testing.T) {
	db, bucket := openTestDB(t)
	store := NewUsageStore(db, bucket)

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 10; i++ {
		_ = store.Record(&SessionRecord{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			UserID:    "u",
		})
	}
	got, _ := store.Query(base.Add(-time.Minute), time.Now(), 3)
	if len(got) != 3 {
		t.Errorf("limit=3 should cap at 3, got %d", len(got))
	}
}

func TestUsageStore_Retention(t *testing.T) {
	db, bucket := openTestDB(t)
	store := NewUsageStore(db, bucket)
	store.retention = time.Hour

	// Old record — beyond retention
	old := &SessionRecord{Timestamp: time.Now().Add(-2 * time.Hour), UserID: "old"}
	fresh := &SessionRecord{Timestamp: time.Now(), UserID: "fresh"}

	if err := store.Record(old); err != nil {
		t.Fatalf("record old: %v", err)
	}
	// After the next Record() call, pruning runs and drops the old one.
	if err := store.Record(fresh); err != nil {
		t.Fatalf("record fresh: %v", err)
	}
	if n, _ := store.Count(); n != 1 {
		t.Errorf("after retention prune, Count = %d, want 1", n)
	}
}

func TestUsageStore_MaxRecordsCap(t *testing.T) {
	db, bucket := openTestDB(t)
	store := NewUsageStore(db, bucket)
	store.maxRecords = 3
	store.retention = 24 * time.Hour // keep retention permissive

	for i := 0; i < 5; i++ {
		_ = store.Record(&SessionRecord{
			Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond),
			UserID:    "u",
		})
	}
	if n, _ := store.Count(); n > 3 {
		t.Errorf("cap=3 should keep at most 3 records, got %d", n)
	}
}

func TestUsageStore_RecordPreservesFields(t *testing.T) {
	db, bucket := openTestDB(t)
	store := NewUsageStore(db, bucket)

	orig := &SessionRecord{
		Timestamp:  time.Now(),
		UserID:     "alice",
		Cluster:    "prod",
		Provider:   "anthropic",
		Model:      "claude-sonnet-4-6",
		Trigger:    "insight",
		Reason:     "done",
		Rounds:     3,
		Usage:      Usage{InputTokens: 5000, OutputTokens: 1000, CacheReadTokens: 10000},
		ToolCalls:  4,
		ToolBytes:  12000,
		DurationMs: 5432,
		Fallback:   true,
		Tools: map[string]ToolStats{
			"get_pods": {Calls: 2, Bytes: 1000, Errors: 0, DurationMs: 300},
		},
		Compacts: []CompactEvent{
			{TurnsFolded: 3, TokensBefore: 8000, TokensAfter: 2000, Model: "haiku"},
		},
	}
	if err := store.Record(orig); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := store.Query(orig.Timestamp.Add(-time.Second), orig.Timestamp.Add(time.Second), 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	r := got[0]
	if r.UserID != "alice" || r.Cluster != "prod" || r.Rounds != 3 ||
		r.Usage.CacheReadTokens != 10000 || r.Fallback != true ||
		len(r.Tools) != 1 || r.Tools["get_pods"].Calls != 2 ||
		len(r.Compacts) != 1 || r.Compacts[0].TurnsFolded != 3 {
		t.Errorf("field roundtrip mismatch: %+v", r)
	}
}
