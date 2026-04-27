package agent

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── Test cert helpers ────────────────────────────────────────────────
//
// These helpers materialize cert/key pairs on disk inside t.TempDir(),
// using ECDSA-P256 for speed (RSA at sufficient size takes hundreds of
// ms per cert and would balloon the test suite). They're shared by
// tls_config_test.go and server_tls_test.go.

type testCertHandle struct {
	certPath string
	keyPath  string
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
}

type testCertOpts struct {
	isCA      bool
	clientUse bool
	parent    *testCertHandle // when set, the new cert is signed by this CA
}

// genTestCert creates a self-signed (or parent-signed) cert/key pair on
// disk. name is used both as Common Name and as the file prefix.
func genTestCert(t *testing.T, name string, opts testCertOpts) *testCertHandle {
	t.Helper()
	dir := t.TempDir()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	if opts.isCA {
		template.IsCA = true
		template.BasicConstraintsValid = true
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	} else {
		template.KeyUsage = x509.KeyUsageDigitalSignature
		if opts.clientUse {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		} else {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			template.DNSNames = []string{"localhost"}
		}
	}

	parentTemplate := template
	var parentKey crypto.Signer = key
	if opts.parent != nil {
		parentTemplate = opts.parent.cert
		parentKey = opts.parent.key
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, parentTemplate, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	certPath := filepath.Join(dir, name+".cert.pem")
	keyPath := filepath.Join(dir, name+".key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return &testCertHandle{certPath: certPath, keyPath: keyPath, cert: cert, key: key}
}

// ─── LoadServerTLSFromEnv ─────────────────────────────────────────────

func TestLoadServerTLSFromEnv_AllEmptyIsPlaintext(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_TLS_CERT_FILE", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_KEY_FILE", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_CLIENT_CA", "")
	t.Setenv("KUBEBOLT_AGENT_REQUIRE_MTLS", "")

	cfg, err := LoadServerTLSFromEnv()
	if err != nil {
		t.Fatalf("LoadServerTLSFromEnv: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil cfg (plaintext), got %+v", cfg)
	}
}

func TestLoadServerTLSFromEnv_HalfSetIsError(t *testing.T) {
	cases := []struct {
		name string
		cert string
		key  string
	}{
		{"cert only", "/tmp/cert.pem", ""},
		{"key only", "", "/tmp/key.pem"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("KUBEBOLT_AGENT_TLS_CERT_FILE", c.cert)
			t.Setenv("KUBEBOLT_AGENT_TLS_KEY_FILE", c.key)
			t.Setenv("KUBEBOLT_AGENT_TLS_CLIENT_CA", "")
			t.Setenv("KUBEBOLT_AGENT_REQUIRE_MTLS", "")
			if _, err := LoadServerTLSFromEnv(); err == nil {
				t.Error("expected error on half-set TLS config")
			}
		})
	}
}

func TestLoadServerTLSFromEnv_RequireMTLSWithoutCAIsError(t *testing.T) {
	server := genTestCert(t, "server", testCertOpts{})
	t.Setenv("KUBEBOLT_AGENT_TLS_CERT_FILE", server.certPath)
	t.Setenv("KUBEBOLT_AGENT_TLS_KEY_FILE", server.keyPath)
	t.Setenv("KUBEBOLT_AGENT_TLS_CLIENT_CA", "")
	t.Setenv("KUBEBOLT_AGENT_REQUIRE_MTLS", "true")

	if _, err := LoadServerTLSFromEnv(); err == nil {
		t.Error("expected error: REQUIRE_MTLS=true without TLS_CLIENT_CA")
	}
}

func TestLoadServerTLSFromEnv_RequireMTLSAloneIsError(t *testing.T) {
	// REQUIRE_MTLS without any cert/key is meaningless and must fail
	// loud — silently dropping to plaintext would be misleading.
	t.Setenv("KUBEBOLT_AGENT_TLS_CERT_FILE", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_KEY_FILE", "")
	t.Setenv("KUBEBOLT_AGENT_TLS_CLIENT_CA", "")
	t.Setenv("KUBEBOLT_AGENT_REQUIRE_MTLS", "true")

	if _, err := LoadServerTLSFromEnv(); err == nil {
		t.Error("expected error: REQUIRE_MTLS=true without server cert/key")
	}
}

func TestLoadServerTLSFromEnv_HappyPathServerOnly(t *testing.T) {
	server := genTestCert(t, "server", testCertOpts{})
	t.Setenv("KUBEBOLT_AGENT_TLS_CERT_FILE", server.certPath)
	t.Setenv("KUBEBOLT_AGENT_TLS_KEY_FILE", server.keyPath)
	t.Setenv("KUBEBOLT_AGENT_TLS_CLIENT_CA", "")
	t.Setenv("KUBEBOLT_AGENT_REQUIRE_MTLS", "")

	cfg, err := LoadServerTLSFromEnv()
	if err != nil {
		t.Fatalf("LoadServerTLSFromEnv: %v", err)
	}
	if cfg == nil || cfg.Config == nil {
		t.Fatal("expected populated config")
	}
	if cfg.Config.ClientAuth != tls.NoClientCert {
		t.Errorf("ClientAuth = %v, want NoClientCert", cfg.Config.ClientAuth)
	}
	if cfg.RequireMTLS {
		t.Error("RequireMTLS should be false")
	}
}

// ─── BuildServerTLSConfig ─────────────────────────────────────────────

func TestBuildServerTLSConfig_ServerOnly(t *testing.T) {
	server := genTestCert(t, "server", testCertOpts{})
	cfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, "", false)
	if err != nil {
		t.Fatalf("BuildServerTLSConfig: %v", err)
	}
	if cfg.Config.ClientAuth != tls.NoClientCert {
		t.Errorf("ClientAuth = %v, want NoClientCert", cfg.Config.ClientAuth)
	}
	if cfg.Config.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want >= TLS 1.2", cfg.Config.MinVersion)
	}
}

func TestBuildServerTLSConfig_OptionalMTLS(t *testing.T) {
	ca := genTestCert(t, "ca", testCertOpts{isCA: true})
	server := genTestCert(t, "server", testCertOpts{parent: ca})
	cfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, ca.certPath, false)
	if err != nil {
		t.Fatalf("BuildServerTLSConfig: %v", err)
	}
	if cfg.Config.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Errorf("ClientAuth = %v, want VerifyClientCertIfGiven", cfg.Config.ClientAuth)
	}
	if cfg.RequireMTLS {
		t.Error("RequireMTLS must be false")
	}
}

func TestBuildServerTLSConfig_RequireMTLS(t *testing.T) {
	ca := genTestCert(t, "ca", testCertOpts{isCA: true})
	server := genTestCert(t, "server", testCertOpts{parent: ca})
	cfg, err := BuildServerTLSConfig(server.certPath, server.keyPath, ca.certPath, true)
	if err != nil {
		t.Fatalf("BuildServerTLSConfig: %v", err)
	}
	if cfg.Config.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.Config.ClientAuth)
	}
	if !cfg.RequireMTLS {
		t.Error("RequireMTLS must be true")
	}
}

func TestBuildServerTLSConfig_MissingCertFile(t *testing.T) {
	if _, err := BuildServerTLSConfig("/nonexistent/cert.pem", "/nonexistent/key.pem", "", false); err == nil {
		t.Error("expected error for missing cert file")
	}
}

func TestBuildServerTLSConfig_BadClientCA(t *testing.T) {
	server := genTestCert(t, "server", testCertOpts{})
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildServerTLSConfig(server.certPath, server.keyPath, bad, false); err == nil {
		t.Error("expected error for non-PEM client CA")
	}
}
