package findings

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// Cluster-scoped deletion — what happens to a cluster's security data when the
// cluster LEAVES the tenant (in-vivo 2026-09-15).
//
// The lifecycle had a hole: the sweeper only visits LIVE connectors, so a
// cluster removed from the org never gets its actives reconciled to resolved;
// PruneOrg only deletes resolved; and the admin read path applies no cluster
// narrowing. A deleted cluster's findings were therefore immortal — 1,047
// active rows from a demo cluster, frozen at their last sweep, shown to every
// admin forever. The product rule is the operator's: registered-but-
// disconnected keeps its history (the Security page must answer for dead
// clusters), REMOVED from the tenant shows nothing.
//
// Two verbs implement the rule:
//   - DeleteCluster / DeleteEventsCluster — the cascade behind the explicit
//     operator delete (the type-the-name modal). Runs per identifier, because
//     rows may be keyed by the cluster UID or by the context-name fallback.
//   - ClusterIDs / EventClusterIDs — the distinct-cluster listing the
//     retention pass uses to sweep ORPHANS: rows whose cluster is registered
//     nowhere (removed before the cascade existed, or misattributed by the
//     pre-2.0.3 silent drop, finding #32).

// DeleteCluster removes every finding of one (tenant, cluster) — active
// included: absence from the tenant is not resolution, it is departure.
// Returns the count removed.
func (s *BoltStore) DeleteCluster(tenantID, clusterID string) (int, error) {
	return s.pruneMatching(func(rec *Record) bool {
		return rec.TenantID == tenantID && rec.ClusterID == clusterID
	})
}

// ClusterIDs returns the distinct cluster identifiers holding findings for
// the tenant. Decoded from the records, not parsed from keys — context names
// can legally contain the key separator (EKS ARNs carry '/').
func (s *BoltStore) ClusterIDs(tenantID string) ([]string, error) {
	seen := map[string]struct{}{}
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
			if rec.TenantID == tenantID {
				seen[rec.ClusterID] = struct{}{}
			}
			return nil
		})
	})
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, err
}

// DeleteEventsCluster removes every runtime event of one (tenant, cluster).
func (s *BoltEventStore) DeleteEventsCluster(tenantID, clusterID string) (int, error) {
	return s.deleteEventsMatching(func(rec *EventRecord) bool {
		return rec.TenantID == tenantID && rec.ClusterID == clusterID
	})
}

// EventClusterIDs returns the distinct cluster identifiers holding runtime
// events for the tenant — its OWN listing, not the findings one: a Falco-only
// cluster can have events and zero findings.
func (s *BoltEventStore) EventClusterIDs(tenantID string) ([]string, error) {
	seen := map[string]struct{}{}
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
			if rec.TenantID == tenantID {
				seen[rec.ClusterID] = struct{}{}
			}
			return nil
		})
	})
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, err
}

func (s *BoltEventStore) deleteEventsMatching(match func(*EventRecord) bool) (int, error) {
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
			if match(&rec) {
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

// compile-time: both engines satisfy the grown contracts.
var (
	_ interface {
		DeleteCluster(string, string) (int, error)
		ClusterIDs(string) ([]string, error)
	} = (*BoltStore)(nil)
	_ interface {
		DeleteEventsCluster(string, string) (int, error)
		EventClusterIDs(string) ([]string, error)
	} = (*BoltEventStore)(nil)
)
