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
