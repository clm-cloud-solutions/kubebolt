package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// BoltPolicyStore persists the per-rule policy overrides (#44 step 1) on the
// install's BoltDB — the OSS twin of the Enterprise Postgres store. Sparse:
// only what the operator changed exists; everything else falls back to the
// shipped PolicyCatalog.
//
// Single-tenant: the org argument is accepted for signature parity with the
// EE store and is not part of the key (rule \x00 category). OSS has one
// tenant, so keying on it would only make a lookup miss.
type BoltPolicyStore struct {
	db     *bolt.DB
	bucket []byte
}

func NewBoltPolicyStore(db *bolt.DB, bucket []byte) *BoltPolicyStore {
	return &BoltPolicyStore{db: db, bucket: bucket}
}

func policyKey(ruleID, category string) []byte {
	return []byte(ruleID + "\x00" + category)
}

func (s *BoltPolicyStore) List(ctx context.Context, org string) ([]StoredRulePolicy, error) {
	out := []StoredRulePolicy{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.ForEach(func(k, v []byte) error {
			var p StoredRulePolicy
			if json.Unmarshal(v, &p) == nil {
				out = append(out, p)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].Category < out[j].Category
	})
	return out, nil
}

// Upsert stores one layer of one rule. Knobs the patch leaves nil fall back
// to the existing row's value, so moving the threshold never clears a
// severity set earlier (and vice versa) — the same merge the SQL upsert does
// with COALESCE.
func (s *BoltPolicyStore) Upsert(ctx context.Context, org string, p StoredRulePolicy) error {
	if p.Category == "" {
		p.Category = PolicyCategoryGlobal
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		key := policyKey(p.RuleID, p.Category)
		if raw := b.Get(key); raw != nil {
			var prev StoredRulePolicy
			if json.Unmarshal(raw, &prev) == nil {
				if p.Threshold == nil {
					p.Threshold = prev.Threshold
				}
				if p.Severity == nil {
					p.Severity = prev.Severity
				}
			}
		}
		p.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

func (s *BoltPolicyStore) Delete(ctx context.Context, org, ruleID, category string) error {
	if category == "" {
		category = PolicyCategoryGlobal
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucket)
		if b == nil {
			return fmt.Errorf("bucket %s not found", s.bucket)
		}
		return b.Delete(policyKey(ruleID, category))
	})
}

// PolicyLister is what the snapshot cache needs from a store — the read
// half of the policy contract, so the cache is not tied to one backend.
type PolicyLister interface {
	List(ctx context.Context, org string) ([]StoredRulePolicy, error)
}

var _ PolicyLister = (*BoltPolicyStore)(nil)

// ─── snapshot cache ────────────────────────────────────────────────────────

// PolicyCache turns the sparse store into effective PolicySnapshots for the
// engine's policy source. The snapshot is per (org, ENV): the cluster's
// environment category layers on top of the global overrides, knob by knob.
// Cached per (org, env) on first use, dropped by Invalidate(org) — the
// PUT/DELETE handlers invalidate, so a change reaches the next evaluation
// tick (≤30s) without reading the store every cycle.
type PolicyCache struct {
	store PolicyLister
	// envFor maps (tenant, cluster) → environment category ("" = unclassified
	// → global layer only). nil = no resolver (global only), which is what
	// OSS wires: environments are Enterprise billing metadata.
	envFor func(tenant, cluster string) string
	mu     sync.Mutex
	byKey  map[string]PolicySnapshot // org + "\x00" + env
}

func NewPolicyCache(store PolicyLister, envFor func(tenant, cluster string) string) *PolicyCache {
	return &PolicyCache{store: store, envFor: envFor, byKey: map[string]PolicySnapshot{}}
}

// SnapshotFor is the policy-source function: resolves the cluster's env and
// folds `global` + that category into one effective snapshot (category wins
// per knob). Load errors degrade to shipped defaults — a broken policy read
// must never stop detection.
func (c *PolicyCache) SnapshotFor(tenant, cluster string) PolicySnapshot {
	env := ""
	if c.envFor != nil {
		env = c.envFor(tenant, cluster)
	}
	key := tenant + "\x00" + env

	c.mu.Lock()
	if snap, ok := c.byKey[key]; ok {
		c.mu.Unlock()
		return snap
	}
	c.mu.Unlock()

	rows, err := c.store.List(context.Background(), tenant)
	snap := PolicySnapshot{Thresholds: map[string]float64{}, Severities: map[string]string{}}
	if err == nil {
		// Two passes so ROW ORDER never decides precedence: global first,
		// then the cluster's category overrides on top, knob by knob.
		for _, r := range rows {
			if r.Category == PolicyCategoryGlobal {
				foldPolicyRow(&snap, r)
			}
		}
		if env != "" {
			for _, r := range rows {
				if r.Category == env {
					foldPolicyRow(&snap, r)
				}
			}
		}
	}
	c.mu.Lock()
	c.byKey[key] = snap
	c.mu.Unlock()
	return snap
}

func foldPolicyRow(snap *PolicySnapshot, r StoredRulePolicy) {
	if r.Threshold != nil {
		snap.Thresholds[r.RuleID] = *r.Threshold
	}
	if r.Severity != nil {
		snap.Severities[r.RuleID] = *r.Severity
	}
}

// Invalidate drops EVERY cached env variant of the org.
func (c *PolicyCache) Invalidate(org string) {
	prefix := org + "\x00"
	c.mu.Lock()
	for k := range c.byKey {
		if strings.HasPrefix(k, prefix) {
			delete(c.byKey, k)
		}
	}
	c.mu.Unlock()
}
