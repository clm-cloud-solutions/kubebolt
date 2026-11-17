package audit

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// A mutation's params are kept for as long as the trail (2160h by default) and
// printed to the process log on the way. For an env change the trail records
// which variables changed and how they are sourced, never a literal value.

// DropEnvValues removes, anywhere in v, the value of every entry of an "env"
// list: {name, kind, value, valueFrom} keeps its name, its kind and its
// valueFrom (a reference, not the secret). It reports whether it removed any.
func DropEnvValues(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if k == "env" {
				if items, ok := child.([]any); ok {
					for _, it := range items {
						if m, ok := it.(map[string]any); ok {
							if _, has := m["value"]; has {
								delete(m, "value")
								changed = true
							}
						}
					}
				}
			}
			changed = DropEnvValues(child) || changed
		}
	case []any:
		for _, child := range t {
			changed = DropEnvValues(child) || changed
		}
	}
	return changed
}

// DropEnvValues rewrites the records written before params were scrubbed at
// write time, so a value already in the trail does not stay there for the rest
// of its retention. Idempotent: a record with nothing to drop is not touched.
func (s *BoltStore) DropEnvValues() (int, error) {
	var rewritten int
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		type rewrite struct{ k, v []byte }
		var pending []rewrite
		err := b.ForEach(func(k, v []byte) error {
			var rec Record
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.Params == nil || !DropEnvValues(rec.Params) {
				return nil
			}
			payload, err := json.Marshal(&rec)
			if err != nil {
				return err
			}
			pending = append(pending, rewrite{k: append([]byte(nil), k...), v: payload})
			return nil
		})
		if err != nil {
			return err
		}
		for _, p := range pending {
			if err := b.Put(p.k, p.v); err != nil {
				return err
			}
		}
		rewritten = len(pending)
		return nil
	})
	return rewritten, err
}
