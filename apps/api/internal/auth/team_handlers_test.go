package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newHierarchyHandlers builds an org/team-wired Handlers on a fresh BoltDB:
// default tenant (org) + default team, wired via SetOrgTeamContext exactly like
// main.go does at boot.
func newHierarchyHandlers(t *testing.T) (*Handlers, *BoltTeamStore, string) {
	t.Helper()
	s := newTestStore(t)
	tenants, err := NewTenantsStore(s.DB())
	if err != nil {
		t.Fatalf("NewTenantsStore: %v", err)
	}
	teams, err := NewTeamStore(s.DB())
	if err != nil {
		t.Fatalf("NewTeamStore: %v", err)
	}
	dt := ensureFixtureDefaultTenant(t, tenants)
	team, err := teams.EnsureDefaultTeam(context.Background(), dt.ID)
	if err != nil {
		t.Fatalf("EnsureDefaultTeam: %v", err)
	}
	h := &Handlers{store: s, authEnabled: true}
	h.SetOrgTeamContext(teams, tenants, dt.ID, team.ID)
	return h, teams, team.ID
}

// mountUsers wires POST /users + DELETE /users/{id} with a synthetic admin
// caller, so the membership-lifecycle handlers run with chi URL params + claims.
func mountUsers(h *Handlers, callerID string) http.Handler {
	r := chi.NewRouter()
	r.Route("/users", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := context.WithValue(req.Context(), claimsKey, &Claims{
					UserID: callerID, Username: "admin", Role: RoleAdmin,
				})
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Post("/", h.CreateUser)
		r.Delete("/{id}", h.DeleteUser)
	})
	return r
}

func memberIDs(t *testing.T, teams *BoltTeamStore, teamID string) map[string]bool {
	t.Helper()
	members, err := teams.ListMembers(context.Background(), teamID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	out := map[string]bool{}
	for _, m := range members {
		out[m.UserID] = true
	}
	return out
}

// TestCreateUser_DefaultTeamEnrolment pins the edition-specific behavior of
// create-without-a-team:
//   - OSS: every user is auto-enrolled in the single default team.
//   - Multi-tenant: the user is org-only (no team) — the admin assigns teams
//     explicitly. (Auto-enrolling cross-org against a boot-pinned default team
//     UUID was the bug this change fixes.)
func TestCreateUser_DefaultTeamEnrolment(t *testing.T) {
	h, teams, teamID := newHierarchyHandlers(t)
	srv := mountUsers(h, "caller-admin")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"username":"alice","password":"password123","name":"Alice","role":"editor"}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("CreateUser status = %d, body=%s", rr.Code, rr.Body)
	}
	var created UserResponse
	decodeJSON(t, rr.Body.Bytes(), &created)

	enrolled := memberIDs(t, teams, teamID)[created.ID]
	if MultiTenantEnabled {
		if enrolled {
			t.Errorf("multi-tenant: new user %s should be org-only (no auto-enrol), but was enrolled", created.ID)
		}
	} else if !enrolled {
		t.Errorf("OSS: new user %s was not enrolled in the default team", created.ID)
	}
}

// TestCreateUser_WithTeamID enrolls the new user in an explicitly chosen team
// at creation — the org-level "create user" modal's optional team selector and
// the team-level "create user" path. Edition-agnostic on membership; the
// team_role elevation is honored only in multi-tenant (OSS teams never elevate).
func TestCreateUser_WithTeamID(t *testing.T) {
	h, teams, teamID := newHierarchyHandlers(t)
	srv := mountUsers(h, "caller-admin")

	rr := httptest.NewRecorder()
	body := `{"username":"erin","password":"password123","name":"Erin","role":"viewer","teamId":"` + teamID + `","teamRole":"editor"}`
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("CreateUser status = %d, body=%s", rr.Code, rr.Body)
	}
	var created UserResponse
	decodeJSON(t, rr.Body.Bytes(), &created)

	m, ok, err := teams.GetMembership(context.Background(), teamID, created.ID)
	if err != nil || !ok {
		t.Fatalf("expected membership in team %s, ok=%v err=%v", teamID, ok, err)
	}
	if MultiTenantEnabled && m.TeamRole != RoleEditor {
		t.Errorf("team role = %q, want editor", m.TeamRole)
	}
}

// TestCreateUser_InvalidTeamID rejects an unknown / cross-org team id up front
// (multi-tenant). OSS ignores teamId entirely, so this guard is multi-tenant.
func TestCreateUser_InvalidTeamID(t *testing.T) {
	if !MultiTenantEnabled {
		t.Skip("team-id validation is a multi-tenant path")
	}
	h, _, _ := newHierarchyHandlers(t)
	srv := mountUsers(h, "caller-admin")

	rr := httptest.NewRecorder()
	body := `{"username":"frank","password":"password123","name":"Frank","role":"viewer","teamId":"does-not-exist"}`
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body)
	}
}

func TestDeleteUser_RemovesMembership(t *testing.T) {
	h, teams, teamID := newHierarchyHandlers(t)
	// Create an editor directly + enroll, then delete via the handler. Editor
	// (not admin) avoids the last-admin protection; caller != target avoids the
	// self-delete guard.
	u, err := h.store.CreateUser(context.Background(), "bob", "", "Bob", "password123", RoleEditor)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	h.enrollNewUser(context.Background(), u.ID, teamID, "")
	if !memberIDs(t, teams, teamID)[u.ID] {
		t.Fatalf("precondition: bob should be a member before delete")
	}

	srv := mountUsers(h, "caller-admin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/users/"+u.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("DeleteUser status = %d, body=%s", rr.Code, rr.Body)
	}
	if memberIDs(t, teams, teamID)[u.ID] {
		t.Errorf("membership for deleted user %s should be gone", u.ID)
	}
}

// TestDeleteUser_RemovesAllMemberships guards the orphan-row regression: a user
// in MULTIPLE teams must be removed from every one on delete (not just the
// default), or the team list shows a blank "ghost" member.
func TestDeleteUser_RemovesAllMemberships(t *testing.T) {
	h, teams, teamA := newHierarchyHandlers(t)
	teamB, err := teams.CreateTeam(context.Background(), h.defaultOrgID, "team-b")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	u, err := h.store.CreateUser(context.Background(), "grace", "", "Grace", "password123", RoleEditor)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, tid := range []string{teamA, teamB.ID} {
		if _, err := teams.AddMember(context.Background(), tid, u.ID, ""); err != nil {
			t.Fatalf("AddMember %s: %v", tid, err)
		}
	}

	srv := mountUsers(h, "caller-admin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/users/"+u.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("DeleteUser status = %d, body=%s", rr.Code, rr.Body)
	}
	if memberIDs(t, teams, teamA)[u.ID] || memberIDs(t, teams, teamB.ID)[u.ID] {
		t.Errorf("deleted user %s should be removed from ALL teams (no orphan rows)", u.ID)
	}
}

func TestGetMe_IncludesOrgAndTeamWithEffectiveRole(t *testing.T) {
	h, _, teamID := newHierarchyHandlers(t)
	u, err := h.store.CreateUser(context.Background(), "carol", "", "Carol", "password123", RoleViewer)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// Enroll explicitly in the default team: multi-tenant no longer auto-enrolls
	// (org-only is valid), so a "has a team" assertion must put them in one.
	h.enrollNewUser(context.Background(), u.ID, teamID, "")

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, &Claims{
		UserID: u.ID, Username: "carol", Role: RoleViewer,
	}))
	rr := httptest.NewRecorder()
	h.GetMe(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GetMe status = %d, body=%s", rr.Code, rr.Body)
	}

	var me meResponse
	decodeJSON(t, rr.Body.Bytes(), &me)
	if me.Org == nil || me.Org.Name != DefaultTenantName {
		t.Errorf("expected org=%q, got %+v", DefaultTenantName, me.Org)
	}
	if me.Team == nil || me.Team.Name != DefaultTeamName {
		t.Errorf("expected team=%q, got %+v", DefaultTeamName, me.Team)
	}
	// OSS team_role is "" → effective role equals the org role.
	if me.Team != nil && me.Team.Role != RoleViewer {
		t.Errorf("effective team role = %q, want %q (org role inherited)", me.Team.Role, RoleViewer)
	}
}

func TestListTeams_ReturnsDefaultTeamWithMemberCount(t *testing.T) {
	h, _, teamID := newHierarchyHandlers(t)
	u, _ := h.store.CreateUser(context.Background(), "dave", "", "Dave", "password123", RoleEditor)
	h.enrollNewUser(context.Background(), u.ID, teamID, "")

	rr := httptest.NewRecorder()
	h.ListTeams(rr, httptest.NewRequest(http.MethodGet, "/teams", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("ListTeams status = %d", rr.Code)
	}
	var got []teamSummary
	decodeJSON(t, rr.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Name != DefaultTeamName {
		t.Fatalf("expected the single default team, got %+v", got)
	}
	if got[0].MemberCount != 1 {
		t.Errorf("memberCount = %d, want 1", got[0].MemberCount)
	}
}

func TestCreateTeam_OSSGuardrailRequiresEE(t *testing.T) {
	withMultiTenant(t, false) // OSS default
	h, _, _ := newHierarchyHandlers(t)

	rr := httptest.NewRecorder()
	h.CreateTeam(rr, httptest.NewRequest(http.MethodPost, "/teams",
		strings.NewReader(`{"name":"platform"}`)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("OSS CreateTeam should 409, got %d body=%s", rr.Code, rr.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != ErrCodeRequiresEE {
		t.Errorf("expected code %q, got %q", ErrCodeRequiresEE, body["code"])
	}
}

// reqWithPrincipal builds a request whose context carries an authenticated
// principal, the way RequireAuth would in production.
func reqWithPrincipal(userID string, role Role) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return r.WithContext(context.WithValue(r.Context(), claimsKey, &Claims{UserID: userID, Role: role}))
}

// TestCanManageTeam pins the team-admin authorization (Track D §11): org admins
// manage any team; a team's own admin manages that team; a plain member cannot.
func TestCanManageTeam(t *testing.T) {
	h, teams, teamID := newHierarchyHandlers(t)

	// Org admin → manages any team in the org.
	if !h.canManageTeam(reqWithPrincipal("admin-uid", RoleAdmin), teamID) {
		t.Error("org admin should manage any team")
	}

	// A viewer who is the team's admin (team_role admin) → manages it.
	ta, _ := h.store.CreateUser(context.Background(), "ta", "", "TA", "password123", RoleViewer)
	if _, err := teams.AddMember(context.Background(), teamID, ta.ID, RoleAdmin); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if !h.canManageTeam(reqWithPrincipal(ta.ID, RoleViewer), teamID) {
		t.Error("team-admin should manage their own team")
	}

	// A viewer who is a plain member (inherits org role) → cannot manage.
	tm, _ := h.store.CreateUser(context.Background(), "tm", "", "TM", "password123", RoleViewer)
	if _, err := teams.AddMember(context.Background(), teamID, tm.ID, ""); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if h.canManageTeam(reqWithPrincipal(tm.ID, RoleViewer), teamID) {
		t.Error("a plain member must not manage the team")
	}

	// A non-member viewer → cannot manage.
	if h.canManageTeam(reqWithPrincipal("stranger", RoleViewer), teamID) {
		t.Error("a non-member must not manage the team")
	}
}
