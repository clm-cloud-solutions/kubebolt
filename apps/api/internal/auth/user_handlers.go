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

// --- Create user ---

type createUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Role     Role   `json:"role"`
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

	user, err := h.store.CreateUser(r.Context(), req.Username, req.Email, req.Name, req.Password, req.Role)
	if err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}

	// Enroll the new user in the default team so the org → team → user
	// hierarchy stays materialized (every user is a real member, not just an
	// implicit org-role holder). team_role "" = inherit the org role. Best
	// effort: a membership write failure must not fail user creation — the
	// boot backfill re-ensures it on next start. nil-guarded for the
	// auth-disabled path where the team context isn't wired.
	h.enrollInDefaultTeam(r.Context(), user.ID)

	respondJSON(w, http.StatusCreated, user.ToResponse())
}

// enrollInDefaultTeam adds a user to the OSS default team (idempotent). No-op
// when the team context isn't wired. team_role is "" — the user's access is
// their org role; OSS teams never elevate.
func (h *Handlers) enrollInDefaultTeam(ctx context.Context, userID string) {
	if h.teams == nil || h.defaultTeamID == "" {
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

	if err := h.store.DeleteUser(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	// Drop the user's default-team membership so no orphan membership lingers.
	// Best effort + nil-guarded (auth-disabled path).
	if h.teams != nil && h.defaultTeamID != "" {
		if err := h.teams.RemoveMember(r.Context(), h.defaultTeamID, id); err != nil {
			slog.Warn("could not remove deleted user's team membership",
				slog.String("user_id", id),
				slog.String("team_id", h.defaultTeamID),
				slog.String("error", err.Error()),
			)
		}
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
