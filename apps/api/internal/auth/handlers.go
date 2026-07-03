package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Handlers provides HTTP handlers for authentication endpoints.
type Handlers struct {
	store       AuthStore
	jwt         *JWTService
	authEnabled bool
	cfg         config.AuthConfig
	// apiTokens is optional. When wired (SetAPITokenStore), RequireAuth
	// also accepts long-lived REST API tokens (kbs_/kbk_) in addition to
	// the user-session JWT. nil → only JWT auth. Interface-typed (W1 seam):
	// OSS wires the BoltDB *APITokenStore, EE a Postgres impl.
	apiTokens APITokenStorer

	// Org/team context for the OSS single-org+single-team hierarchy. Wired
	// via SetOrgTeamContext at boot (auth-enabled path only). When teams is
	// nil the membership lifecycle is skipped — every handler nil-guards, so
	// the auth-disabled / pre-W1 path behaves exactly as before.
	teams         TeamStore
	tenants       TenantStore
	defaultOrgID  string
	defaultTeamID string
}

// SetOrgTeamContext wires the default org + team so the auth handlers can keep
// every user enrolled in the default team (CreateUser/DeleteUser), surface the
// hierarchy in /auth/me, and serve the read-only /teams endpoints. Called once
// at boot from main.go after EnsureDefaultTeam. Optional — when unset the
// membership lifecycle and /teams endpoints degrade gracefully.
func (h *Handlers) SetOrgTeamContext(teams TeamStore, tenants TenantStore, defaultOrgID, defaultTeamID string) {
	h.teams = teams
	h.tenants = tenants
	h.defaultOrgID = defaultOrgID
	h.defaultTeamID = defaultTeamID
}

// SetAPITokenStore wires the REST API-token store so RequireAuth accepts
// kbs_/kbk_ bearer tokens. Optional; call once at boot from main.go.
func (h *Handlers) SetAPITokenStore(s APITokenStorer) {
	h.apiTokens = s
}

// APITokens exposes the wired store (nil if unset) for the admin handlers.
func (h *Handlers) APITokens() APITokenStorer {
	return h.apiTokens
}

// NewHandlers creates auth handlers with a store and JWT service. Takes the
// AuthStore interface (not *Store) so the EE build can inject a Postgres-backed
// user/refresh store via the newAuthStore seam without editing this file.
func NewHandlers(store AuthStore, jwt *JWTService, cfg config.AuthConfig) *Handlers {
	return &Handlers{
		store:       store,
		jwt:         jwt,
		authEnabled: cfg.Enabled,
		cfg:         cfg,
	}
}

// NewNoOpHandlers creates handlers for when auth is disabled.
// Only GetAuthConfig works; all other endpoints are unreachable (middleware skips).
func NewNoOpHandlers() *Handlers {
	return &Handlers{authEnabled: false}
}

// IsEnabled returns whether authentication is enabled.
func (h *Handlers) IsEnabled() bool {
	return h.authEnabled
}

// --- Auth config (public) ---

type authConfigResponse struct {
	Enabled bool `json:"enabled"`
	// SignupEnabled reports whether self-service org signup is reachable.
	// True only on the multi-org edition (EE/Cloud) with auth on — the
	// frontend gates the "Create an organization" link on this flag, so it
	// stays inert in OSS where signup returns 409 requires_ee.
	SignupEnabled bool `json:"signupEnabled"`
}

// GetAuthConfig returns whether auth is enabled (public endpoint).
func (h *Handlers) GetAuthConfig(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, authConfigResponse{
		Enabled:       h.authEnabled,
		SignupEnabled: MultiTenantEnabled && h.authEnabled && h.tenants != nil && h.teams != nil,
	})
}

// --- Login ---

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken string       `json:"accessToken"`
	User        UserResponse `json:"user"`
}

// Login authenticates a user and returns a JWT access token + refresh cookie.
// isSecureRequest reports whether the client reached us over HTTPS — either
// directly (r.TLS) or via a TLS-terminating proxy that sets X-Forwarded-Proto.
// The refresh cookie's Secure attribute mirrors this: it's protected on HTTPS
// deployments, but not forced on plain-HTTP self-hosted setups (localhost,
// kubectl port-forward) where a Secure cookie would be silently dropped by the
// browser and break login.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		respondError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	// The login identifier is either an email (global-unique, multi-org login
	// identity for Track D) or a username (RLS-scoped, within-org). An "@"
	// routes to the email path; everything else keeps the legacy username path
	// byte-for-byte identical to OSS.
	var user *User
	var err error
	if strings.Contains(req.Username, "@") {
		user, err = h.store.GetUserByEmail(r.Context(), req.Username)
	} else {
		user, err = h.store.GetUserByUsername(r.Context(), req.Username)
	}
	if err != nil || !CheckPassword(user, req.Password) {
		respondError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	accessToken, err := h.jwt.GenerateAccessToken(user)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// Generate and store refresh token
	rawRefresh, expiry, err := h.jwt.GenerateRefreshToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate refresh token")
		return
	}

	// Scope the post-login writes to the user's org so they pass RLS WITH CHECK
	// for non-default-org users (EE). In OSS User.OrgID is empty, so octx
	// resolves to the default tenant and behavior is unchanged.
	octx := WithTenantID(r.Context(), user.OrgID)

	rt := &RefreshToken{
		TokenHash: HashToken(rawRefresh),
		UserID:    user.ID,
		ExpiresAt: expiry,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.SaveRefreshToken(octx, rt); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store refresh token")
		return
	}

	// Set refresh token as httpOnly cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "kb_refresh",
		Value:    rawRefresh,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.cfg.RefreshTokenExpiry.Seconds()),
	})

	// Update last login
	h.store.UpdateLastLogin(octx, user.ID)

	respondJSON(w, http.StatusOK, loginResponse{
		AccessToken: accessToken,
		User:        user.ToResponse(),
	})
}

// --- Signup (public, multi-org self-service) ---

type signupRequest struct {
	OrgName  string `json:"orgName"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Signup provisions a brand-new organization end to end (tenant + default team
// + admin user) and logs the new admin in, returning a JWT + refresh cookie
// exactly like Login. Public route — self-service signup is the entry point for
// the multi-org (EE / Track D) edition. Gated 409 requires_ee in OSS, where
// signup is meaningless (single auto-seeded org).
func (h *Handlers) Signup(w http.ResponseWriter, r *http.Request) {
	if !MultiTenantEnabled {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "self-service signup requires the multi-org edition",
			"code":  "requires_ee",
		})
		return
	}
	if h.tenants == nil || h.teams == nil {
		respondError(w, http.StatusServiceUnavailable, "signup is not available")
		return
	}

	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.OrgName = strings.TrimSpace(req.OrgName)
	req.Email = strings.TrimSpace(req.Email)
	req.Name = strings.TrimSpace(req.Name)
	if req.OrgName == "" {
		respondError(w, http.StatusBadRequest, "organization name is required")
		return
	}
	if !strings.Contains(req.Email, "@") {
		respondError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if len(req.Password) < 8 {
		respondError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	// Email doubles as the username (the global-unique login identity).
	org, admin, err := BootstrapOrg(r.Context(), h.tenants, h.teams, h.store,
		req.OrgName, req.Email, req.Email, req.Name, req.Password)
	if err != nil {
		if isConflictErr(err) {
			respondError(w, http.StatusConflict, "an organization or account with that name or email already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, "failed to create organization")
		return
	}

	accessToken, err := h.jwt.GenerateAccessToken(admin)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	rawRefresh, expiry, err := h.jwt.GenerateRefreshToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate refresh token")
		return
	}

	// Scope the refresh-token + last-login writes to the freshly created org so
	// they pass RLS WITH CHECK.
	octx := WithTenantID(r.Context(), org.ID)

	rt := &RefreshToken{
		TokenHash: HashToken(rawRefresh),
		UserID:    admin.ID,
		ExpiresAt: expiry,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.SaveRefreshToken(octx, rt); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store refresh token")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "kb_refresh",
		Value:    rawRefresh,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.cfg.RefreshTokenExpiry.Seconds()),
	})

	h.store.UpdateLastLogin(octx, admin.ID)

	respondJSON(w, http.StatusCreated, loginResponse{
		AccessToken: accessToken,
		User:        admin.ToResponse(),
	})
}

// isConflictErr reports whether a BootstrapOrg failure was a uniqueness
// collision (duplicate org name, username/email) rather than an internal fault,
// so Signup can map it to 409 instead of 500. Matches on the sentinel
// ErrTenantExists and the substring the Bolt/Postgres user stores emit for a
// duplicate identity.
func isConflictErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTenantExists) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "unique constraint")
}

// --- Refresh ---

type refreshResponse struct {
	AccessToken string `json:"accessToken"`
}

// Refresh exchanges a valid refresh token cookie for a new access token.
// Implements refresh token rotation (old token deleted, new one issued).
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("kb_refresh")
	if err != nil || cookie.Value == "" {
		respondError(w, http.StatusUnauthorized, "no refresh token")
		return
	}

	tokenHash := HashToken(cookie.Value)
	rt, err := h.store.GetRefreshToken(r.Context(), tokenHash)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	// Refresh is a PUBLIC route: the access token (and its org claim) is gone, so
	// the request carries no tenant. The refresh token resolves the owning user's
	// org (OrgID, derived at lookup), and every follow-up store call is scoped to
	// it so the RLS-protected GetUser + rotation writes target the right tenant.
	// In OSS rt.OrgID is "" and the single-tenant stores ignore it — unchanged.
	octx := WithTenantID(r.Context(), rt.OrgID)

	if time.Now().After(rt.ExpiresAt) {
		h.store.DeleteRefreshToken(octx, tokenHash)
		respondError(w, http.StatusUnauthorized, "refresh token expired")
		return
	}

	// Rotate: delete old token
	h.store.DeleteRefreshToken(octx, tokenHash)

	user, err := h.store.GetUser(octx, rt.UserID)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "user not found")
		return
	}

	accessToken, err := h.jwt.GenerateAccessToken(user)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// Issue new refresh token
	rawRefresh, expiry, err := h.jwt.GenerateRefreshToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate refresh token")
		return
	}

	newRT := &RefreshToken{
		TokenHash: HashToken(rawRefresh),
		UserID:    user.ID,
		ExpiresAt: expiry,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.SaveRefreshToken(octx, newRT); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store refresh token")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "kb_refresh",
		Value:    rawRefresh,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.cfg.RefreshTokenExpiry.Seconds()),
	})

	respondJSON(w, http.StatusOK, refreshResponse{AccessToken: accessToken})
}

// --- Logout ---

// Logout invalidates the refresh token and clears the cookie.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("kb_refresh"); err == nil && cookie.Value != "" {
		h.store.DeleteRefreshToken(r.Context(), HashToken(cookie.Value))
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "kb_refresh",
		Value:    "",
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Me ---

// GetMe returns the current authenticated user's profile.
func (h *Handlers) GetMe(w http.ResponseWriter, r *http.Request) {
	claims := ContextClaims(r)
	if claims == nil {
		respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	user, err := h.store.GetUser(r.Context(), claims.UserID)
	if err != nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	respondJSON(w, http.StatusOK, meResponse{
		UserResponse:    user.ToResponse(),
		Org:             h.orgBriefFor(user),
		Team:            h.teamBriefFor(r.Context(), user),
		IsPlatformAdmin: IsPlatformAdminRequest(r),
	})
}

// meResponse extends the user profile with the org + team the user belongs to,
// so the frontend can render the org → team → user hierarchy (and the upgrade
// CTA for OSS). Org/Team are nil when the org/team context isn't wired
// (auth-disabled installs) — the frontend treats their absence as "single
// implicit org/team", identical to pre-hierarchy behavior.
type meResponse struct {
	UserResponse
	Org  *orgBrief  `json:"org,omitempty"`
	Team *teamBrief `json:"team,omitempty"`
	// IsPlatformAdmin lets the frontend show the /platform portal entry and the
	// install-global setting tabs ONLY to a platform operator. Edition-aware:
	// true for the lone admin in OSS/single-tenant, for a `plat`-claim token in
	// Cloud, and for auth-disabled dev. Cosmetic only — the backend enforces.
	IsPlatformAdmin bool `json:"isPlatformAdmin"`
}

// UserTeamIDs returns the set of team IDs the user is a member of — the API
// layer uses it to scope cluster visibility by team (Track D). Returns an empty
// set (never nil) when the team store isn't wired or the user has no
// memberships, so callers can range/lookup without a nil check.
func (h *Handlers) UserTeamIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if h.teams == nil || userID == "" {
		return out, nil
	}
	ms, err := h.teams.ListUserTeams(ctx, userID)
	if err != nil {
		return out, err
	}
	for _, m := range ms {
		out[m.TeamID] = struct{}{}
	}
	return out, nil
}

// TeamBelongsToOrg reports whether teamID exists and belongs to orgID. Used to
// validate a team selection (e.g. assigning a cluster to a team) before acting.
// RLS already scopes GetTeam to the caller's org; the explicit OrgID check is
// belt-and-suspenders.
func (h *Handlers) TeamBelongsToOrg(ctx context.Context, orgID, teamID string) bool {
	if h.teams == nil || teamID == "" {
		return false
	}
	team, err := h.teams.GetTeam(ctx, teamID)
	return err == nil && team.OrgID == orgID
}

// GetMyTeams returns every team the caller belongs to, with their effective role
// in each — the data the topbar team switcher renders (Track D §2.3, active
// team). Returns an empty list when the team store isn't wired (auth-disabled).
func (h *Handlers) GetMyTeams(w http.ResponseWriter, r *http.Request) {
	claims := ContextClaims(r)
	if claims == nil {
		respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if h.teams == nil {
		slog.Warn("GetMyTeams diag: h.teams is nil (SetOrgTeamContext not wired) — returning empty")
		respondJSON(w, http.StatusOK, []teamBrief{})
		return
	}
	user, err := h.store.GetUser(r.Context(), claims.UserID)
	if err != nil {
		slog.Warn("GetMyTeams diag: GetUser failed",
			slog.String("user_id", claims.UserID),
			slog.String("resolved_org", TenantIDFromContext(r.Context())),
			slog.String("error", err.Error()))
		respondError(w, http.StatusNotFound, "user not found")
		return
	}
	memberships, err := h.teams.ListUserTeams(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load teams")
		return
	}
	out := make([]teamBrief, 0, len(memberships))
	for _, m := range memberships {
		team, err := h.teams.GetTeam(r.Context(), m.TeamID)
		if err != nil {
			continue // membership whose team was deleted out from under us
		}
		out = append(out, teamBrief{ID: team.ID, Name: team.Name, Role: EffectiveRole(user.Role, m.TeamRole)})
	}
	respondJSON(w, http.StatusOK, out)
}

type orgBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type teamBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Role is the user's EFFECTIVE role in the team — max(org_role, team_role).
	// In OSS team_role is always "", so this equals the org role.
	Role Role `json:"role"`
}

// orgBriefFor resolves the user's organization. In OSS this is always the
// single default tenant. Returns nil when the tenant context isn't wired.
func (h *Handlers) orgBriefFor(u *User) *orgBrief {
	if h.tenants == nil {
		return nil
	}
	// The user's OWN org. Single-tenant OSS: their OrgID is the default tenant;
	// multi-tenant: it MUST be the user's org, never a global default — otherwise
	// every user sees the operator/default org in /auth/me.
	orgID := u.OrgID
	if orgID == "" {
		orgID = h.defaultOrgID // OSS / users created before org_id was stamped
	}
	if orgID == "" {
		return nil
	}
	t, err := h.tenants.GetTenant(orgID)
	if err != nil {
		return nil
	}
	return &orgBrief{ID: t.ID, Name: t.Name}
}

// teamBriefFor resolves the user's default-team membership and effective role.
// Returns nil when the team context isn't wired or the user has no membership
// (shouldn't happen in OSS — the lifecycle keeps everyone enrolled — but we
// degrade gracefully rather than fabricate a team).
func (h *Handlers) teamBriefFor(ctx context.Context, u *User) *teamBrief {
	if h.teams == nil {
		return nil
	}
	// The user's OWN team — their first membership. Single-tenant: the default
	// team; multi-tenant: their org's team, never another org's global default.
	if ms, err := h.teams.ListUserTeams(ctx, u.ID); err == nil && len(ms) > 0 {
		if team, err := h.teams.GetTeam(ctx, ms[0].TeamID); err == nil {
			return &teamBrief{ID: team.ID, Name: team.Name, Role: EffectiveRole(u.Role, ms[0].TeamRole)}
		}
	}
	// Multi-tenant: no membership = genuinely org-only (created without a team,
	// or removed from all of them). Do NOT fabricate the boot-time default team
	// — that UUID is pinned to the operator org's "default" team and would
	// misrepresent the user's access (they'd appear to belong to a team they
	// are not a member of). Org-only is a valid state in the model.
	if MultiTenantEnabled {
		return nil
	}
	// OSS fallback: the single configured default team (lifecycle keeps every
	// user enrolled; this is the pre-membership / defensive path).
	if h.defaultTeamID == "" {
		return nil
	}
	team, err := h.teams.GetTeam(ctx, h.defaultTeamID)
	if err != nil {
		return nil
	}
	effective := u.Role
	if m, ok, _ := h.teams.GetMembership(ctx, h.defaultTeamID, u.ID); ok {
		effective = EffectiveRole(u.Role, m.TeamRole)
	}
	return &teamBrief{ID: team.ID, Name: team.Name, Role: effective}
}

// --- Change own password ---

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePassword allows the authenticated user to change their own password.
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := ContextClaims(r)
	if claims == nil {
		respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.CurrentPassword == "" || req.NewPassword == "" {
		respondError(w, http.StatusBadRequest, "current and new password are required")
		return
	}

	if len(req.NewPassword) < 8 {
		respondError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}

	user, err := h.store.GetUser(r.Context(), claims.UserID)
	if err != nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	if !CheckPassword(user, req.CurrentPassword) {
		respondError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	if err := h.store.UpdatePassword(r.Context(), user.ID, req.NewPassword); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Response helpers ---

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, msg string) {
	respondJSON(w, status, map[string]string{"error": msg})
}
