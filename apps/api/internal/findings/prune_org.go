package findings

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Per-org pruning — the only form that actually deletes under RLS.
//
// The global Prune/PruneEvents run their DELETE with no org context. On the
// Bolt engine that is fine (no RLS, one org). On Postgres the runtime connects
// as a NOBYPASSRLS role, so `tenant_id = current_setting('app.current_org',
// true)` evaluates against NULL, matches zero rows, and the delete removes
// NOTHING while returning (0, nil). Retention silently never happened — the
// failure mode is indistinguishable from "nothing was old enough".
//
// PruneOrg takes the org explicitly so the Postgres impl can open the
// transaction with the GUC set. It also gives per-plan retention somewhere to
// live: each org gets its OWN horizon, which a single global DELETE could not
// express even if RLS allowed it.

// PruneOrg deletes one org's resolved findings older than `before`.
func (s *BoltStore) PruneOrg(orgID string, before time.Time) (int, error) {
	return s.pruneMatching(func(rec *Record) bool {
		return rec.TenantID == orgID &&
			rec.Status == StatusResolved &&
			rec.ResolvedAt != nil &&
			rec.ResolvedAt.Before(before)
	})
}

func (s *BoltStore) pruneMatching(match func(*Record) bool) (int, error) {
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

// PruneEventsOrg deletes one org's runtime events older than `before`. Unlike
// findings there is no resolved/active lifecycle — a runtime event is a fact
// that happened, so age is the only criterion.
func (s *BoltEventStore) PruneEventsOrg(orgID string, before time.Time) (int, error) {
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
			if rec.TenantID == orgID && rec.At.Before(before) {
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
