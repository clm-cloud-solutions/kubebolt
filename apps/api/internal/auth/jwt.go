package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
}

// JWTService handles JWT token generation and validation.
type JWTService struct {
	secret        []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
}

// NewJWTService creates a new JWT service from auth config.
func NewJWTService(cfg config.AuthConfig) *JWTService {
	return &JWTService{
		secret:        cfg.JWTSecret,
		accessExpiry:  cfg.AccessTokenExpiry,
		refreshExpiry: cfg.RefreshTokenExpiry,
	}
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
