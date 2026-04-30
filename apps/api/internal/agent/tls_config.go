package agent

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// TLSConfig is the resolved server TLS configuration. Listen treats
// nil as plaintext mode (with a startup warning).
//
// ENTERPRISE-CANDIDATE (mTLS):
// Plain TLS (server cert only) stays OSS — that is baseline transport
// security. The mTLS path (client cert verification, ClientCA-driven
// chains, RequireMTLS) is a candidate to move behind a license gate
// when the SaaS hospedado launches. The split point is the ClientAuth
// configuration in BuildServerTLSConfig: server-only TLS = OSS, any
// VerifyClientCert variant = Enterprise.
type TLSConfig struct {
	Config *tls.Config
	// RequireMTLS is exposed so AuthConfig.RequireMTLS can be derived
	// from the same env. The server-side ClientAuth setting handles the
	// handshake-level rejection; the interceptor uses this flag to
	// double-check identity.TLSVerified after auth (defense in depth
	// for the case where some operator wires a TLS terminator in front
	// of us that strips the client cert).
	RequireMTLS bool
}

// LoadServerTLSFromEnv reads KUBEBOLT_AGENT_TLS_* vars and returns a
// TLSConfig or nil (plaintext). It refuses half-set configurations:
// having only a cert without a key — or REQUIRE_MTLS without a client
// CA — almost certainly indicates a typo, and silent fallback to
// plaintext would mask it.
//
// Env vars:
//
//	KUBEBOLT_AGENT_TLS_CERT_FILE     server cert path (PEM)
//	KUBEBOLT_AGENT_TLS_KEY_FILE      server key path (PEM)
//	KUBEBOLT_AGENT_TLS_CLIENT_CA     CA bundle to validate client certs (enables mTLS)
//	KUBEBOLT_AGENT_REQUIRE_MTLS      "true" to require a verified client cert
func LoadServerTLSFromEnv() (*TLSConfig, error) {
	certFile := os.Getenv("KUBEBOLT_AGENT_TLS_CERT_FILE")
	keyFile := os.Getenv("KUBEBOLT_AGENT_TLS_KEY_FILE")
	clientCA := os.Getenv("KUBEBOLT_AGENT_TLS_CLIENT_CA")
	requireMTLS := os.Getenv("KUBEBOLT_AGENT_REQUIRE_MTLS") == "true"

	// All-empty → plaintext, no error.
	if certFile == "" && keyFile == "" && clientCA == "" && !requireMTLS {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("agent TLS: cert and key must both be set (got cert=%q key=%q)", certFile, keyFile)
	}
	if requireMTLS && clientCA == "" {
		return nil, errors.New("agent TLS: KUBEBOLT_AGENT_REQUIRE_MTLS=true but TLS_CLIENT_CA not set")
	}
	return BuildServerTLSConfig(certFile, keyFile, clientCA, requireMTLS)
}

// BuildServerTLSConfig assembles the *tls.Config from explicit paths.
// Extracted so tests can exercise the chain without going through env.
//
// ClientAuth resolution:
//
//	clientCA == ""              → tls.NoClientCert
//	clientCA set, !requireMTLS  → tls.VerifyClientCertIfGiven
//	clientCA set, requireMTLS   → tls.RequireAndVerifyClientCert
func BuildServerTLSConfig(certFile, keyFile, clientCA string, requireMTLS bool) (*TLSConfig, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if clientCA != "" {
		caBytes, err := os.ReadFile(clientCA)
		if err != nil {
			return nil, fmt.Errorf("read client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("client CA at %s contains no parseable PEM", clientCA)
		}
		cfg.ClientCAs = pool
		if requireMTLS {
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			cfg.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}
	return &TLSConfig{Config: cfg, RequireMTLS: requireMTLS}, nil
}
