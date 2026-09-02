// Package findings persists normalized security/compliance findings —
// the rows behind the Security & Compliance dashboard and Kobi's
// query_findings read-tool (E2 SEC-A; the store deferred from E1's
// integrations framework by decision D2, landing with its first
// consumer).
//
// The design mirrors the insights store one-for-one: same
// (tenant, cluster, fingerprint) identity, same Bolt-OSS /
// Postgres-EE split, same active→resolved lifecycle. Providers
// (Trivy, kube-bench, Kyverno, …) produce integrations.Finding
// payloads; the ingest layer wraps them in Records and stamps the
// tenant — a provider can never spoof another org's findings.
package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

const (
	StatusActive   = "active"
	StatusResolved = "resolved"
)

// Record is one persisted finding: the normalized payload plus
// identity + lifecycle. JSON-encoded whole into Bolt; in Postgres the
// filterable columns are broken out and the full record rides a JSONB
// payload column.
type Record struct {
	// Identity — mirrors insights: tenant/cluster prefix means SaaS
	// multi-tenant needs no rekey and range scans stay cheap.
	TenantID    string `json:"tenantId"`
	ClusterID   string `json:"clusterId"`
	Fingerprint string `json:"fingerprint"`

	integrations.Finding // the normalized payload (source, kind, severity, resource, …)

	Status     string     `json:"status"` // active | resolved
	FirstSeen  time.Time  `json:"firstSeen"`
	LastSeen   time.Time  `json:"lastSeen"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

// Fingerprint derives the stable identity of a finding: same source
// reporting the same issue on the same resource → same fingerprint,
// so re-ingests refresh LastSeen instead of duplicating rows.
// Severity is deliberately NOT part of the identity — a CVE being
// re-scored must update the existing record, not fork it.
func Fingerprint(f integrations.Finding) string {
	h := sha256.Sum256([]byte(
		f.Source + "|" + string(f.Kind) + "|" + f.Title + "|" +
			f.ResourceKind + "|" + f.ResourceNamespace + "|" + f.ResourceName + "|" + f.CISControl,
	))
	return hex.EncodeToString(h[:16])
}

// Query filters List. Empty fields match everything; Limit<=0 means
// no cap.
type Query struct {
	TenantID  string
	ClusterID string
	Source    string
	Kind      string
	Severity  string
	Status    string
	Limit     int
}

// Store is the persistence contract — extracted like InsightStore so
// tests use Bolt in a temp file and EE drops in Postgres with zero
// rekey. Implementations must be safe for concurrent use (ingest
// writes while the API lists).
type Store interface {
	// Upsert writes the full record, replacing any prior copy with
	// the same (tenantID, clusterID, fingerprint). First writer sets
	// FirstSeen; the caller refreshes LastSeen.
	Upsert(rec *Record) error
	// MarkResolved sets Status=resolved + ResolvedAt. No-op when the
	// record doesn't exist.
	MarkResolved(tenantID, clusterID, fingerprint string, at time.Time) error
	// Get returns one record by identity.
	Get(tenantID, clusterID, fingerprint string) (*Record, bool, error)
	// List returns records matching the query, newest LastSeen first.
	List(q Query) ([]Record, error)
	// Prune deletes resolved records whose ResolvedAt is older than
	// `before`. Active records never expire. Returns the count removed.
	//
	// DO NOT use this on the EE/Postgres path: it carries no org context, so
	// RLS matches no rows and it silently deletes nothing. Use PruneOrg.
	Prune(before time.Time) (int, error)
	// PruneOrg is Prune scoped to ONE org — the only form that deletes under
	// RLS, and the one per-plan retention needs (each org has its own horizon).
	PruneOrg(orgID string, before time.Time) (int, error)
}

func recordKey(tenantID, clusterID, fingerprint string) []byte {
	return []byte(tenantID + "/" + clusterID + "/" + fingerprint)
}

func matchesQuery(rec *Record, q Query) bool {
	if q.TenantID != "" && rec.TenantID != q.TenantID {
		return false
	}
	if q.ClusterID != "" && rec.ClusterID != q.ClusterID {
		return false
	}
	if q.Source != "" && rec.Source != q.Source {
		return false
	}
	if q.Kind != "" && string(rec.Kind) != q.Kind {
		return false
	}
	if q.Severity != "" && string(rec.Severity) != q.Severity {
		return false
	}
	if q.Status != "" && rec.Status != q.Status {
		return false
	}
	return true
}

func sortNewestFirst(out []Record) {
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
}

// ─── BoltDB implementation (OSS) ─────────────────────────────────

// BoltStore holds JSON-encoded Records in one bucket keyed by
// `<tenantID>/<clusterID>/<fingerprint>` — the insights layout.
type BoltStore struct {
	db     *bolt.DB
	bucket []byte
}

// NewBoltStore wires the store to a BoltDB handle + bucket name. The
// bucket must already exist (created at boot alongside the others).
func NewBoltStore(db *bolt.DB, bucket []byte) *BoltStore {
	return &BoltStore{db: db, bucket: bucket}
}

func (s *BoltStore) Upsert(rec *Record) error {
	if rec == nil {
		return fmt.Errorf("nil finding Record")
	}
	if rec.Fingerprint == "" {
		return fmt.Errorf("finding Record missing fingerprint")
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal finding Record: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.Put(recordKey(rec.TenantID, rec.ClusterID, rec.Fingerprint), payload)
	})
}

func (s *BoltStore) MarkResolved(tenantID, clusterID, fingerprint string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		key := recordKey(tenantID, clusterID, fingerprint)
		raw := b.Get(key)
		if raw == nil {
			return nil // no-op when record doesn't exist
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unmarshal finding Record: %w", err)
		}
		rec.Status = StatusResolved
		rec.ResolvedAt = &at
		payload, err := json.Marshal(&rec)
		if err != nil {
			return err
		}
		return b.Put(key, payload)
	})
}

func (s *BoltStore) Get(tenantID, clusterID, fingerprint string) (*Record, bool, error) {
	var rec *Record
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		raw := b.Get(recordKey(tenantID, clusterID, fingerprint))
		if raw == nil {
			return nil
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		rec = &r
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return rec, rec != nil, nil
}

func (s *BoltStore) List(q Query) ([]Record, error) {
	out := []Record{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.ForEach(func(_, v []byte) error {
			var rec Record
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
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
	sortNewestFirst(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *BoltStore) Prune(before time.Time) (int, error) {
	removed := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		var stale [][]byte
		if err := b.ForEach(func(k, v []byte) error {
			var rec Record
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if rec.Status == StatusResolved && rec.ResolvedAt != nil && rec.ResolvedAt.Before(before) {
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
