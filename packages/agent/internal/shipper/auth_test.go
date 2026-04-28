package shipper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// ─── LoadAuthFromEnv ──────────────────────────────────────────────────

func TestLoadAuthFromEnv_Populated(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_AUTH_MODE", "ingest-token")
	t.Setenv("KUBEBOLT_AGENT_TOKEN_FILE", "/var/run/secrets/kubebolt/token")
	t.Setenv("KUBEBOLT_AGENT_TLS_ENABLED", "true")
	t.Setenv("KUBEBOLT_AGENT_TLS_CA_FILE", "/etc/tls/ca.pem")
	t.Setenv("KUBEBOLT_AGENT_TLS_CERT_FILE", "/etc/tls/client.crt")
	t.Setenv("KUBEBOLT_AGENT_TLS_KEY_FILE", "/etc/tls/client.key")
	t.Setenv("KUBEBOLT_AGENT_TLS_SERVER_NAME", "kubebolt.local")

	o := LoadAuthFromEnv()
	if o.Mode != AuthModeIngestToken {
		t.Errorf("Mode = %s, want ingest-token", o.Mode)
	}
	if o.TokenFile != "/var/run/secrets/kubebolt/token" {
		t.Errorf("TokenFile = %s", o.TokenFile)
	}
	if !o.TLSEnabled {
		t.Error("TLSEnabled should be true")
	}
	if o.CAFile != "/etc/tls/ca.pem" {
		t.Errorf("CAFile = %s", o.CAFile)
	}
	if o.CertFile != "/etc/tls/client.crt" || o.KeyFile != "/etc/tls/client.key" {
		t.Errorf("client cert pair wrong: %s / %s", o.CertFile, o.KeyFile)
	}
	if o.ServerName != "kubebolt.local" {
		t.Errorf("ServerName = %s", o.ServerName)
	}
	if !o.HasAuth() {
		t.Error("HasAuth should be true")
	}
}

func TestLoadAuthFromEnv_AllEmpty(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_AUTH_MODE", "")
	t.Setenv("KUBEBOLT_AGENT_TOKEN_FILE", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_ENABLED", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_CA_FILE", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_CERT_FILE", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_KEY_FILE", "")

	o := LoadAuthFromEnv()
	if o.HasAuth() {
		t.Error("zero env should not yield HasAuth=true")
	}
	if err := o.Validate(); err != nil {
		t.Errorf("zero env should validate ok, got %v", err)
	}
}

func TestLoadAuthFromEnv_TLSEnabledOnlyOnExactTrue(t *testing.T) {
	for _, v := range []string{"1", "yes", "TRUE", ""} {
		t.Setenv("KUBEBOLT_AGENT_TLS_ENABLED", v)
		if LoadAuthFromEnv().TLSEnabled {
			t.Errorf("TLSEnabled should be false for value %q (only exact \"true\" enables)", v)
		}
	}
	t.Setenv("KUBEBOLT_AGENT_TLS_ENABLED", "true")
	if !LoadAuthFromEnv().TLSEnabled {
		t.Error("TLSEnabled should be true for \"true\"")
	}
}

// ─── Validate ─────────────────────────────────────────────────────────

func TestValidate_NoAuthIsOK(t *testing.T) {
	if err := (AuthOptions{}).Validate(); err != nil {
		t.Errorf("zero value should validate, got %v", err)
	}
}

func TestValidate_TokenFileWithoutMode(t *testing.T) {
	if err := (AuthOptions{TokenFile: "/tmp/x"}).Validate(); err == nil {
		t.Error("expected error: TOKEN_FILE without AUTH_MODE")
	}
}

func TestValidate_ModeWithoutTokenFile(t *testing.T) {
	if err := (AuthOptions{Mode: AuthModeIngestToken}).Validate(); err == nil {
		t.Error("expected error: AUTH_MODE without TOKEN_FILE")
	}
}

func TestValidate_UnknownMode(t *testing.T) {
	dir := t.TempDir()
	tok := writeFile(t, dir, "token", "kb_x")
	if err := (AuthOptions{Mode: "oauth", TokenFile: tok}).Validate(); err == nil {
		t.Error("expected error: unknown mode")
	}
}

func TestValidate_TokenFileMustExistAtStartup(t *testing.T) {
	err := AuthOptions{Mode: AuthModeIngestToken, TokenFile: "/nonexistent/token"}.Validate()
	if err == nil {
		t.Error("expected error when token file does not exist")
	}
}

func TestValidate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	tok := writeFile(t, dir, "token", "kb_abc")
	if err := (AuthOptions{Mode: AuthModeIngestToken, TokenFile: tok}).Validate(); err != nil {
		t.Errorf("expected ok, got %v", err)
	}
}

func TestValidate_HalfSetMTLS(t *testing.T) {
	dir := t.TempDir()
	tok := writeFile(t, dir, "token", "kb_x")
	cases := []struct {
		name     string
		certFile string
		keyFile  string
	}{
		{"cert without key", "/tmp/cert.pem", ""},
		{"key without cert", "", "/tmp/key.pem"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := AuthOptions{
				Mode:       AuthModeIngestToken,
				TokenFile:  tok,
				TLSEnabled: true,
				CertFile:   c.certFile,
				KeyFile:    c.keyFile,
			}.Validate()
			if err == nil {
				t.Error("expected error on half-set mTLS")
			}
		})
	}
}

func TestValidate_ClientCertWithoutTLS(t *testing.T) {
	dir := t.TempDir()
	tok := writeFile(t, dir, "token", "kb_x")
	err := AuthOptions{
		Mode:       AuthModeIngestToken,
		TokenFile:  tok,
		TLSEnabled: false,
		CertFile:   "/tmp/c.pem",
		KeyFile:    "/tmp/k.pem",
	}.Validate()
	if err == nil {
		t.Error("expected error: client cert configured but TLS disabled")
	}
}

// ─── tokenCreds ───────────────────────────────────────────────────────

func TestTokenCreds_AddsBearerAndModeHeaders(t *testing.T) {
	dir := t.TempDir()
	tokenPath := writeFile(t, dir, "token", "kb_secret_value\n")

	creds := NewTokenCreds(AuthOptions{
		Mode:       AuthModeIngestToken,
		TokenFile:  tokenPath,
		TLSEnabled: true,
	})
	if creds == nil {
		t.Fatal("creds should not be nil")
	}

	md, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if md["authorization"] != "Bearer kb_secret_value" {
		t.Errorf("authorization = %q, want Bearer kb_secret_value (note: trim whitespace)", md["authorization"])
	}
	if md["kubebolt-auth-mode"] != "ingest-token" {
		t.Errorf("kubebolt-auth-mode = %q, want ingest-token", md["kubebolt-auth-mode"])
	}
}

func TestTokenCreds_ReReadsOnEachCall(t *testing.T) {
	// Pin the rotation contract: kubelet rotating the projected SA
	// token while the connection stays open must propagate to the next
	// RPC without reconnect. Re-reading the file on every call is how.
	dir := t.TempDir()
	tokenPath := writeFile(t, dir, "token", "v1")

	creds := NewTokenCreds(AuthOptions{Mode: AuthModeIngestToken, TokenFile: tokenPath, TLSEnabled: true})

	md1, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md1["authorization"] != "Bearer v1" {
		t.Errorf("first read = %q", md1["authorization"])
	}

	if err := os.WriteFile(tokenPath, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	md2, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md2["authorization"] != "Bearer v2" {
		t.Errorf("after rotation = %q (re-read failed)", md2["authorization"])
	}
}

func TestTokenCreds_EmptyTokenIsError(t *testing.T) {
	dir := t.TempDir()
	tokenPath := writeFile(t, dir, "token", "   \n")
	creds := NewTokenCreds(AuthOptions{Mode: AuthModeIngestToken, TokenFile: tokenPath, TLSEnabled: true})
	if _, err := creds.GetRequestMetadata(context.Background()); err == nil {
		t.Error("expected error for whitespace-only token file")
	}
}

func TestTokenCreds_MissingFileIsError(t *testing.T) {
	dir := t.TempDir()
	tokenPath := writeFile(t, dir, "token", "v1")
	creds := NewTokenCreds(AuthOptions{Mode: AuthModeIngestToken, TokenFile: tokenPath, TLSEnabled: true})
	_ = os.Remove(tokenPath)
	if _, err := creds.GetRequestMetadata(context.Background()); err == nil {
		t.Error("expected error when token file goes missing between calls")
	}
}

func TestTokenCreds_RequireTransportSecurity(t *testing.T) {
	dir := t.TempDir()
	tok := writeFile(t, dir, "token", "kb_x")

	on := NewTokenCreds(AuthOptions{Mode: AuthModeIngestToken, TokenFile: tok, TLSEnabled: true})
	if !on.RequireTransportSecurity() {
		t.Error("RequireTransportSecurity should be true with TLSEnabled=true")
	}
	off := NewTokenCreds(AuthOptions{Mode: AuthModeIngestToken, TokenFile: tok, TLSEnabled: false})
	if off.RequireTransportSecurity() {
		t.Error("RequireTransportSecurity should be false with TLSEnabled=false (operator opts in to risk)")
	}
}

func TestNewTokenCreds_NilWhenNoAuth(t *testing.T) {
	if creds := NewTokenCreds(AuthOptions{}); creds != nil {
		t.Error("NewTokenCreds should return nil when HasAuth=false")
	}
}

// ─── BuildTransportCredentials ────────────────────────────────────────

func TestBuildTransportCredentials_PlaintextWhenTLSOff(t *testing.T) {
	creds, err := BuildTransportCredentials(AuthOptions{TLSEnabled: false})
	if err != nil {
		t.Fatalf("BuildTransportCredentials: %v", err)
	}
	if creds == nil {
		t.Error("expected insecure credentials, got nil")
	}
}

func TestBuildTransportCredentials_TLSWithoutCAUsesSystemTrust(t *testing.T) {
	// No CA file → cfg.RootCAs stays nil → Go falls back to the
	// system trust store. This is the right default for SaaS where
	// the backend cert chains to a public CA.
	creds, err := BuildTransportCredentials(AuthOptions{TLSEnabled: true, ServerName: "example.com"})
	if err != nil {
		t.Fatalf("BuildTransportCredentials: %v", err)
	}
	if creds == nil {
		t.Error("expected TLS credentials, got nil")
	}
}

func TestBuildTransportCredentials_BadCAFile(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "ca.pem", "not a pem block")
	_, err := BuildTransportCredentials(AuthOptions{TLSEnabled: true, CAFile: bad})
	if err == nil {
		t.Error("expected error for non-PEM CA file")
		return
	}
	if !strings.Contains(err.Error(), "PEM") {
		t.Errorf("error should mention PEM, got %v", err)
	}
}

func TestBuildTransportCredentials_MissingCAFile(t *testing.T) {
	_, err := BuildTransportCredentials(AuthOptions{TLSEnabled: true, CAFile: "/nonexistent/ca.pem"})
	if err == nil {
		t.Error("expected error for missing CA file")
	}
}

func TestBuildTransportCredentials_BadClientCertPair(t *testing.T) {
	dir := t.TempDir()
	cert := writeFile(t, dir, "cert.pem", "not a real cert")
	key := writeFile(t, dir, "key.pem", "not a real key")
	_, err := BuildTransportCredentials(AuthOptions{TLSEnabled: true, CertFile: cert, KeyFile: key})
	if err == nil {
		t.Error("expected error for invalid client cert/key")
	}
}
