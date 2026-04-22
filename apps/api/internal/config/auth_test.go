package config

import (
	"os"
	"testing"
	"time"
)

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"KUBEBOLT_AUTH_ENABLED", "KUBEBOLT_JWT_SECRET", "KUBEBOLT_JWT_EXPIRY",
		"KUBEBOLT_JWT_REFRESH_EXPIRY", "KUBEBOLT_DATA_DIR", "KUBEBOLT_ADMIN_PASSWORD",
	} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}

func TestLoadAuthConfig_EnabledByDefault(t *testing.T) {
	clearAuthEnv(t)
	// Admin password auto-generation prints to stderr — we tolerate that noise
	// in test output.
	cfg := LoadAuthConfig()
	if !cfg.Enabled {
		t.Error("auth should be enabled by default")
	}
	if cfg.AccessTokenExpiry != 15*time.Minute {
		t.Errorf("access expiry = %v, want 15m", cfg.AccessTokenExpiry)
	}
	if cfg.RefreshTokenExpiry != 7*24*time.Hour {
		t.Errorf("refresh expiry = %v, want 168h", cfg.RefreshTokenExpiry)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("default data dir = %q", cfg.DataDir)
	}
}

func TestLoadAuthConfig_Disabled(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("KUBEBOLT_AUTH_ENABLED", "false")

	cfg := LoadAuthConfig()
	if cfg.Enabled {
		t.Error("should be disabled")
	}
	// When disabled, short-circuit returns before even reading other vars
	if cfg.InitialAdminPassword != "" {
		t.Error("disabled config should skip admin password generation")
	}
}

func TestLoadAuthConfig_CustomValues(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("KUBEBOLT_AUTH_ENABLED", "true")
	t.Setenv("KUBEBOLT_JWT_SECRET", "my-explicit-secret")
	t.Setenv("KUBEBOLT_JWT_EXPIRY", "30m")
	t.Setenv("KUBEBOLT_JWT_REFRESH_EXPIRY", "72h")
	t.Setenv("KUBEBOLT_DATA_DIR", "/var/lib/kubebolt")
	t.Setenv("KUBEBOLT_ADMIN_PASSWORD", "set-by-operator")

	cfg := LoadAuthConfig()
	if string(cfg.JWTSecret) != "my-explicit-secret" {
		t.Errorf("secret mismatch")
	}
	if !cfg.JWTSecretFromEnv {
		t.Error("JWTSecretFromEnv should be true when KUBEBOLT_JWT_SECRET is set")
	}
	if cfg.AccessTokenExpiry != 30*time.Minute {
		t.Errorf("access expiry = %v", cfg.AccessTokenExpiry)
	}
	if cfg.RefreshTokenExpiry != 72*time.Hour {
		t.Errorf("refresh expiry = %v", cfg.RefreshTokenExpiry)
	}
	if cfg.DataDir != "/var/lib/kubebolt" {
		t.Errorf("data dir = %q", cfg.DataDir)
	}
	if cfg.InitialAdminPassword != "set-by-operator" {
		t.Errorf("admin password = %q", cfg.InitialAdminPassword)
	}
}

func TestLoadAuthConfig_GeneratesPasswordWhenMissing(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("KUBEBOLT_AUTH_ENABLED", "true")
	cfg := LoadAuthConfig()
	if cfg.InitialAdminPassword == "" {
		t.Error("should auto-generate admin password when unset")
	}
	if len(cfg.InitialAdminPassword) != 16 {
		t.Errorf("generated password length = %d, want 16", len(cfg.InitialAdminPassword))
	}
}
