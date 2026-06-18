// Package auth — teams_store hosts the BoltDB-backed registry of Teams and
// their memberships, the W1 identity seam (see
// internal/saas/kubebolt-e1-multitenant-scoping.md §8).
//
// A Team groups users by functional/business area ("Platform", "Data") inside
// an Organization (= Tenant). It is NOT the pricing tier "team" (that's
// Tenant.Plan — a different layer). Membership ties a user to a team and may
// carry a team_role that ELEVATES the user's org-level role within that team
// (effective = max(org_role, team_role)); empty = inherit the org role.
//
// OSS is degenerate: a single default org + single default team, every user a
// member, no role elevation / cross-team grants / management UI. EE swaps a
// Postgres TeamStore that activates multi-team, segmentation, and grants — the
// interface carries the full shape so EE never edits it.
//
// Layout (three buckets, sharing kubebolt.db):
//
//	teams             key: team_id (uuid)         value: Team JSON
//	team_name_index   key: org_id/lower(name)     value: team_id   (uniqueness per org)
//	team_members      key: team_id/user_id        value: Membership JSON
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var (
	teamsBucket         = []byte("teams")
	teamNameIndexBucket = []byte("team_name_index")
	teamMembersBucket   = []byte("team_members")
)

// DefaultTeamName is the auto-seeded team every org gets at bootstrap; in OSS
// it's the only team and owns all clusters.
const DefaultTeamName = "default"

var (
	ErrTeamNotFound = errors.New("team not found")
	ErrTeamExists   = errors.New("team name already exists in organization")
)

// Team groups users by functional/business area within an Organization.
type Team struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"orgId"` // = Tenant.ID
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Membership ties a user to a team. TeamRole only ELEVATES the user's org_role
// within this team; empty = inherit the org role unchanged.
type Membership struct {
	TeamID    string    `json:"teamId"`
	UserID    string    `json:"userId"`
	TeamRole  Role      `json:"teamRole,omitempty"` // "" = inherit org_role
	CreatedAt time.Time `json:"createdAt"`
}

// EffectiveRole resolves a user's role inside a team: the team role only
// elevates, never lowers. Empty org_role + empty team_role = "" (no access),
// which is the "team-only user not a member of this team" case (§8.2).
func EffectiveRole(orgRole, teamRole Role) Role {
	if RoleLevel(teamRole) > RoleLevel(orgRole) {
		return teamRole
	}
	return orgRole
}

// TeamStore is the W1 seam. OSS = BoltTeamStore (degenerate single team); EE =
// PostgresTeamStore (multi-team). MemoryTeamStore backs tests.
type TeamStore interface {
	// EnsureDefaultTeam returns the org's "default" team, creating it if absent.
	EnsureDefaultTeam(ctx context.Context, orgID string) (*Team, error)
	CreateTeam(ctx context.Context, orgID, name string) (*Team, error)
	GetTeam(ctx context.Context, id string) (*Team, error)
	ListTeams(ctx context.Context, orgID string) ([]Team, error)
	UpdateTeam(ctx context.Context, id string, mut func(*Team) error) (*Team, error)
	DeleteTeam(ctx context.Context, id string) error

	// AddMember adds (or updates the team_role of) a user in a team. Idempotent.
	AddMember(ctx context.Context, teamID, userID string, teamRole Role) (*Membership, error)
	RemoveMember(ctx context.Context, teamID, userID string) error
	GetMembership(ctx context.Context, teamID, userID string) (*Membership, bool, error)
	ListMembers(ctx context.Context, teamID string) ([]Membership, error)
	// ListUserTeams returns every membership for a user — drives role/access
	// resolution across the teams they belong to.
	ListUserTeams(ctx context.Context, userID string) ([]Membership, error)
}

func teamNameKey(orgID, name string) []byte {
	return []byte(orgID + "/" + strings.ToLower(strings.TrimSpace(name)))
}

func memberKey(teamID, userID string) []byte {
	return []byte(teamID + "/" + userID)
}

// ─── Bolt impl ────────────────────────────────────────────────────────────

type BoltTeamStore struct {
	db *bolt.DB
}

// Compile-time guarantee the Bolt impl satisfies the seam.
var _ TeamStore = (*BoltTeamStore)(nil)

func NewTeamStore(db *bolt.DB) (*BoltTeamStore, error) {
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{teamsBucket, teamNameIndexBucket, teamMembersBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("creating team buckets: %w", err)
	}
	return &BoltTeamStore{db: db}, nil
}

func (s *BoltTeamStore) EnsureDefaultTeam(ctx context.Context, orgID string) (*Team, error) {
	var found *Team
	if err := s.db.View(func(tx *bolt.Tx) error {
		if id := tx.Bucket(teamNameIndexBucket).Get(teamNameKey(orgID, DefaultTeamName)); id != nil {
			raw := tx.Bucket(teamsBucket).Get(id)
			if raw != nil {
				var t Team
				if err := json.Unmarshal(raw, &t); err != nil {
					return err
				}
				found = &t
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if found != nil {
		return found, nil
	}
	return s.CreateTeam(ctx, orgID, DefaultTeamName)
}

func (s *BoltTeamStore) CreateTeam(_ context.Context, orgID, name string) (*Team, error) {
	name = strings.TrimSpace(name)
	if orgID == "" || name == "" {
		return nil, fmt.Errorf("orgID and name are required")
	}
	now := time.Now().UTC()
	team := &Team{ID: uuid.New().String(), OrgID: orgID, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		idx := tx.Bucket(teamNameIndexBucket)
		if idx.Get(teamNameKey(orgID, name)) != nil {
			return ErrTeamExists
		}
		enc, err := json.Marshal(team)
		if err != nil {
			return err
		}
		if err := tx.Bucket(teamsBucket).Put([]byte(team.ID), enc); err != nil {
			return err
		}
		return idx.Put(teamNameKey(orgID, name), []byte(team.ID))
	}); err != nil {
		return nil, err
	}
	return team, nil
}

func (s *BoltTeamStore) GetTeam(_ context.Context, id string) (*Team, error) {
	var team *Team
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(teamsBucket).Get([]byte(id))
		if raw == nil {
			return ErrTeamNotFound
		}
		var t Team
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		team = &t
		return nil
	})
	return team, err
}

func (s *BoltTeamStore) ListTeams(_ context.Context, orgID string) ([]Team, error) {
	var out []Team
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(teamsBucket).ForEach(func(_, v []byte) error {
			var t Team
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			if orgID == "" || t.OrgID == orgID {
				out = append(out, t)
			}
			return nil
		})
	})
	return out, err
}

func (s *BoltTeamStore) UpdateTeam(_ context.Context, id string, mut func(*Team) error) (*Team, error) {
	var team *Team
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(teamsBucket)
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrTeamNotFound
		}
		var t Team
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		oldName := t.Name
		if err := mut(&t); err != nil {
			return err
		}
		idx := tx.Bucket(teamNameIndexBucket)
		if !strings.EqualFold(t.Name, oldName) {
			if idx.Get(teamNameKey(t.OrgID, t.Name)) != nil {
				return ErrTeamExists
			}
			if err := idx.Delete(teamNameKey(t.OrgID, oldName)); err != nil {
				return err
			}
			if err := idx.Put(teamNameKey(t.OrgID, t.Name), []byte(t.ID)); err != nil {
				return err
			}
		}
		t.UpdatedAt = time.Now().UTC()
		enc, err := json.Marshal(&t)
		if err != nil {
			return err
		}
		team = &t
		return b.Put([]byte(id), enc)
	})
	return team, err
}

func (s *BoltTeamStore) DeleteTeam(_ context.Context, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(teamsBucket)
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrTeamNotFound
		}
		var t Team
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		if err := tx.Bucket(teamNameIndexBucket).Delete(teamNameKey(t.OrgID, t.Name)); err != nil {
			return err
		}
		// Drop all memberships of this team.
		mb := tx.Bucket(teamMembersBucket)
		c := mb.Cursor()
		prefix := []byte(id + "/")
		var toDelete [][]byte
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, _ = c.Next() {
			toDelete = append(toDelete, append([]byte(nil), k...))
		}
		for _, k := range toDelete {
			if err := mb.Delete(k); err != nil {
				return err
			}
		}
		return b.Delete([]byte(id))
	})
}

func (s *BoltTeamStore) AddMember(_ context.Context, teamID, userID string, teamRole Role) (*Membership, error) {
	if teamRole != "" && !ValidRole(teamRole) {
		return nil, fmt.Errorf("invalid team role %q", teamRole)
	}
	m := &Membership{TeamID: teamID, UserID: userID, TeamRole: teamRole, CreatedAt: time.Now().UTC()}
	err := s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(teamsBucket).Get([]byte(teamID)) == nil {
			return ErrTeamNotFound
		}
		// Preserve CreatedAt if the membership already exists (this is an update).
		if raw := tx.Bucket(teamMembersBucket).Get(memberKey(teamID, userID)); raw != nil {
			var prev Membership
			if err := json.Unmarshal(raw, &prev); err == nil {
				m.CreatedAt = prev.CreatedAt
			}
		}
		enc, err := json.Marshal(m)
		if err != nil {
			return err
		}
		return tx.Bucket(teamMembersBucket).Put(memberKey(teamID, userID), enc)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *BoltTeamStore) RemoveMember(_ context.Context, teamID, userID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(teamMembersBucket).Delete(memberKey(teamID, userID))
	})
}

func (s *BoltTeamStore) GetMembership(_ context.Context, teamID, userID string) (*Membership, bool, error) {
	var m *Membership
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(teamMembersBucket).Get(memberKey(teamID, userID))
		if raw == nil {
			return nil
		}
		var mm Membership
		if err := json.Unmarshal(raw, &mm); err != nil {
			return err
		}
		m = &mm
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return m, m != nil, nil
}

func (s *BoltTeamStore) ListMembers(_ context.Context, teamID string) ([]Membership, error) {
	var out []Membership
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(teamMembersBucket).Cursor()
		prefix := []byte(teamID + "/")
		for k, v := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, v = c.Next() {
			var m Membership
			if err := json.Unmarshal(v, &m); err != nil {
				return err
			}
			out = append(out, m)
		}
		return nil
	})
	return out, err
}

// ListUserTeams scans memberships filtering by user. OSS scale (one default
// team, all users) makes the scan trivial; the EE Postgres impl indexes user_id.
func (s *BoltTeamStore) ListUserTeams(_ context.Context, userID string) ([]Membership, error) {
	var out []Membership
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(teamMembersBucket).ForEach(func(_, v []byte) error {
			var m Membership
			if err := json.Unmarshal(v, &m); err != nil {
				return err
			}
			if m.UserID == userID {
				out = append(out, m)
			}
			return nil
		})
	})
	return out, err
}
