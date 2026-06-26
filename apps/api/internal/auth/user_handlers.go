package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// --- List users ---

// ListUsers returns all users (admin only).
func (h *Handlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	resp := make([]UserResponse, len(users))
	for i, u := range users {
		resp[i] = u.ToResponse()
	}

	respondJSON(w, http.StatusOK, resp)
}

// --- User directory (org-admin OR team-admin) ---

// userDirectoryEntry is the minimal, non-sensitive projection of a user used to
// populate team-membership pickers. It deliberately omits role / email / login
// timestamps — a team-admin only needs to identify who to add.
type userDirectoryEntry struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// isOrgOrTeamAdmin reports whether the caller is an org admin OR an admin of at
// least one team. Used to gate the user directory: a team-admin needs to see the
// org's users to add members, but must NOT reach the full admin-only /users CRUD.
func (h *Handlers) isOrgOrTeamAdmin(r *http.Request) bool {
	if ContextRole(r) == RoleAdmin {
		return true
	}
	if h.teams == nil {
		return false
	}
	uid := ContextUserID(r)
	if uid == "" {
		return false
	}
	memberships, err := h.teams.ListUserTeams(r.Context(), uid)
	if err != nil {
		return false
	}
	for _, m := range memberships {
		if m.TeamRole == RoleAdmin {
			return true
		}
	}
	return false
}

// ListUsersDirectory returns a minimal {id, username, name} listing of the org's
// users. Accessible to org admins AND team admins — the latter need it to pick
// members for the teams they manage, but the full /users CRUD stays org-admin
// only. RLS scopes ListUsers to the caller's org.
func (h *Handlers) ListUsersDirectory(w http.ResponseWriter, r *http.Request) {
	if !h.isOrgOrTeamAdmin(r) {
		respondError(w, http.StatusForbidden, "admin or team-admin access required")
		return
	}

	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	resp := make([]userDirectoryEntry, len(users))
	for i, u := range users {
		resp[i] = userDirectoryEntry{ID: u.ID, Username: u.Username, Name: u.Name}
	}

	respondJSON(w, http.StatusOK, resp)
}

// --- Create user ---

type createUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	// Role is the user's ORG role. Every user belongs to the caller's org —
	// org membership is the invariant; team membership is optional (TeamID).
	Role Role `json:"role"`
	// TeamID optionally enrolls the new user in a team at creation time. It
	// MUST belong to the caller's org. Empty = org-only (a valid state). This
	// is the multi-tenant path: the org-level "create user" modal offers an
	// optional team selector, and the team-level "create user" passes the
	// team's id with Role=viewer. Ignored in OSS (single default team).
	TeamID string `json:"teamId,omitempty"`
	// TeamRole is the optional team-level elevation for TeamID. "" inherits the
	// org role. Only meaningful alongside TeamID.
	TeamRole Role `json:"teamRole,omitempty"`
}

// CreateUser creates a new user (admin only).
func (h *Handlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		respondError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	if len(req.Password) < 8 {
		respondError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	if !ValidRole(req.Role) {
		respondError(w, http.StatusBadRequest, "role must be admin, editor, or viewer")
		return
	}

	if req.TeamRole != "" && !ValidRole(req.TeamRole) {
		respondError(w, http.StatusBadRequest, "team role must be admin, editor, or viewer")
		return
	}

	// Validate the chosen team up front (multi-tenant) so an invalid id is
	// rejected cleanly instead of creating the user and silently failing the
	// enrolment. RLS scopes GetTeam to the caller's org, so a cross-org id
	// resolves to not-found; the explicit OrgID check is belt-and-suspenders.
	if MultiTenantEnabled && req.TeamID != "" && h.teams != nil {
		team, err := h.teams.GetTeam(r.Context(), req.TeamID)
		if err != nil || team.OrgID != h.callerOrgID(r) {
			respondError(w, http.StatusBadRequest, "selected team not found in your organization")
			return
		}
	}

	user, err := h.store.CreateUser(r.Context(), req.Username, req.Email, req.Name, req.Password, req.Role)
	if err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}

	// Materialize the new user's team membership per edition (see enrollNewUser).
	h.enrollNewUser(r.Context(), user.ID, req.TeamID, req.TeamRole)

	respondJSON(w, http.StatusCreated, user.ToResponse())
}

// enrollNewUser materializes a freshly-created user's team membership, per
// edition:
//
//   - Multi-tenant (Cloud): enroll ONLY in the team the admin explicitly chose
//     (teamID). No teamID → the user stays org-only, a valid state in the
//     org → team → user model. We deliberately do NOT fall back to a boot-time
//     default team here: h.defaultTeamID is a single UUID pinned at boot to the
//     operator org's "default" team, so enrolling against it cross-org both
//     leaked the wrong team and silently failed under RLS. teamRole "" inherits
//     the org role.
//   - OSS / single-tenant: every user is a member of the one default team;
//     teamID/teamRole are ignored (there is only one team, identified by name).
//
// Best effort: a membership write failure must not fail user creation. nil-
// guarded for the auth-disabled path where the team context isn't wired.
func (h *Handlers) enrollNewUser(ctx context.Context, userID, teamID string, teamRole Role) {
	if h.teams == nil {
		return
	}
	if MultiTenantEnabled {
		if teamID == "" {
			return // org-only — intentional, not a missing default
		}
		if _, err := h.teams.AddMember(ctx, teamID, userID, teamRole); err != nil {
			slog.Warn("could not enroll new user in selected team",
				slog.String("user_id", userID),
				slog.String("team_id", teamID),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	// OSS: the single default team. team_role "" — OSS teams never elevate.
	if h.defaultTeamID == "" {
		return
	}
	if _, err := h.teams.AddMember(ctx, h.defaultTeamID, userID, ""); err != nil {
		slog.Warn("could not enroll user in default team",
			slog.String("user_id", userID),
			slog.String("team_id", h.defaultTeamID),
			slog.String("error", err.Error()),
		)
	}
}

// --- Get user ---

// GetUser returns a single user by ID (admin only).
func (h *Handlers) GetUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user, err := h.store.GetUser(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	respondJSON(w, http.StatusOK, user.ToResponse())
}

// --- Update user ---

type updateUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     Role   `json:"role"`
}

// UpdateUser updates a user's profile and/or role (admin only).
func (h *Handlers) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Role != "" && !ValidRole(req.Role) {
		respondError(w, http.StatusBadRequest, "role must be admin, editor, or viewer")
		return
	}

	// Prevent demoting the last admin
	if req.Role != "" && req.Role != RoleAdmin {
		existing, err := h.store.GetUser(r.Context(), id)
		if err != nil {
			respondError(w, http.StatusNotFound, "user not found")
			return
		}
		if existing.Role == RoleAdmin {
			count, _ := h.store.CountByRole(r.Context(), RoleAdmin)
			if count <= 1 {
				respondError(w, http.StatusBadRequest, "cannot demote the last admin user")
				return
			}
		}
	}

	user, err := h.store.UpdateUser(r.Context(), id, req.Username, req.Email, req.Name, req.Role)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, user.ToResponse())
}

// --- Reset password (admin) ---

type resetPasswordRequest struct {
	Password string `json:"password"`
}

// ResetPassword sets a new password for a user (admin only, no current password required).
func (h *Handlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req resetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Password == "" {
		respondError(w, http.StatusBadRequest, "password is required")
		return
	}

	if len(req.Password) < 8 {
		respondError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	if err := h.store.UpdatePassword(r.Context(), id, req.Password); err != nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	// Invalidate all refresh tokens for this user
	h.store.DeleteUserRefreshTokens(r.Context(), id)

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Delete user ---

// DeleteUser removes a user (admin only). Cannot delete self or last admin.
func (h *Handlers) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Cannot delete self
	callerID := ContextUserID(r)
	if callerID == id {
		respondError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}

	// Cannot delete the last admin
	user, err := h.store.GetUser(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	if user.Role == RoleAdmin {
		count, _ := h.store.CountByRole(r.Context(), RoleAdmin)
		if count <= 1 {
			respondError(w, http.StatusBadRequest, "cannot delete the last admin user")
			return
		}
	}

	// Collect ALL the user's team memberships up front so we can clean every one
	// of them after deletion. A user may belong to many teams (multi-tenant) —
	// removing only the default team left orphan membership rows (a blank member
	// row in the team list) for any other team. Best effort + nil-guarded.
	var memberships []Membership
	if h.teams != nil {
		if ms, lerr := h.teams.ListUserTeams(r.Context(), id); lerr == nil {
			memberships = ms
		}
	}

	if err := h.store.DeleteUser(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	// Drop every team membership so no orphan lingers.
	for _, m := range memberships {
		if err := h.teams.RemoveMember(r.Context(), m.TeamID, id); err != nil {
			slog.Warn("could not remove deleted user's team membership",
				slog.String("user_id", id),
				slog.String("team_id", m.TeamID),
				slog.String("error", err.Error()),
			)
		}
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
