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
	"net/http"

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

// ListTeams returns the teams in the caller's organization. OSS: the single
// default team.
func (h *Handlers) ListTeams(w http.ResponseWriter, r *http.Request) {
	if h.teams == nil {
		respondJSON(w, http.StatusOK, []teamSummary{})
		return
	}
	teams, err := h.teams.ListTeams(h.defaultOrgID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]teamSummary, 0, len(teams))
	for i := range teams {
		out = append(out, h.summarizeTeam(&teams[i]))
	}
	respondJSON(w, http.StatusOK, out)
}

// GetTeam returns one team by ID.
func (h *Handlers) GetTeam(w http.ResponseWriter, r *http.Request) {
	if h.teams == nil {
		respondError(w, http.StatusNotFound, "team not found")
		return
	}
	team, err := h.teams.GetTeam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "team not found")
		return
	}
	respondJSON(w, http.StatusOK, h.summarizeTeam(team))
}

// ListTeamMembers returns the team's members joined with their user record and
// effective role. Admin-only (wired in the router).
func (h *Handlers) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	if h.teams == nil {
		respondJSON(w, http.StatusOK, []teamMemberView{})
		return
	}
	teamID := chi.URLParam(r, "id")
	members, err := h.teams.ListMembers(teamID)
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
		if u, err := h.store.GetUser(m.UserID); err == nil {
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

// CreateTeam is the OSS guardrail: additional teams are an EE/SaaS capability.
func (h *Handlers) CreateTeam(w http.ResponseWriter, r *http.Request) {
	if !MultiTenantEnabled {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "creating additional teams requires KubeBolt SaaS or Enterprise",
			"code":  ErrCodeRequiresEE,
		})
		return
	}
	// EE provides the real implementation behind the seam; OSS never reaches
	// here. Returning 501 keeps the contract honest if the seam is flipped
	// without an EE handler wired.
	respondError(w, http.StatusNotImplemented, "team creation not wired")
}

func (h *Handlers) summarizeTeam(t *Team) teamSummary {
	count := 0
	if members, err := h.teams.ListMembers(t.ID); err == nil {
		count = len(members)
	}
	return teamSummary{ID: t.ID, Name: t.Name, OrgID: t.OrgID, MemberCount: count}
}
