package findings

import (
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"path/filepath"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

func newTestEventBolt(t *testing.T) *BoltEventStore {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "e.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bucket := []byte("runtime_events")
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return NewBoltEventStore(db, bucket)
}

func falcoEvent(rule string, at time.Time, priority string) integrations.RuntimeEvent {
	return integrations.RuntimeEvent{
		At: at, Priority: priority, RuleName: rule, Source: "falco",
		Namespace: "production", PodName: "postgres-0",
		DetectedBehavior: "shell spawned",
	}
}

// runEventStoreContract — both engines must behave identically.
func runEventStoreContract(t *testing.T, s EventStore) {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Second)

	for i, spec := range []struct {
		tenant, rule, prio string
		at                 time.Time
	}{
		{"org-a", "Terminal shell in container", "Critical", base},
		{"org-a", "Write below etc", "Warning", base.Add(time.Minute)},
		{"org-b", "Terminal shell in container", "Critical", base},
	} {
		if err := s.Append(&EventRecord{
			TenantID: spec.tenant, ClusterID: "cluster-1",
			RuntimeEvent: falcoEvent(spec.rule, spec.at, spec.prio),
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// Newest first + tenant scoping.
	out, err := s.ListEvents(EventQuery{TenantID: "org-a"})
	if err != nil || len(out) != 2 {
		t.Fatalf("org-a list: %v / %d, want 2", err, len(out))
	}
	if out[0].RuleName != "Write below etc" {
		t.Fatalf("newest-first violated: %+v", out[0])
	}
	if out[0].ID == "" || out[0].ReceivedAt.IsZero() {
		t.Fatalf("Append must fill ID/ReceivedAt: %+v", out[0])
	}

	// Priority filter + limit.
	if out, _ := s.ListEvents(EventQuery{TenantID: "org-a", Priority: "Critical"}); len(out) != 1 {
		t.Fatalf("priority filter: %d", len(out))
	}
	if out, _ := s.ListEvents(EventQuery{TenantID: "org-a", Limit: 1}); len(out) != 1 {
		t.Fatalf("limit: %d", len(out))
	}

	// Prune by age spares the newer event.
	n, err := s.PruneEvents(base.Add(30 * time.Second))
	if err != nil || n != 2 { // org-a old + org-b old (prune is cross-org maintenance)
		t.Fatalf("prune = %d, %v — want 2", n, err)
	}
	if out, _ := s.ListEvents(EventQuery{TenantID: "org-a"}); len(out) != 1 {
		t.Fatalf("post-prune org-a: %d, want 1", len(out))
	}
}

func TestBoltEventStore_Contract(t *testing.T) {
	runEventStoreContract(t, newTestEventBolt(t))
}

// A push source retries. falcosidekick reposts byte-for-byte on any non-2xx or
// network error — observed doing it while the API was down — so the same event
// must land once, not once per attempt.
func TestEventIDFromPayload_SameBodyIsIdempotent(t *testing.T) {
	at := time.Date(2026, 8, 6, 18, 16, 52, 534172000, time.UTC)
	body := []byte(`{"rule":"Read sensitive file untrusted","priority":"Warning"}`)

	a := EventIDFromPayload(at, body, 0)
	b := EventIDFromPayload(at, body, 0)
	if a != b {
		t.Fatalf("the same delivery produced two ids:\n %s\n %s\nA retry would duplicate", a, b)
	}
	// The random-suffix id cannot do this, which is why the unique index on
	// (tenant, cluster, id) never fired for Falco.
	if newEventID(at) == newEventID(at) {
		t.Fatal("newEventID is supposed to be non-deterministic")
	}
}

// Distinct events must never collapse. Two syscalls differ in at least their
// nanosecond timestamp, so their payloads differ — the dedup is exact, not a
// window, and cannot swallow a rule firing twice in a tight loop.
func TestEventIDFromPayload_DistinctEventsStayDistinct(t *testing.T) {
	at := time.Date(2026, 8, 6, 18, 16, 52, 534172000, time.UTC)
	base := `{"rule":"Read sensitive file untrusted","time":"2026-08-06T18:16:52.5341720`

	ids := map[string]bool{}
	for _, tail := range []string{`00Z"}`, `01Z"}`, `02Z"}`} {
		ids[EventIDFromPayload(at, []byte(base+tail), 0)] = true
	}
	if len(ids) != 3 {
		t.Errorf("got %d distinct ids for 3 distinct payloads — events would be lost", len(ids))
	}
	// Several events out of ONE body stay separable too.
	if EventIDFromPayload(at, []byte(base+`00Z"}`), 0) == EventIDFromPayload(at, []byte(base+`00Z"}`), 1) {
		t.Error("seq must separate events parsed from the same body")
	}
}

// The Bolt key range is ordered by the id, so the time prefix has to survive.
func TestEventIDFromPayload_KeepsTheChronologicalPrefix(t *testing.T) {
	early := EventIDFromPayload(time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC), []byte(`{"b":1}`), 0)
	late := EventIDFromPayload(time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC), []byte(`{"a":1}`), 0)
	if !(early < late) {
		t.Errorf("ids must sort chronologically:\n early=%s\n late =%s", early, late)
	}
}

// End to end on the store: appending the same record twice leaves one row.
func TestBoltEventStore_RedeliveryIsStoredOnce(t *testing.T) {
	store := newTestEventBolt(t)
	at := time.Now().UTC()
	body := []byte(`{"rule":"Terminal shell in container"}`)

	for i := 0; i < 3; i++ {
		rec := &EventRecord{
			ID: EventIDFromPayload(at, body, 0), TenantID: "org-a", ClusterID: "c1",
		}
		rec.At, rec.RuleName, rec.Source = at, "Terminal shell in container", "falco"
		if err := store.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListEvents(EventQuery{TenantID: "org-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("three deliveries of one event stored %d rows, want 1", len(got))
	}
}
