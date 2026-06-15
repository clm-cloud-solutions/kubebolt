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
	dt, err := tenants.GetDefaultTenant()
	if err != nil {
		t.Fatalf("GetDefaultTenant: %v", err)
	}
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

func TestCreateUser_EnrollsInDefaultTeam(t *testing.T) {
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

	if !memberIDs(t, teams, teamID)[created.ID] {
		t.Errorf("new user %s was not enrolled in the default team", created.ID)
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
	h.enrollInDefaultTeam(context.Background(), u.ID)
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

func TestGetMe_IncludesOrgAndTeamWithEffectiveRole(t *testing.T) {
	h, _, _ := newHierarchyHandlers(t)
	u, err := h.store.CreateUser(context.Background(), "carol", "", "Carol", "password123", RoleViewer)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	h.enrollInDefaultTeam(context.Background(), u.ID)

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
	h, _, _ := newHierarchyHandlers(t)
	u, _ := h.store.CreateUser(context.Background(), "dave", "", "Dave", "password123", RoleEditor)
	h.enrollInDefaultTeam(context.Background(), u.ID)

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
