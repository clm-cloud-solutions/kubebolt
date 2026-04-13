package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// AuthConfig holds authentication configuration loaded from KUBEBOLT_AUTH_* env vars.
type AuthConfig struct {
	Enabled              bool
	JWTSecret            []byte
	JWTSecretFromEnv     bool // true if the secret was explicitly set via env var
	AccessTokenExpiry    time.Duration
	RefreshTokenExpiry   time.Duration
	DataDir              string
	InitialAdminPassword string
}

// LoadAuthConfig reads authentication configuration from environment variables.
func LoadAuthConfig() AuthConfig {
	cfg := AuthConfig{
		Enabled:            true,
		AccessTokenExpiry:  15 * time.Minute,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
		DataDir:            "./data",
	}

	if v := os.Getenv("KUBEBOLT_AUTH_ENABLED"); v != "" {
		cfg.Enabled = strings.ToLower(v) == "true" || v == "1"
	}

	if !cfg.Enabled {
		return cfg
	}

	// JWT secret
	if v := os.Getenv("KUBEBOLT_JWT_SECRET"); v != "" {
		cfg.JWTSecret = []byte(v)
		cfg.JWTSecretFromEnv = true
	}
	// If not set, it will be resolved from the database in main.go (generate once, persist)

	// Token expiry
	if v := os.Getenv("KUBEBOLT_JWT_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.AccessTokenExpiry = d
		}
	}
	if v := os.Getenv("KUBEBOLT_JWT_REFRESH_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RefreshTokenExpiry = d
		}
	}

	// Data directory
	if v := os.Getenv("KUBEBOLT_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}

	// Admin password
	if v := os.Getenv("KUBEBOLT_ADMIN_PASSWORD"); v != "" {
		cfg.InitialAdminPassword = v
	} else {
		pw := generateRandomPassword(16)
		cfg.InitialAdminPassword = pw
		fmt.Fprintf(os.Stderr, "\n"+
			"========================================\n"+
			"  Generated admin password: %s\n"+
			"  (set KUBEBOLT_ADMIN_PASSWORD to override)\n"+
			"========================================\n\n", pw)
	}

	return cfg
}

func generateRandomPassword(length int) string {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("Failed to generate random password: %v", err)
	}
	return hex.EncodeToString(b)[:length]
}
