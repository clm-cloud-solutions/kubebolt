// Package shipper — auth.go owns the agent-side credentials the
// shipper attaches to its gRPC dial.
//
// Two layers, independently configurable via env vars set by the
// helm DaemonSet template:
//
//	Auth (mode header + bearer token)   →  PerRPCCredentials
//	Transport (TLS / mTLS to backend)   →  TransportCredentials
//
// Token re-read happens on every RPC against the same file path —
// kubelet rotates projected SA tokens transparently, and the same
// flow works for ingest tokens mounted from a Secret. Reads are tmpfs,
// so the cost is negligible.
package shipper

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// AuthMode mirrors the backend's "kubebolt-auth-mode" header values.
type AuthMode string

const (
	AuthModeTokenReview AuthMode = "tokenreview"
	AuthModeIngestToken AuthMode = "ingest-token"
)

// AuthOptions carries everything the shipper needs to authenticate
// with the backend. Zero value yields plaintext + no token (Sprint 0
// migration default): the shipper dials with insecure credentials and
// the backend accepts because enforcement is "disabled".
type AuthOptions struct {
	// Mode + TokenFile go together. Empty Mode disables per-RPC creds.
	Mode      AuthMode
	TokenFile string

	// TLS, independent from auth mode.
	TLSEnabled bool
	CAFile     string // server cert trust bundle (PEM)
	CertFile   string // client cert (mTLS) — paired with KeyFile
	KeyFile    string // client key (mTLS) — paired with CertFile
	ServerName string // SNI override; empty = derive from dial target
}

// LoadAuthFromEnv reads KUBEBOLT_AGENT_* env vars set by the helm
// DaemonSet template. Returns the zero value when nothing is set.
func LoadAuthFromEnv() AuthOptions {
	return AuthOptions{
		Mode:       AuthMode(os.Getenv("KUBEBOLT_AGENT_AUTH_MODE")),
		TokenFile:  os.Getenv("KUBEBOLT_AGENT_TOKEN_FILE"),
		TLSEnabled: os.Getenv("KUBEBOLT_AGENT_TLS_ENABLED") == "true",
		CAFile:     os.Getenv("KUBEBOLT_AGENT_TLS_CA_FILE"),
		CertFile:   os.Getenv("KUBEBOLT_AGENT_TLS_CERT_FILE"),
		KeyFile:    os.Getenv("KUBEBOLT_AGENT_TLS_KEY_FILE"),
		ServerName: os.Getenv("KUBEBOLT_AGENT_TLS_SERVER_NAME"),
	}
}

// Validate refuses combinations that almost certainly indicate a typo
// in helm values: half-set auth, half-set mTLS, unknown mode, or a
// configured token file path that does not exist at startup.
//
// Validate intentionally does NOT log — call sites surface warnings
// (e.g. "auth-without-TLS") closer to the operator-facing layer.
func (o AuthOptions) Validate() error {
	switch o.Mode {
	case "":
		if o.TokenFile != "" {
			return errors.New("KUBEBOLT_AGENT_TOKEN_FILE set but KUBEBOLT_AGENT_AUTH_MODE empty")
		}
	case AuthModeTokenReview, AuthModeIngestToken:
		if o.TokenFile == "" {
			return fmt.Errorf("KUBEBOLT_AGENT_AUTH_MODE=%s requires KUBEBOLT_AGENT_TOKEN_FILE", o.Mode)
		}
		if _, err := os.Stat(o.TokenFile); err != nil {
			return fmt.Errorf("token file %s: %w", o.TokenFile, err)
		}
	default:
		return fmt.Errorf("unknown KUBEBOLT_AGENT_AUTH_MODE %q (want tokenreview or ingest-token)", o.Mode)
	}
	if (o.CertFile != "") != (o.KeyFile != "") {
		return errors.New("KUBEBOLT_AGENT_TLS_CERT_FILE and TLS_KEY_FILE must both be set or both empty")
	}
	if (o.CertFile != "" || o.KeyFile != "") && !o.TLSEnabled {
		return errors.New("client cert configured but KUBEBOLT_AGENT_TLS_ENABLED is not true")
	}
	return nil
}

// HasAuth reports whether per-RPC credentials should be attached.
func (o AuthOptions) HasAuth() bool { return o.Mode != "" && o.TokenFile != "" }

// ─── PerRPCCredentials ────────────────────────────────────────────────

// tokenCreds re-reads the token file on every call. Kubelet rotates
// projected SA tokens well before they expire and the file path is
// constant, so re-read makes rotation transparent to the connection.
type tokenCreds struct {
	mode   AuthMode
	file   string
	secure bool
}

// NewTokenCreds returns the per-RPC credentials, or nil when no auth
// is configured (zero AuthOptions / Sprint 0 mode).
func NewTokenCreds(opts AuthOptions) credentials.PerRPCCredentials {
	if !opts.HasAuth() {
		return nil
	}
	return &tokenCreds{
		mode:   opts.Mode,
		file:   opts.TokenFile,
		secure: opts.TLSEnabled,
	}
}

func (c *tokenCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	raw, err := os.ReadFile(c.file)
	if err != nil {
		return nil, fmt.Errorf("read token file %s: %w", c.file, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("token file %s is empty", c.file)
	}
	return map[string]string{
		"authorization":      "Bearer " + token,
		"kubebolt-auth-mode": string(c.mode),
	}, nil
}

// RequireTransportSecurity tells gRPC whether to refuse plaintext
// connections when this credential is attached. We honor TLSEnabled:
// with TLS off, gRPC permits the bearer to ride plaintext (the
// operator assumes the risk; main.go logs a WARN at startup).
func (c *tokenCreds) RequireTransportSecurity() bool { return c.secure }

// ─── Transport credentials ────────────────────────────────────────────

// BuildTransportCredentials returns the gRPC TransportCredentials the
// shipper should dial with. TLSEnabled=false yields insecure
// credentials (plaintext) — the legacy Sprint 0 behavior.
func BuildTransportCredentials(opts AuthOptions) (credentials.TransportCredentials, error) {
	if !opts.TLSEnabled {
		return insecure.NewCredentials(), nil
	}
	cfg := &tls.Config{
		ServerName: opts.ServerName,
		MinVersion: tls.VersionTLS12,
	}
	if opts.CAFile != "" {
		caPEM, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA %s: %w", opts.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("CA at %s contains no parseable PEM", opts.CAFile)
		}
		cfg.RootCAs = pool
	}
	if opts.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return credentials.NewTLS(cfg), nil
}
