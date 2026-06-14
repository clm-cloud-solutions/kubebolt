package auth

import (
	"errors"
	"testing"
)

func newTestTeamStore(t *testing.T) *BoltTeamStore {
	t.Helper()
	s := newTestStore(t)
	ts, err := NewTeamStore(s.DB())
	if err != nil {
		t.Fatalf("NewTeamStore: %v", err)
	}
	return ts
}

func TestTeamStore_CreateGetList(t *testing.T) {
	ts := newTestTeamStore(t)
	org := "org-1"
	a, err := ts.CreateTeam(org, "Platform")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if a.ID == "" || a.OrgID != org || a.Name != "Platform" {
		t.Fatalf("unexpected team: %#v", a)
	}
	if _, err := ts.CreateTeam(org, "Data"); err != nil {
		t.Fatalf("CreateTeam Data: %v", err)
	}
	got, err := ts.GetTeam(a.ID)
	if err != nil || got.Name != "Platform" {
		t.Fatalf("GetTeam = %#v, %v", got, err)
	}
	teams, err := ts.ListTeams(org)
	if err != nil || len(teams) != 2 {
		t.Fatalf("ListTeams = %d teams, %v", len(teams), err)
	}
	// Other orgs don't leak in.
	if other, _ := ts.ListTeams("org-2"); len(other) != 0 {
		t.Fatalf("ListTeams(org-2) should be empty, got %d", len(other))
	}
}

func TestTeamStore_NameUniquePerOrg(t *testing.T) {
	ts := newTestTeamStore(t)
	if _, err := ts.CreateTeam("org-1", "Platform"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := ts.CreateTeam("org-1", "platform"); !errors.Is(err, ErrTeamExists) {
		t.Fatalf("dup name (case-insensitive) should be ErrTeamExists, got %v", err)
	}
	// Same name in a DIFFERENT org is fine.
	if _, err := ts.CreateTeam("org-2", "Platform"); err != nil {
		t.Fatalf("same name other org should succeed, got %v", err)
	}
}

func TestTeamStore_EnsureDefaultIdempotent(t *testing.T) {
	ts := newTestTeamStore(t)
	a, err := ts.EnsureDefaultTeam("org-1")
	if err != nil || a.Name != DefaultTeamName {
		t.Fatalf("EnsureDefaultTeam = %#v, %v", a, err)
	}
	b, err := ts.EnsureDefaultTeam("org-1")
	if err != nil {
		t.Fatalf("second EnsureDefaultTeam: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("EnsureDefaultTeam not idempotent: %s != %s", a.ID, b.ID)
	}
	if teams, _ := ts.ListTeams("org-1"); len(teams) != 1 {
		t.Fatalf("expected 1 default team, got %d", len(teams))
	}
}

func TestTeamStore_Membership(t *testing.T) {
	ts := newTestTeamStore(t)
	team, _ := ts.CreateTeam("org-1", "Platform")

	if _, err := ts.AddMember(team.ID, "user-A", RoleEditor); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := ts.AddMember(team.ID, "user-B", ""); err != nil { // inherit
		t.Fatalf("AddMember inherit: %v", err)
	}
	// AddMember to a missing team fails.
	if _, err := ts.AddMember("nope", "user-A", RoleViewer); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("AddMember to missing team = %v, want ErrTeamNotFound", err)
	}

	m, ok, err := ts.GetMembership(team.ID, "user-A")
	if err != nil || !ok || m.TeamRole != RoleEditor {
		t.Fatalf("GetMembership = %#v, ok=%v, %v", m, ok, err)
	}
	members, _ := ts.ListMembers(team.ID)
	if len(members) != 2 {
		t.Fatalf("ListMembers = %d, want 2", len(members))
	}

	// Update the team_role (idempotent upsert).
	if _, err := ts.AddMember(team.ID, "user-A", RoleAdmin); err != nil {
		t.Fatalf("update member: %v", err)
	}
	if m, _, _ := ts.GetMembership(team.ID, "user-A"); m.TeamRole != RoleAdmin {
		t.Fatalf("team role not updated: %#v", m)
	}

	// ListUserTeams across teams.
	team2, _ := ts.CreateTeam("org-1", "Data")
	ts.AddMember(team2.ID, "user-A", RoleViewer)
	ut, _ := ts.ListUserTeams("user-A")
	if len(ut) != 2 {
		t.Fatalf("ListUserTeams(user-A) = %d, want 2", len(ut))
	}

	// Remove.
	if err := ts.RemoveMember(team.ID, "user-B"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, ok, _ := ts.GetMembership(team.ID, "user-B"); ok {
		t.Fatalf("user-B should be gone")
	}
}

func TestTeamStore_DeleteDropsMemberships(t *testing.T) {
	ts := newTestTeamStore(t)
	team, _ := ts.CreateTeam("org-1", "Platform")
	ts.AddMember(team.ID, "user-A", RoleEditor)
	if err := ts.DeleteTeam(team.ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if _, err := ts.GetTeam(team.ID); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("team should be gone, got %v", err)
	}
	if members, _ := ts.ListMembers(team.ID); len(members) != 0 {
		t.Fatalf("memberships should be dropped, got %d", len(members))
	}
	// Name index freed → can recreate.
	if _, err := ts.CreateTeam("org-1", "Platform"); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

func TestEffectiveRole(t *testing.T) {
	cases := []struct {
		name             string
		orgRole, teamRole, want Role
	}{
		{"team elevates viewer→editor", RoleViewer, RoleEditor, RoleEditor},
		{"team can't lower admin", RoleAdmin, RoleViewer, RoleAdmin},
		{"empty team role inherits org", RoleEditor, "", RoleEditor},
		{"org member no team role", RoleViewer, "", RoleViewer},
		{"team-only user (no org role) gets team role", "", RoleEditor, RoleEditor},
		{"team-only, not member of team → no access", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveRole(tc.orgRole, tc.teamRole); got != tc.want {
				t.Fatalf("EffectiveRole(%q,%q) = %q, want %q", tc.orgRole, tc.teamRole, got, tc.want)
			}
		})
	}
}
