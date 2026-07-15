package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Claims represents the JWT claims for an access token.
type Claims struct {
	jwt.RegisteredClaims
	UserID   string `json:"uid"`
	Username string `json:"usr"`
	Role     Role   `json:"role"`
	// TenantID scopes the token to an organization/tenant. Empty in OSS
	// (single-tenant: callers resolve it to DefaultTenantName). The EE/SaaS
	// edition populates it at login for multi-tenant deployments. Read it
	// via ContextTenantID; override resolution via TenantResolver.
	TenantID string `json:"tid,omitempty"`
	// Plat marks a platform administrator (cloud/SaaS operator). It gates the
	// isolated /platform portal via RequirePlatformAdmin. Set at token-issue
	// time when the user's email is in KUBEBOLT_PLATFORM_ADMINS. Omitted
	// (false) for every normal org/team user and in OSS.
	Plat bool `json:"plat,omitempty"`
}

// JWTService handles JWT token generation and validation.
type JWTService struct {
	secret        []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
	// platformAdmins is the lowercased email set whose tokens get the `plat`
	// claim. nil/empty in OSS / self-hosted (no platform tier).
	platformAdmins map[string]bool
}

// NewJWTService creates a new JWT service from auth config.
func NewJWTService(cfg config.AuthConfig) *JWTService {
	var plat map[string]bool
	if len(cfg.PlatformAdmins) > 0 {
		plat = make(map[string]bool, len(cfg.PlatformAdmins))
		for _, e := range cfg.PlatformAdmins {
			plat[strings.ToLower(strings.TrimSpace(e))] = true
		}
	}
	return &JWTService{
		secret:         cfg.JWTSecret,
		accessExpiry:   cfg.AccessTokenExpiry,
		refreshExpiry:  cfg.RefreshTokenExpiry,
		platformAdmins: plat,
	}
}

// IsPlatformAdmin reports whether the given email is a configured platform
// administrator. Case-insensitive; false when none are configured.
func (j *JWTService) IsPlatformAdmin(email string) bool {
	if j.platformAdmins == nil || email == "" {
		return false
	}
	return j.platformAdmins[strings.ToLower(strings.TrimSpace(email))]
}

// GenerateAccessToken creates a signed JWT access token for a user.
func (j *JWTService) GenerateAccessToken(user *User) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(j.accessExpiry)),
			Issuer:    "kubebolt",
		},
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		// TenantID stamps the user's org into the token so downstream
		// (TenantResolver → ContextTenantID → cluster.RuntimeKey →
		// Manager.resolveRuntime) routes per org. Empty in OSS (User.OrgID
		// is never set → omitempty drops the claim → resolves to
		// DefaultTenantName), so OSS tokens are byte-identical to pre-seam.
		TenantID: user.OrgID,
		// Stamp the platform-admin capability so the isolated /platform portal
		// (RequirePlatformAdmin) can gate purely off the token.
		Plat: j.IsPlatformAdmin(user.Email),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

// GenerateRefreshToken creates a random refresh token string and its expiry time.
func (j *JWTService) GenerateRefreshToken() (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("generate random bytes: %w", err)
	}
	token := hex.EncodeToString(b)
	expiry := time.Now().Add(j.refreshExpiry)
	return token, expiry, nil
}

// ValidateAccessToken parses and validates a JWT access token string.
func (j *JWTService) ValidateAccessToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// HashToken returns the SHA-256 hex digest of a token string.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// pfTokenPurpose tags a port-forward cookie JWT so neither an access token nor a
// verification/reset link can be replayed as port-forward access (and vice
// versa) — token confusion is rejected on the purpose check.
const pfTokenPurpose = "port_forward"

// PortForwardClaims is the claim set for the kb_pf cookie that authorizes access
// to the /pf/{id}/ reverse proxy. It carries only the owning org so the proxy can
// enforce tenant isolation on a request that (being a top-level navigation) can't
// send an Authorization header.
type PortForwardClaims struct {
	jwt.RegisteredClaims
	OrgID   string `json:"tid,omitempty"`
	Purpose string `json:"pur"`
}

// GeneratePortForwardToken mints the signed value for a kb_pf cookie, binding a
// port-forward session's owning org.
func (j *JWTService) GeneratePortForwardToken(orgID string, expiresAt time.Time) (string, error) {
	claims := PortForwardClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Issuer:    "kubebolt",
		},
		OrgID:   orgID,
		Purpose: pfTokenPurpose,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

// ValidatePortForwardToken parses a kb_pf cookie and returns the org it
// authorizes. Rejects expired tokens, bad signatures, and any token whose purpose
// isn't port-forward access.
func (j *JWTService) ValidatePortForwardToken(tokenStr string) (orgID string, err error) {
	token, err := jwt.ParseWithClaims(tokenStr, &PortForwardClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return "", fmt.Errorf("invalid port-forward token: %w", err)
	}
	claims, ok := token.Claims.(*PortForwardClaims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("invalid port-forward token claims")
	}
	if claims.Purpose != pfTokenPurpose {
		return "", fmt.Errorf("token is not a port-forward token")
	}
	return claims.OrgID, nil
}
