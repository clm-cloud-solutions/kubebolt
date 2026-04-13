package auth

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Handlers provides HTTP handlers for authentication endpoints.
type Handlers struct {
	store       *Store
	jwt         *JWTService
	authEnabled bool
	cfg         config.AuthConfig
}

// NewHandlers creates auth handlers with a store and JWT service.
func NewHandlers(store *Store, jwt *JWTService, cfg config.AuthConfig) *Handlers {
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
}

// GetAuthConfig returns whether auth is enabled (public endpoint).
func (h *Handlers) GetAuthConfig(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, authConfigResponse{Enabled: h.authEnabled})
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

	user, err := h.store.GetUserByUsername(req.Username)
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

	rt := &RefreshToken{
		TokenHash: HashToken(rawRefresh),
		UserID:    user.ID,
		ExpiresAt: expiry,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.SaveRefreshToken(rt); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store refresh token")
		return
	}

	// Set refresh token as httpOnly cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "kb_refresh",
		Value:    rawRefresh,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.cfg.RefreshTokenExpiry.Seconds()),
	})

	// Update last login
	h.store.UpdateLastLogin(user.ID)

	respondJSON(w, http.StatusOK, loginResponse{
		AccessToken: accessToken,
		User:        user.ToResponse(),
	})
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
	rt, err := h.store.GetRefreshToken(tokenHash)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	if time.Now().After(rt.ExpiresAt) {
		h.store.DeleteRefreshToken(tokenHash)
		respondError(w, http.StatusUnauthorized, "refresh token expired")
		return
	}

	// Rotate: delete old token
	h.store.DeleteRefreshToken(tokenHash)

	user, err := h.store.GetUser(rt.UserID)
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
	if err := h.store.SaveRefreshToken(newRT); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to store refresh token")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "kb_refresh",
		Value:    rawRefresh,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.cfg.RefreshTokenExpiry.Seconds()),
	})

	respondJSON(w, http.StatusOK, refreshResponse{AccessToken: accessToken})
}

// --- Logout ---

// Logout invalidates the refresh token and clears the cookie.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("kb_refresh"); err == nil && cookie.Value != "" {
		h.store.DeleteRefreshToken(HashToken(cookie.Value))
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "kb_refresh",
		Value:    "",
		Path:     "/api/v1/auth",
		HttpOnly: true,
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

	user, err := h.store.GetUser(claims.UserID)
	if err != nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	respondJSON(w, http.StatusOK, user.ToResponse())
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

	user, err := h.store.GetUser(claims.UserID)
	if err != nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	if !CheckPassword(user, req.CurrentPassword) {
		respondError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	if err := h.store.UpdatePassword(user.ID, req.NewPassword); err != nil {
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
