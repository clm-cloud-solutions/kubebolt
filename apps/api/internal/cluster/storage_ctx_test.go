package cluster

import (
	"context"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// newBoltStorage spins a temp BoltDB-backed *Storage with the three buckets
// pre-created (the auth Store creates them in production).
func newBoltStorage(t *testing.T) *Storage {
	t.Helper()
	dir := t.TempDir()
	db, err := bolt.Open(dir+"/c.db", 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfgB, dispB, uidB := []byte("clusters"), []byte("cluster_display"), []byte("cluster_uid")
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{cfgB, dispB, uidB} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create buckets: %v", err)
	}
	return NewStorage(db, cfgB, dispB, uidB)
}

// TestBoltStorage_CtxThreaded confirms the ctx-threaded ClusterStore interface
// (A.2) is satisfied by the Bolt impl and that it ignores the ctx — single-org
// OSS behavior is unchanged. A canceled ctx must not affect Bolt operations,
// proving the ctx is genuinely ignored (not passed into a DB driver).
func TestBoltStorage_CtxThreaded(t *testing.T) {
	var s ClusterStore = newBoltStorage(t) // compile-time: Bolt satisfies the ctx interface

	// A canceled ctx must not break the Bolt store — it ignores the ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := &StoredKubeconfig{
		Context:    "ctx-a",
		Kubeconfig: []byte("apiVersion: v1"),
		UploadedAt: time.Now().UTC().Truncate(time.Second),
		UploadedBy: "admin",
	}
	if err := s.SaveKubeconfig(ctx, cfg); err != nil {
		t.Fatalf("SaveKubeconfig (canceled ctx ignored): %v", err)
	}

	got, err := s.GetKubeconfig(ctx, "ctx-a")
	if err != nil || got == nil || string(got.Kubeconfig) != "apiVersion: v1" {
		t.Fatalf("GetKubeconfig: err=%v got=%+v", err, got)
	}
	if list, err := s.ListKubeconfigs(ctx); err != nil || len(list) != 1 {
		t.Fatalf("ListKubeconfigs: err=%v n=%d", err, len(list))
	}

	// Display names.
	if err := s.SetDisplayName(ctx, "ctx-a", "Prod"); err != nil {
		t.Fatalf("SetDisplayName: %v", err)
	}
	if s.GetDisplayName(ctx, "ctx-a") != "Prod" {
		t.Fatal("display name not stored")
	}
	if names, _ := s.AllDisplayNames(ctx); len(names) != 1 || names["ctx-a"] != "Prod" {
		t.Fatalf("AllDisplayNames: %+v", names)
	}
	if err := s.SetDisplayName(ctx, "ctx-a", ""); err != nil { // empty clears
		t.Fatalf("SetDisplayName(clear): %v", err)
	}
	if s.GetDisplayName(ctx, "ctx-a") != "" {
		t.Fatal("display name not cleared")
	}

	// Cluster UIDs.
	if err := s.SetClusterUID(ctx, "ctx-a", "uid-123"); err != nil {
		t.Fatalf("SetClusterUID: %v", err)
	}
	if s.GetClusterUID(ctx, "ctx-a") != "uid-123" {
		t.Fatal("uid not stored")
	}
	if uids, _ := s.AllClusterUIDs(ctx); len(uids) != 1 || uids["ctx-a"] != "uid-123" {
		t.Fatalf("AllClusterUIDs: %+v", uids)
	}

	if err := s.DeleteDisplayName(ctx, "ctx-a"); err != nil {
		t.Fatalf("DeleteDisplayName: %v", err)
	}
	if err := s.DeleteKubeconfig(ctx, "ctx-a"); err != nil {
		t.Fatalf("DeleteKubeconfig: %v", err)
	}
	if err := s.DeleteKubeconfig(ctx, "ctx-a"); err == nil {
		t.Fatal("DeleteKubeconfig(missing) should error")
	}
}
