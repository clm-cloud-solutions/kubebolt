package auth

import (
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

func testJWTService(access, refresh time.Duration) *JWTService {
	return NewJWTService(config.AuthConfig{
		JWTSecret:          []byte("test-secret-32-bytes-long!!!!!!!"),
		AccessTokenExpiry:  access,
		RefreshTokenExpiry: refresh,
	})
}

func TestGenerateAccessToken_Roundtrip(t *testing.T) {
	svc := testJWTService(time.Hour, 24*time.Hour)
	user := &User{ID: "uid1", Username: "alice", Role: RoleEditor}

	tok, err := svc.GenerateAccessToken(user)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}

	claims, err := svc.ValidateAccessToken(tok)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.UserID != "uid1" || claims.Username != "alice" || claims.Role != RoleEditor {
		t.Errorf("claim mismatch: %+v", claims)
	}
}

func TestValidateAccessToken_RejectsTampered(t *testing.T) {
	svc := testJWTService(time.Hour, 24*time.Hour)
	tok, _ := svc.GenerateAccessToken(&User{ID: "u", Username: "x", Role: RoleViewer})

	// Flip the last character to invalidate the signature. Pick a
	// replacement that differs from the original — JWTs end in a random
	// base64url char, so a fixed letter like "X" is a no-op ~1/64 of the time.
	replacement := byte('A')
	if tok[len(tok)-1] == 'A' {
		replacement = 'B'
	}
	tampered := tok[:len(tok)-1] + string(replacement)
	if _, err := svc.ValidateAccessToken(tampered); err == nil {
		t.Error("tampered token must not validate")
	}
}

func TestValidateAccessToken_RejectsWrongSecret(t *testing.T) {
	a := testJWTService(time.Hour, 24*time.Hour)
	b := NewJWTService(config.AuthConfig{
		JWTSecret:          []byte("different-secret-32-bytes-long!!"),
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 24 * time.Hour,
	})

	tok, _ := a.GenerateAccessToken(&User{ID: "u", Role: RoleViewer})
	if _, err := b.ValidateAccessToken(tok); err == nil {
		t.Error("token signed with different secret must not validate")
	}
}

func TestValidateAccessToken_RejectsExpired(t *testing.T) {
	svc := testJWTService(-1*time.Second, 24*time.Hour)
	tok, _ := svc.GenerateAccessToken(&User{ID: "u", Role: RoleViewer})
	if _, err := svc.ValidateAccessToken(tok); err == nil {
		t.Error("expired token must not validate")
	}
}

func TestGenerateRefreshToken(t *testing.T) {
	svc := testJWTService(time.Hour, 24*time.Hour)
	tok1, exp1, err := svc.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if len(tok1) != 64 { // 32 bytes hex = 64 chars
		t.Errorf("token length %d, want 64", len(tok1))
	}
	if time.Until(exp1) < 23*time.Hour {
		t.Errorf("expiry should be ~24h from now, got %v", time.Until(exp1))
	}
	// Should be random each call
	tok2, _, _ := svc.GenerateRefreshToken()
	if tok1 == tok2 {
		t.Error("refresh tokens should be unique across calls")
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	if HashToken("abc") != HashToken("abc") {
		t.Error("hash must be deterministic")
	}
	if HashToken("abc") == HashToken("abd") {
		t.Error("different inputs must produce different hashes")
	}
	// sha256 hex length = 64
	if len(HashToken("x")) != 64 {
		t.Error("hash should be 64 hex chars")
	}
}
