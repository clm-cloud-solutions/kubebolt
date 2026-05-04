package channel

import (
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// TestRegistry_PersistOnRegister verifies the AgentRegistry calls
// Upsert on the wired AgentStore at Register time, including the
// HelloMeta context (capabilities, display name, version).
func TestRegistry_PersistOnRegister(t *testing.T) {
	store := NewMemoryAgentStore()
	r := NewAgentRegistry()
	r.SetStore(store)

	r.SetHelloMeta("c1", "agent-1", HelloMeta{
		Capabilities: []string{"metrics", "kube-proxy"},
		DisplayName:  "kind-test (via agent)",
		AgentVersion: "0.2.0",
	})
	a := NewAgent("c1", "agent-1", "node-a", &auth.AgentIdentity{TenantID: "t1", Mode: auth.ModeIngestToken}, nil)
	r.Register(a)

	recs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	rec := recs[0]
	if rec.ClusterID != "c1" || rec.AgentID != "agent-1" {
		t.Errorf("wrong key: %+v", rec)
	}
	if rec.NodeName != "node-a" {
		t.Errorf("NodeName = %q, want node-a", rec.NodeName)
	}
	if rec.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", rec.TenantID)
	}
	if rec.AuthMode != string(auth.ModeIngestToken) {
		t.Errorf("AuthMode = %q, want ingest-token", rec.AuthMode)
	}
	if rec.DisplayName != "kind-test (via agent)" {
		t.Errorf("DisplayName = %q", rec.DisplayName)
	}
	if rec.AgentVersion != "0.2.0" {
		t.Errorf("AgentVersion = %q", rec.AgentVersion)
	}
	if !rec.HasKubeProxy() {
		t.Errorf("HasKubeProxy() = false; capabilities = %v", rec.Capabilities)
	}
	if !rec.Connected() {
		t.Errorf("expected Connected()=true after Register")
	}
}

// TestRegistry_MarkDisconnectedOnUnregister verifies Unregister bumps
// DisconnectedAt on the persisted record (not delete — we keep records
// for forensics + restart restore).
func TestRegistry_MarkDisconnectedOnUnregister(t *testing.T) {
	store := NewMemoryAgentStore()
	r := NewAgentRegistry()
	r.SetStore(store)

	a := NewAgent("c1", "agent-1", "node-a", nil, nil)
	r.Register(a)
	r.Unregister(a)

	recs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected record retained after Unregister, got %d", len(recs))
	}
	if recs[0].Connected() {
		t.Errorf("expected Connected()=false after Unregister")
	}
	if recs[0].DisconnectedAt.IsZero() {
		t.Errorf("DisconnectedAt should be non-zero")
	}
}

// TestRegistry_StalUnregisterDoesNotOverwriteCurrent — the
// pointer-equality check in Unregister already protects the in-memory
// map; verify it ALSO skips the store write so a stale defer doesn't
// mark a freshly-reconnected record as disconnected.
func TestRegistry_StaleUnregisterDoesNotMarkDisconnected(t *testing.T) {
	store := NewMemoryAgentStore()
	r := NewAgentRegistry()
	r.SetStore(store)

	a1 := NewAgent("c1", "agent-1", "node-a", nil, nil)
	r.Register(a1)
	a2 := NewAgent("c1", "agent-1", "node-a", nil, nil) // same key, new pointer
	r.Register(a2)

	// Stale Unregister of a1 (e.g. its handler exiting after a2's
	// Register evicted it). Should NOT touch a2's record.
	r.Unregister(a1)

	recs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if !recs[0].Connected() {
		t.Errorf("a2 marked disconnected by stale a1 Unregister")
	}
}

// TestStore_Prune verifies disconnected records older than `before`
// are removed and connected ones are preserved.
func TestStore_Prune(t *testing.T) {
	store := NewMemoryAgentStore()

	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	// Connected — must survive any prune.
	_ = store.Upsert(&AgentRecord{ClusterID: "c1", AgentID: "live", FirstSeen: old, LastSeen: now})
	// Disconnected long ago — should prune.
	_ = store.Upsert(&AgentRecord{ClusterID: "c1", AgentID: "ancient", FirstSeen: old, LastSeen: old, DisconnectedAt: old})
	// Disconnected recently — should NOT prune (within horizon).
	_ = store.Upsert(&AgentRecord{ClusterID: "c1", AgentID: "fresh", FirstSeen: recent, LastSeen: recent, DisconnectedAt: recent})

	removed, err := store.Prune(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only 'ancient')", removed)
	}
	recs, _ := store.List()
	ids := make(map[string]bool)
	for _, r := range recs {
		ids[r.AgentID] = true
	}
	if !ids["live"] || !ids["fresh"] || ids["ancient"] {
		t.Errorf("after prune: %v", ids)
	}
}

// TestBoltAgentStore_RoundTrip exercises the BoltDB-backed store
// against a temporary file: write some records, reopen the file,
// list them back. This is what protects the boot-restore path
// (cluster.Manager.AddAgentProxyCluster after restart) from silent
// drift in the schema or bucket name.
func TestBoltAgentStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	bucket := []byte("agents")

	// First open: write 2 records.
	{
		db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("bolt.Open: %v", err)
		}
		err = db.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists(bucket)
			return err
		})
		if err != nil {
			t.Fatalf("CreateBucket: %v", err)
		}
		store := NewBoltAgentStore(db, bucket)
		now := time.Now().UTC()
		_ = store.Upsert(&AgentRecord{
			ClusterID:    "cluster-1",
			AgentID:      "agent-a",
			NodeName:     "n1",
			TenantID:     "t1",
			Capabilities: []string{"metrics", "kube-proxy"},
			DisplayName:  "kind-1 (via agent)",
			FirstSeen:    now,
			LastSeen:     now,
		})
		_ = store.Upsert(&AgentRecord{
			ClusterID:    "cluster-2",
			AgentID:      "agent-b",
			NodeName:     "n2",
			Capabilities: []string{"metrics"},
			FirstSeen:    now,
			LastSeen:     now,
		})
		_ = db.Close()
	}

	// Second open: read back, verify.
	{
		db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("bolt.Open #2: %v", err)
		}
		defer db.Close()
		store := NewBoltAgentStore(db, bucket)
		recs, err := store.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(recs) != 2 {
			t.Fatalf("expected 2 records after reopen, got %d", len(recs))
		}
		// Sort order is by (cluster_id, agent_id) — cluster-1 first.
		if recs[0].ClusterID != "cluster-1" || recs[1].ClusterID != "cluster-2" {
			t.Errorf("sort order: %+v", recs)
		}
		if !recs[0].HasKubeProxy() {
			t.Errorf("cluster-1 should have kube-proxy capability")
		}
		if recs[1].HasKubeProxy() {
			t.Errorf("cluster-2 has metrics-only — HasKubeProxy should be false")
		}
		// Mark cluster-1 disconnected, verify it persists across the
		// next reopen (test below mimics what a deploy would do).
		if err := store.MarkDisconnected("cluster-1", "agent-a", time.Now().UTC()); err != nil {
			t.Fatalf("MarkDisconnected: %v", err)
		}
		recs, _ = store.List()
		for _, r := range recs {
			if r.AgentID == "agent-a" && r.Connected() {
				t.Errorf("agent-a still Connected after MarkDisconnected")
			}
		}
	}
}

// TestRegistry_SetStoreNil — disabling persistence after the fact
// (e.g. if main.go decides to run without auth). Subsequent
// Register/Unregister must not panic and the previously-written
// records stay where they were.
func TestRegistry_SetStoreNil(t *testing.T) {
	store := NewMemoryAgentStore()
	r := NewAgentRegistry()
	r.SetStore(store)

	a := NewAgent("c1", "agent-1", "node-a", nil, nil)
	r.Register(a)

	r.SetStore(nil)
	a2 := NewAgent("c2", "agent-2", "node-b", nil, nil)
	r.Register(a2)
	r.Unregister(a)

	recs, _ := store.List()
	// Only the first record should be in the store; the second
	// Register happened with store=nil so it wasn't persisted. The
	// Unregister of `a` ALSO happened with store=nil so it didn't
	// flip a's DisconnectedAt.
	if len(recs) != 1 {
		t.Errorf("expected 1 record, got %d", len(recs))
	}
	if !recs[0].Connected() {
		t.Errorf("a's record marked disconnected — should be untouched after SetStore(nil)")
	}
}
