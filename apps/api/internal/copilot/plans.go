package copilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// Kobi Plans storage (Kobi Plans v0, plans-02). A plan has a current version
// and every earlier one, kept unchanged: revise_plan never rewrites history.
// The tables and buckets are kobi_plans_* because «plan» is already the
// billing plan (P7).

var (
	ErrPlanNotFound        = errors.New("plan not found")
	ErrPlanExists          = errors.New("a plan with this id already exists")
	ErrPlanVersionConflict = errors.New("plan version conflict")
	// ErrPlanContentChanged: Update was asked to change what the plan proposes.
	// That is a new version, and only Revise writes one.
	ErrPlanContentChanged = errors.New("plan content changed: revise it instead")
)

// PlanFilter narrows List. Zero values mean «any».
type PlanFilter struct {
	ClusterID string
	Statuses  []kobiplan.Status
	// Limit caps the rows; 0 = no cap.
	Limit int
}

func (f PlanFilter) match(p *kobiplan.Plan) bool {
	if f.ClusterID != "" && p.ClusterID != f.ClusterID {
		return false
	}
	if len(f.Statuses) == 0 {
		return true
	}
	for _, s := range f.Statuses {
		if p.Status == s {
			return true
		}
	}
	return false
}

// PlanStore keeps Kobi Plans. Every method is scoped to one org (the plan's
// OrgID); the Postgres store runs each statement inside the org's RLS context.
// Every write validates the plan first: the store never holds one Validate
// refuses.
type PlanStore interface {
	// Create stores a new plan, at version 1. ErrPlanExists when the id is taken.
	Create(p kobiplan.Plan) error
	// Update rewrites the current version in place: its status, exports,
	// baseline, measured impact and notes. It refuses a different version
	// (ErrPlanVersionConflict) and a different content (ErrPlanContentChanged):
	// what the plan proposes only changes through Revise.
	Update(p kobiplan.Plan) error
	// Revise stores a new version: the current one moves to the history as it
	// is, and p, at the next version, becomes current. ErrPlanVersionConflict
	// when p.Version is not the current one plus one.
	Revise(p kobiplan.Plan) error
	// Get returns the current version. ErrPlanNotFound when the org has no such plan.
	Get(tenantID, id string) (*kobiplan.Plan, error)
	// GetVersion returns any version, the current one included.
	GetVersion(tenantID, id string, version int) (*kobiplan.Plan, error)
	// List returns the org's plans, the last updated first.
	List(tenantID string, f PlanFilter) ([]kobiplan.Plan, error)
	// LiveForCluster returns the org's live plans (draft or exported) on one
	// cluster: Free keeps one at a time per cluster (P21).
	LiveForCluster(tenantID, clusterID string) ([]kobiplan.Plan, error)
	// PruneOrg deletes the org's plans last updated before `before`, every
	// version with them. Deleting a conversation does not delete its plans.
	PruneOrg(tenantID string, before time.Time) (int, error)
}

// prepareWrite fills what the store owns and validates the result.
func prepareWrite(p *kobiplan.Plan, now time.Time) error {
	if p.OrgID == "" {
		return fmt.Errorf("plan %s: no org", p.ID)
	}
	p.UpdatedAt = now
	if p.ContentHash == "" {
		p.ContentHash = kobiplan.ContentHash(*p)
	}
	return p.Validate()
}

// checkUpdate is Update's rule, shared by both engines.
func checkUpdate(cur, next *kobiplan.Plan) error {
	if next.Version != cur.Version {
		return fmt.Errorf("%w: update of version %d, current is %d", ErrPlanVersionConflict, next.Version, cur.Version)
	}
	if next.ContentHash != cur.ContentHash {
		return ErrPlanContentChanged
	}
	return nil
}

// checkRevise is Revise's rule, shared by both engines.
func checkRevise(cur, next *kobiplan.Plan) error {
	if next.Version != cur.Version+1 {
		return fmt.Errorf("%w: revision to version %d, current is %d", ErrPlanVersionConflict, next.Version, cur.Version)
	}
	return nil
}

func sortPlans(ps []kobiplan.Plan) {
	sort.SliceStable(ps, func(i, j int) bool {
		if !ps[i].UpdatedAt.Equal(ps[j].UpdatedAt) {
			return ps[i].UpdatedAt.After(ps[j].UpdatedAt)
		}
		return ps[i].ID < ps[j].ID
	})
}

// BoltPlanStore is the PlanStore of the single-tenant installs (OSS, and EE
// without Postgres). The current version lives in one bucket keyed by id, the
// earlier ones in another keyed id\0version. The org travels inside the JSON,
// for parity with Postgres, and is checked on every read; it is not part of
// the key.
type BoltPlanStore struct {
	db       *bolt.DB
	plans    []byte
	versions []byte
	now      func() time.Time
}

var _ PlanStore = (*BoltPlanStore)(nil)

// NewBoltPlanStore creates both buckets when missing.
func NewBoltPlanStore(db *bolt.DB, plansBucket, versionsBucket []byte) (*BoltPlanStore, error) {
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{plansBucket, versionsBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("create %s buckets: %w", plansBucket, err)
	}
	return &BoltPlanStore{db: db, plans: plansBucket, versions: versionsBucket,
		now: func() time.Time { return time.Now().UTC() }}, nil
}

func planVersionKey(id string, version int) []byte {
	return []byte(fmt.Sprintf("%s\x00%08d", id, version))
}

func decodePlan(raw []byte) (*kobiplan.Plan, error) {
	var p kobiplan.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// current reads the current version, or ErrPlanNotFound when it is absent or
// belongs to another org.
func (s *BoltPlanStore) current(tx *bolt.Tx, tenantID, id string) (*kobiplan.Plan, []byte, error) {
	raw := tx.Bucket(s.plans).Get([]byte(id))
	if raw == nil {
		return nil, nil, ErrPlanNotFound
	}
	p, err := decodePlan(raw)
	if err != nil {
		return nil, nil, err
	}
	if p.OrgID != tenantID {
		return nil, nil, ErrPlanNotFound
	}
	return p, raw, nil
}

func (s *BoltPlanStore) Create(p kobiplan.Plan) error {
	if err := prepareWrite(&p, s.now()); err != nil {
		return err
	}
	if p.Version != 1 {
		return fmt.Errorf("%w: a new plan starts at version 1, not %d", ErrPlanVersionConflict, p.Version)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.plans)
		if b.Get([]byte(p.ID)) != nil {
			return ErrPlanExists
		}
		return b.Put([]byte(p.ID), raw)
	})
}

func (s *BoltPlanStore) Update(p kobiplan.Plan) error {
	if err := prepareWrite(&p, s.now()); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		cur, _, err := s.current(tx, p.OrgID, p.ID)
		if err != nil {
			return err
		}
		if err := checkUpdate(cur, &p); err != nil {
			return err
		}
		return tx.Bucket(s.plans).Put([]byte(p.ID), raw)
	})
}

func (s *BoltPlanStore) Revise(p kobiplan.Plan) error {
	if err := prepareWrite(&p, s.now()); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		cur, curRaw, err := s.current(tx, p.OrgID, p.ID)
		if err != nil {
			return err
		}
		if err := checkRevise(cur, &p); err != nil {
			return err
		}
		if err := tx.Bucket(s.versions).Put(planVersionKey(cur.ID, cur.Version), curRaw); err != nil {
			return err
		}
		return tx.Bucket(s.plans).Put([]byte(p.ID), raw)
	})
}

func (s *BoltPlanStore) Get(tenantID, id string) (*kobiplan.Plan, error) {
	var out *kobiplan.Plan
	err := s.db.View(func(tx *bolt.Tx) error {
		p, _, err := s.current(tx, tenantID, id)
		out = p
		return err
	})
	return out, err
}

func (s *BoltPlanStore) GetVersion(tenantID, id string, version int) (*kobiplan.Plan, error) {
	var out *kobiplan.Plan
	err := s.db.View(func(tx *bolt.Tx) error {
		cur, _, err := s.current(tx, tenantID, id)
		if err != nil {
			return err
		}
		if cur.Version == version {
			out = cur
			return nil
		}
		raw := tx.Bucket(s.versions).Get(planVersionKey(id, version))
		if raw == nil {
			return ErrPlanNotFound
		}
		out, err = decodePlan(raw)
		return err
	})
	return out, err
}

func (s *BoltPlanStore) List(tenantID string, f PlanFilter) ([]kobiplan.Plan, error) {
	out := []kobiplan.Plan{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(s.plans).ForEach(func(_, raw []byte) error {
			p, err := decodePlan(raw)
			if err != nil || p.OrgID != tenantID || !f.match(p) {
				return nil
			}
			out = append(out, *p)
			return nil
		})
	})
	sortPlans(out)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, err
}

func (s *BoltPlanStore) LiveForCluster(tenantID, clusterID string) ([]kobiplan.Plan, error) {
	return s.List(tenantID, PlanFilter{ClusterID: clusterID, Statuses: []kobiplan.Status{kobiplan.StatusDraft, kobiplan.StatusExported}})
}

func (s *BoltPlanStore) PruneOrg(tenantID string, before time.Time) (int, error) {
	n := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		plans, versions := tx.Bucket(s.plans), tx.Bucket(s.versions)
		var gone []string
		if err := plans.ForEach(func(k, raw []byte) error {
			p, err := decodePlan(raw)
			if err == nil && p.OrgID == tenantID && p.UpdatedAt.Before(before) {
				gone = append(gone, string(k))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, id := range gone {
			prefix := []byte(id + "\x00")
			var old [][]byte
			c := versions.Cursor()
			for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
				old = append(old, append([]byte(nil), k...))
			}
			for _, k := range old {
				if err := versions.Delete(k); err != nil {
					return err
				}
			}
			if err := plans.Delete([]byte(id)); err != nil {
				return err
			}
		}
		n = len(gone)
		return nil
	})
	return n, err
}
