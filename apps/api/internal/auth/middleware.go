package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
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

// OptionalAuth is a best-effort variant of RequireAuth for PUBLIC routes that
// still want to honor the caller's identity WHEN a valid token is present but
// must stay reachable without one. It validates the Authorization bearer token
// exactly like RequireAuth (user-session JWT or REST API token) and, on
// success, stashes the claims (and API principal) in the request context — but
// a missing, malformed, or invalid token is NOT an error: the request simply
// continues unauthenticated (nil claims), so the public path keeps working
// (e.g. the login page fetching /copilot/config before a session exists).
//
// Mount it BEFORE ResolveTenant on a public route so the resolver can read the
// JWT tenant claim and stamp the caller's real org. Without it, a public
// handler that resolves per-org config (e.g. the Copilot model shown in the
// chat-panel title) silently falls back to the env baseline for every caller,
// because no claims are ever established on the public path.
//
// Security: this never GRANTS access — it only reads identity. An invalid
// token is ignored, never trusted; downstream gating (RequireRole, etc.) is
// unaffected. Use it only on routes that are already safe to serve anonymously.
//
// When auth is disabled it is a pass-through, identical to RequireAuth.
func (h *Handlers) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.authEnabled {
			next.ServeHTTP(w, r)
			return
		}

		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			next.ServeHTTP(w, r) // anonymous — public route stays reachable
			return
		}

		// Long-lived REST API token (kbs_/kbk_). On failure, fall through as
		// anonymous rather than 401 — best-effort is the whole point.
		if IsAPIToken(tokenStr) {
			claims, principal, err := h.validateAPIToken(r, tokenStr)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			ctx := context.WithValue(r.Context(), claimsKey, claims)
			ctx = context.WithValue(ctx, apiPrincipalKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// User-session JWT. Invalid/expired → continue unauthenticated.
		claims, err := h.jwt.ValidateAccessToken(tokenStr)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey, claims)))
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

// PortForwardCookieExpiry is how long the kb_pf cookie authorizing /pf/{id}/
// access stays valid.
const PortForwardCookieExpiry = 8 * time.Hour

// IssuePortForwardToken mints the kb_pf cookie value binding a port-forward's
// owning org. Returns "" when auth is disabled (the proxy then skips the check).
func (h *Handlers) IssuePortForwardToken(orgID string) string {
	if !h.authEnabled {
		return ""
	}
	tok, err := h.jwt.GeneratePortForwardToken(orgID, time.Now().Add(PortForwardCookieExpiry))
	if err != nil {
		return ""
	}
	return tok
}

// ValidatePortForwardToken checks a kb_pf cookie value and returns the org it
// authorizes. ok is false when auth is disabled, the value is empty, or the token
// is invalid/expired/wrong-purpose.
func (h *Handlers) ValidatePortForwardToken(tokenStr string) (orgID string, ok bool) {
	if !h.authEnabled || tokenStr == "" {
		return "", false
	}
	orgID, err := h.jwt.ValidatePortForwardToken(tokenStr)
	if err != nil {
		return "", false
	}
	return orgID, true
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
