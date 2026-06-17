// Package auth — team_handlers.go exposes the read-only /teams surface that
// lets the frontend render the org → team → user hierarchy.
//
// OSS is single-org + single-team: these endpoints serve the one auto-seeded
// "default" team and its members. Creating additional teams is gated behind the
// MultiTenantEnabled seam (edition.go) — POST /teams returns 409 + code
// "requires_ee" so the UI can show the upgrade CTA. The read endpoints are open
// to any authenticated user (it's their own team); the member list is
// admin-only, matching the sensitivity of /users.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// teamSummary is the list/detail shape for a team.
type teamSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	OrgID       string `json:"orgId"`
	MemberCount int    `json:"memberCount"`
}

// teamMemberView joins a membership with the user record so the UI can show who
// is on the team and their effective role, without a second round-trip.
type teamMemberView struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Name     string `json:"name"`
	// Role is the EFFECTIVE role in the team — max(org_role, team_role).
	Role Role `json:"role"`
	// TeamRole is the raw team-level elevation ("" in OSS — inherit org role).
	TeamRole Role `json:"teamRole,omitempty"`
}

// callerOrgID resolves the requesting org for tenant-scoped reads. It prefers
// the org stamped by ResolveTenant (the JWT tid → the real org UUID in EE); when
// that's absent or the default-tenant sentinel NAME (unauth / OSS / single
// tenant), it falls back to the configured default org UUID, so those paths keep
// resolving exactly as before (their rows carry the default tenant's UUID).
func (h *Handlers) callerOrgID(r *http.Request) string {
	if org := ContextTenantID(r); org != "" && org != DefaultTenantName {
		return org
	}
	return h.defaultOrgID
}

// canManageTeam reports whether the caller may manage teamID. Org admins manage
// any team in their org; a team's OWN admin (team_role == admin) manages that
// team — members + rename. The team-scoped routes carry no blanket org-admin
// gate, so every team-scoped handler calls this. (Creating / deleting teams
// stays org-admin only — gated at the route.)
func (h *Handlers) canManageTeam(r *http.Request, teamID string) bool {
	if ContextRole(r) == RoleAdmin {
		return true
	}
	if h.teams == nil || teamID == "" {
		return false
	}
	uid := ContextUserID(r)
	if uid == "" {
		return false
	}
	m, ok, err := h.teams.GetMembership(r.Context(), teamID, uid)
	allowed := err == nil && ok && m.TeamRole == RoleAdmin
	if !allowed {
		// Diagnostic sentinel: a team-admin who should manage their team but is
		// denied. Captures every input so the cause (wrong role, missing
		// membership, RLS/org-context, lookup error) is obvious in the log.
		teamRole := ""
		if m != nil {
			teamRole = string(m.TeamRole)
		}
		slog.Warn("canManageTeam denied",
			slog.String("team_id", teamID),
			slog.String("user_id", uid),
			slog.String("ctx_role", string(ContextRole(r))),
			slog.String("org", TenantIDFromContext(r.Context())),
			slog.Bool("membership_found", ok),
			slog.String("team_role", teamRole),
			slog.Any("err", err),
		)
	}
	return allowed
}

// ListTeams returns the teams in the caller's organization. OSS: the single
// default team.
func (h *Handlers) ListTeams(w http.ResponseWriter, r *http.Request) {
	if h.teams == nil {
		respondJSON(w, http.StatusOK, []teamSummary{})
		return
	}
	teams, err := h.teams.ListTeams(r.Context(), h.callerOrgID(r))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]teamSummary, 0, len(teams))
	for i := range teams {
		out = append(out, h.summarizeTeam(r.Context(), &teams[i]))
	}
	respondJSON(w, http.StatusOK, out)
}

// GetTeam returns one team by ID.
func (h *Handlers) GetTeam(w http.ResponseWriter, r *http.Request) {
	if h.teams == nil {
		respondError(w, http.StatusNotFound, "team not found")
		return
	}
	team, err := h.teams.GetTeam(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "team not found")
		return
	}
	respondJSON(w, http.StatusOK, h.summarizeTeam(r.Context(), team))
}

// ListTeamMembers returns the team's members joined with their user record and
// effective role. Admin-only (wired in the router).
func (h *Handlers) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	if h.teams == nil {
		respondJSON(w, http.StatusOK, []teamMemberView{})
		return
	}
	teamID := chi.URLParam(r, "id")
	if !h.canManageTeam(r, teamID) {
		respondError(w, http.StatusForbidden, "you do not manage this team")
		return
	}
	members, err := h.teams.ListMembers(r.Context(), teamID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]teamMemberView, 0, len(members))
	for i := range members {
		m := &members[i]
		v := teamMemberView{UserID: m.UserID, TeamRole: m.TeamRole}
		// Join the user record for display + effective-role resolution. Skip
		// memberships whose user was deleted out from under us (defensive).
		if u, err := h.store.GetUser(r.Context(), m.UserID); err == nil {
			v.Username = u.Username
			v.Name = u.Name
			v.Role = EffectiveRole(u.Role, m.TeamRole)
		} else {
			v.Role = m.TeamRole
		}
		out = append(out, v)
	}
	respondJSON(w, http.StatusOK, out)
}

// requiresMultiTenant writes the 409 requires_ee guardrail and reports whether
// the request should stop. Additional-team management (create/rename/delete/
// membership) is a multi-tenant (Cloud) capability; OSS / EE Self-Hosted operate
// the single default team only.
func (h *Handlers) requiresMultiTenant(w http.ResponseWriter) bool {
	if !MultiTenantEnabled {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "managing teams requires KubeBolt Cloud",
			"code":  ErrCodeRequiresEE,
		})
		return true
	}
	if h.teams == nil {
		respondError(w, http.StatusServiceUnavailable, "team store unavailable")
		return true
	}
	return false
}

type teamNameRequest struct {
	Name string `json:"name"`
}

// CreateTeam creates a team in the caller's org and enrolls the creator as a
// team admin, so it immediately appears in their team switcher.
func (h *Handlers) CreateTeam(w http.ResponseWriter, r *http.Request) {
	if h.requiresMultiTenant(w) {
		return
	}
	var req teamNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "team name is required")
		return
	}
	team, err := h.teams.CreateTeam(r.Context(), h.callerOrgID(r), name)
	if err != nil {
		if errors.Is(err, ErrTeamExists) {
			respondError(w, http.StatusConflict, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Enroll the creator (team admin) so the team shows up in their switcher.
	if uid := ContextUserID(r); uid != "" {
		if _, err := h.teams.AddMember(r.Context(), team.ID, uid, RoleAdmin); err != nil {
			slog.Warn("CreateTeam: could not enroll creator", slog.String("team_id", team.ID), slog.String("error", err.Error()))
		}
	}
	respondJSON(w, http.StatusCreated, h.summarizeTeam(r.Context(), team))
}

// UpdateTeam renames a team. Cross-org access is blocked by RLS (a team outside
// the caller's org resolves to "not found").
func (h *Handlers) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	if h.requiresMultiTenant(w) {
		return
	}
	if !h.canManageTeam(r, chi.URLParam(r, "id")) {
		respondError(w, http.StatusForbidden, "you do not manage this team")
		return
	}
	var req teamNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "team name is required")
		return
	}
	team, err := h.teams.UpdateTeam(r.Context(), chi.URLParam(r, "id"), func(t *Team) error {
		t.Name = name
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrTeamNotFound):
			respondError(w, http.StatusNotFound, "team not found")
		case errors.Is(err, ErrTeamExists):
			respondError(w, http.StatusConflict, err.Error())
		default:
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, h.summarizeTeam(r.Context(), team))
}

// DeleteTeam removes a team. The org's default team is protected (every org
// keeps at least it). Memberships cascade.
func (h *Handlers) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	if h.requiresMultiTenant(w) {
		return
	}
	id := chi.URLParam(r, "id")
	team, err := h.teams.GetTeam(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "team not found")
		return
	}
	if team.Name == DefaultTeamName {
		respondError(w, http.StatusConflict, "the default team cannot be deleted")
		return
	}
	if err := h.teams.DeleteTeam(r.Context(), id); err != nil {
		if errors.Is(err, ErrTeamNotFound) {
			respondError(w, http.StatusNotFound, "team not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type addMemberRequest struct {
	UserID string `json:"userId"`
	// Role is the optional team-level elevation. "" = inherit the org role.
	Role Role `json:"role,omitempty"`
}

// AddTeamMember adds (or updates the team role of) a user in a team.
func (h *Handlers) AddTeamMember(w http.ResponseWriter, r *http.Request) {
	if h.requiresMultiTenant(w) {
		return
	}
	if !h.canManageTeam(r, chi.URLParam(r, "id")) {
		respondError(w, http.StatusForbidden, "you do not manage this team")
		return
	}
	var req addMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == "" {
		respondError(w, http.StatusBadRequest, "userId is required")
		return
	}
	if req.Role != "" && !ValidRole(req.Role) {
		respondError(w, http.StatusBadRequest, "invalid role")
		return
	}
	if _, err := h.teams.AddMember(r.Context(), chi.URLParam(r, "id"), req.UserID, req.Role); err != nil {
		if errors.Is(err, ErrTeamNotFound) {
			respondError(w, http.StatusNotFound, "team not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveTeamMember removes a user from a team.
func (h *Handlers) RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	if h.requiresMultiTenant(w) {
		return
	}
	if !h.canManageTeam(r, chi.URLParam(r, "id")) {
		respondError(w, http.StatusForbidden, "you do not manage this team")
		return
	}
	if err := h.teams.RemoveMember(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "userId")); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) summarizeTeam(ctx context.Context, t *Team) teamSummary {
	count := 0
	if members, err := h.teams.ListMembers(ctx, t.ID); err == nil {
		count = len(members)
	}
	return teamSummary{ID: t.ID, Name: t.Name, OrgID: t.OrgID, MemberCount: count}
}
