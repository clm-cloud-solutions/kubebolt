package findings

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

func trivyCVE(title, ns, name string, sev integrations.FindingSeverity) integrations.Finding {
	return integrations.Finding{
		Kind: integrations.FindingCVE, Source: "trivy",
		Severity: sev, Title: title,
		ResourceKind: "Deployment", ResourceNamespace: ns, ResourceName: name,
		DetectedAt: time.Now().UTC(),
	}
}

// runStoreContract exercises the full Store contract — both engines
// must pass it identically (the PG test reuses it under -tags ee).
func runStoreContract(t *testing.T, s Store) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)

	f1 := trivyCVE("CVE-2024-12345: privilege escalation", "production", "payments-api", integrations.SeverityCritical)
	rec := &Record{
		TenantID: "org-a", ClusterID: "cluster-1", Fingerprint: Fingerprint(f1),
		Finding: f1, Status: StatusActive, FirstSeen: now, LastSeen: now,
	}
	if err := s.Upsert(rec); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Re-ingest: same identity, refreshed LastSeen + re-scored severity
	// → still ONE record (fingerprint excludes severity on purpose).
	rec2 := *rec
	rec2.Severity = integrations.SeverityHigh
	rec2.LastSeen = now.Add(time.Minute)
	if err := s.Upsert(&rec2); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	got, ok, err := s.Get("org-a", "cluster-1", rec.Fingerprint)
	if err != nil || !ok {
		t.Fatalf("Get after upsert: ok=%v err=%v", ok, err)
	}
	if got.Severity != integrations.SeverityHigh || !got.LastSeen.After(now.Add(30*time.Second)) {
		t.Fatalf("re-ingest must update in place: %+v", got)
	}

	// Second finding, other tenant — for filter + isolation checks.
	f2 := trivyCVE("CVE-2025-1: rce", "default", "web", integrations.SeverityLow)
	if err := s.Upsert(&Record{
		TenantID: "org-b", ClusterID: "cluster-9", Fingerprint: Fingerprint(f2),
		Finding: f2, Status: StatusActive, FirstSeen: now, LastSeen: now,
	}); err != nil {
		t.Fatalf("Upsert 2: %v", err)
	}

	// List filters: tenant scoping is the load-bearing one.
	if out, _ := s.List(Query{TenantID: "org-a"}); len(out) != 1 || out[0].TenantID != "org-a" {
		t.Fatalf("tenant filter leaked: %+v", out)
	}
	if out, _ := s.List(Query{TenantID: "org-a", Severity: string(integrations.SeverityCritical)}); len(out) != 0 {
		t.Fatalf("severity filter must reflect the re-score, got %+v", out)
	}
	if out, _ := s.List(Query{TenantID: "org-a", Source: "trivy", Status: StatusActive}); len(out) != 1 {
		t.Fatalf("source+status filter: %+v", out)
	}

	// Resolve lifecycle + prune.
	resolvedAt := now.Add(2 * time.Minute)
	if err := s.MarkResolved("org-a", "cluster-1", rec.Fingerprint, resolvedAt); err != nil {
		t.Fatalf("MarkResolved: %v", err)
	}
	got, _, _ = s.Get("org-a", "cluster-1", rec.Fingerprint)
	if got.Status != StatusResolved || got.ResolvedAt == nil {
		t.Fatalf("resolve lifecycle: %+v", got)
	}
	if err := s.MarkResolved("org-a", "cluster-1", "nope", resolvedAt); err != nil {
		t.Fatalf("MarkResolved on missing must be a no-op: %v", err)
	}
	n, err := s.Prune(resolvedAt.Add(time.Second))
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v — want 1 resolved removed", n, err)
	}
	if out, _ := s.List(Query{TenantID: "org-b"}); len(out) != 1 {
		t.Fatalf("prune must never touch active records: %+v", out)
	}
}

func TestFingerprintStability(t *testing.T) {
	f := trivyCVE("CVE-2024-12345", "production", "payments-api", integrations.SeverityCritical)
	a := Fingerprint(f)
	f.Severity = integrations.SeverityLow // re-score → same identity
	if Fingerprint(f) != a {
		t.Fatal("severity must not fork the fingerprint")
	}
	f.ResourceName = "other" // different resource → different identity
	if Fingerprint(f) == a {
		t.Fatal("distinct resources must not collide")
	}
}

func TestBoltStore_Contract(t *testing.T) {
	dir := t.TempDir()
	db, err := bolt.Open(filepath.Join(dir, "test.db"), 0o600, nil)
	if err != nil {
		t.Fatalf("bolt open: %v", err)
	}
	t.Cleanup(func() { db.Close(); os.RemoveAll(dir) })
	bucket := []byte("findings")
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	runStoreContract(t, NewBoltStore(db, bucket))
}
