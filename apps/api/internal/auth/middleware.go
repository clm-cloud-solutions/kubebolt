package auth

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const claimsKey contextKey = "auth-claims"

// RequireAuth is a middleware that validates the JWT access token from the
// Authorization header and stores the claims in the request context.
func (h *Handlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.authEnabled {
			next.ServeHTTP(w, r)
			return
		}

		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}

		// Long-lived REST API token (kbs_/kbk_) — non-interactive callers
		// (e.g. Autopilot). Distinct from the user-session JWT below.
		if IsAPIToken(tokenStr) {
			claims, principal, err := h.validateAPIToken(r, tokenStr)
			if err != nil {
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), claimsKey, claims)
			ctx = context.WithValue(ctx, apiPrincipalKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		claims, err := h.jwt.ValidateAccessToken(tokenStr)
		if err != nil {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole returns a middleware that checks the authenticated user has at least
// the given minimum role. Must be used after RequireAuth.
func RequireRole(minRole Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := ContextRole(r)
			if RoleLevel(role) < RoleLevel(minRole) {
				http.Error(w, `{"error":"insufficient permissions"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ValidateWSToken validates a token from a WebSocket query parameter.
// Returns claims if valid, nil otherwise.
func (h *Handlers) ValidateWSToken(tokenStr string) *Claims {
	if !h.authEnabled || tokenStr == "" {
		return nil
	}
	claims, err := h.jwt.ValidateAccessToken(tokenStr)
	if err != nil {
		return nil
	}
	return claims
}

// ContextClaims returns the JWT claims from the request context, or nil if not present.
func ContextClaims(r *http.Request) *Claims {
	claims, _ := r.Context().Value(claimsKey).(*Claims)
	return claims
}

// ContextRole returns the role from the request context.
// When auth is disabled (no claims in context), returns RoleAdmin for full access.
func ContextRole(r *http.Request) Role {
	claims := ContextClaims(r)
	if claims == nil {
		return RoleAdmin
	}
	return claims.Role
}

// ContextUserID returns the user ID from the request context, or empty string if not present.
func ContextUserID(r *http.Request) string {
	claims := ContextClaims(r)
	if claims == nil {
		return ""
	}
	return claims.UserID
}

// RequirePlatformAdmin gates platform-tier surfaces — the isolated /platform
// portal AND the install-global settings (auth, ingest-channel, setup, service
// tokens) that no single ORG may modify. It is EDITION-AWARE:
//
//   - Multi-tenant (Cloud): only a token carrying the `plat` claim passes;
//     everyone else gets 404 — not 403 — so the surface is not even disclosed
//     to a customer org admin. This is the real security boundary: an org admin
//     must never toggle install-wide auth or agent ingest.
//   - OSS / EE self-hosted single-tenant: there is no separate platform
//     operator — the lone ADMIN is the platform admin. So the gate degrades to
//     "must be an org admin": RoleAdmin passes, lower roles 404. Auth-disabled
//     dev passes (ContextRole → RoleAdmin).
//
// The non-disclosure 404 (vs 403) is deliberate in both editions.
func RequirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsPlatformAdminRequest(r) {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// IsPlatformAdminRequest reports whether the request may act as a platform admin
// (see RequirePlatformAdmin for the edition rules). Exposed so handlers that mix
// platform and org fields in one payload (e.g. the General settings PUT) can
// gate per-field instead of per-route.
func IsPlatformAdminRequest(r *http.Request) bool {
	claims := ContextClaims(r)
	if MultiTenantEnabled {
		// Cloud: the explicit `plat` claim is required. nil claims means
		// auth-disabled (dev) — pass, mirroring ContextRole → RoleAdmin.
		return claims == nil || claims.Plat
	}
	// Single-tenant: the lone admin is the platform admin.
	return ContextRole(r) == RoleAdmin
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return parts[1]
}
