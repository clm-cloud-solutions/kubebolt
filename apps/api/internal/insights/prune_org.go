package insights

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Per-org pruning. See findings/prune_org.go for the full reasoning; the short
// version is that the global Prune runs its DELETE with no app.current_org set,
// so on the EE/Postgres path RLS matches zero rows and retention silently never
// happens. This form takes the org explicitly, which is also what per-plan
// retention needs — every org gets its own horizon.

// PruneOrg deletes one org's resolved insights older than `before`. Active
// insights never expire regardless of age.
func (s *BoltInsightStore) PruneOrg(orgID string, before time.Time) (int, error) {
	var removed int
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		var toDelete [][]byte
		if err := b.ForEach(func(k, v []byte) error {
			var rec InsightRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.TenantID == orgID && (rec.Status == "resolved" || rec.Status == EpisodeExpired) &&
				rec.ResolvedAt != nil && rec.ResolvedAt.Before(before) {
				keyCopy := make([]byte, len(k))
				copy(keyCopy, k)
				toDelete = append(toDelete, keyCopy)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}

// PruneOrg is the in-memory equivalent, used by tests and the no-persistence path.
func (s *MemoryInsightStore) PruneOrg(orgID string, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int
	for k, rec := range s.records {
		if rec.TenantID == orgID && rec.Status == "resolved" &&
			rec.ResolvedAt != nil && rec.ResolvedAt.Before(before) {
			delete(s.records, k)
			removed++
		}
	}
	return removed, nil
}
