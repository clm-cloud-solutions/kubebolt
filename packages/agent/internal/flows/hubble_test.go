package flows

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/credentials/insecure"
)

// TestBuildRelayCredentials_Branches walks buildRelayCredentials through
// every env-var combination it handles. Certs are generated in-memory
// with crypto/x509 so the test is self-contained (no openssl or CI
// fixtures) and every PEM exercised by LoadX509KeyPair is real.
func TestBuildRelayCredentials_Branches(t *testing.T) {
	// Common fixture: a CA plus one server and one client leaf certs.
	// Each test that needs them writes the PEMs to a tempdir and points
	// the relevant env vars at them.
	ca := newTestCert(t, "kubebolt-test-ca", nil)
	client := newTestCert(t, "kubebolt-agent", &ca)

	t.Run("no env vars → insecure credentials", func(t *testing.T) {
		clearRelayEnv(t)
		creds, err := buildRelayCredentials()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The insecure package exports a sentinel type; compare against
		// a fresh instance by protocol-name to keep the assertion stable
		// across grpc-go minor versions.
		if got, want := creds.Info().SecurityProtocol, insecure.NewCredentials().Info().SecurityProtocol; got != want {
			t.Fatalf("expected insecure creds, got protocol %q (want %q)", got, want)
		}
	})

	t.Run("only CA → TLS server-auth", func(t *testing.T) {
		clearRelayEnv(t)
		dir := t.TempDir()
		caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", caPath)

		creds, err := buildRelayCredentials()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := creds.Info().SecurityProtocol; got != "tls" {
			t.Fatalf("expected tls, got %q", got)
		}
	})

	t.Run("CA + client cert + key → mTLS", func(t *testing.T) {
		clearRelayEnv(t)
		dir := t.TempDir()
		caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
		certPath := writePEM(t, dir, "client.crt", client.certPEM)
		keyPath := writePEM(t, dir, "client.key", client.keyPEM)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", caPath)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CERT_FILE", certPath)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_KEY_FILE", keyPath)

		creds, err := buildRelayCredentials()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := creds.Info().SecurityProtocol; got != "tls" {
			t.Fatalf("expected tls, got %q", got)
		}
	})

	t.Run("CA file unreadable → error", func(t *testing.T) {
		clearRelayEnv(t)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", "/nonexistent/ca.pem")

		_, err := buildRelayCredentials()
		if err == nil {
			t.Fatal("expected an error for missing CA, got nil")
		}
		if !strings.Contains(err.Error(), "read hubble CA") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("CA file not a PEM cert → error", func(t *testing.T) {
		clearRelayEnv(t)
		dir := t.TempDir()
		garbagePath := writePEM(t, dir, "ca.pem", []byte("not a cert"))
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", garbagePath)

		_, err := buildRelayCredentials()
		if err == nil {
			t.Fatal("expected an error for garbage CA, got nil")
		}
		if !strings.Contains(err.Error(), "no valid certs") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("CERT without KEY → error", func(t *testing.T) {
		clearRelayEnv(t)
		dir := t.TempDir()
		caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
		certPath := writePEM(t, dir, "client.crt", client.certPEM)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", caPath)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CERT_FILE", certPath)
		// KEY deliberately omitted.

		_, err := buildRelayCredentials()
		if err == nil {
			t.Fatal("expected error when CERT set without KEY")
		}
		if !strings.Contains(err.Error(), "CERT_FILE and KEY_FILE") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("KEY without CERT → error", func(t *testing.T) {
		clearRelayEnv(t)
		dir := t.TempDir()
		caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
		keyPath := writePEM(t, dir, "client.key", client.keyPEM)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", caPath)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_KEY_FILE", keyPath)
		// CERT deliberately omitted.

		_, err := buildRelayCredentials()
		if err == nil {
			t.Fatal("expected error when KEY set without CERT")
		}
	})

	t.Run("mismatched cert and key → error", func(t *testing.T) {
		clearRelayEnv(t)
		dir := t.TempDir()
		caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
		// Pair the client cert with a *different* key, so LoadX509KeyPair
		// rejects the mismatched pair.
		other := newTestCert(t, "other-client", &ca)
		certPath := writePEM(t, dir, "client.crt", client.certPEM)
		keyPath := writePEM(t, dir, "other.key", other.keyPEM)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CA_FILE", caPath)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_CERT_FILE", certPath)
		t.Setenv("KUBEBOLT_HUBBLE_RELAY_KEY_FILE", keyPath)

		_, err := buildRelayCredentials()
		if err == nil {
			t.Fatal("expected cert/key mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "load hubble client cert") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

// testCert is a minimal holder for PEM-encoded cert + key used by the
// credential builder tests.
type testCert struct {
	certPEM []byte
	keyPEM  []byte
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
}

// newTestCert mints either a self-signed CA (parent==nil) or a leaf
// cert signed by the given parent. Uses ECDSA P-256 because it's fast
// and Go's stdlib handles it natively — RSA would just burn test time.
// The cn is also written into DNSNames so modern Go TLS (which rejects
// pure-CN verification since Go 1.17) can validate the server name.
func newTestCert(t *testing.T, cn string, parent *testCert) testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	if parent == nil {
		tpl.IsCA = true
		tpl.KeyUsage |= x509.KeyUsageCertSign
		tpl.BasicConstraintsValid = true
	}

	parentCert := tpl
	parentKey := any(key)
	if parent != nil {
		parentCert = parent.cert
		parentKey = parent.key
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tpl, parentCert, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	keyDer, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return testCert{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDer}),
		cert:    cert,
		key:     key,
	}
}

func writePEM(t *testing.T, dir, name string, pem []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// clearRelayEnv unsets every relay TLS env var so a previous subtest's
// configuration doesn't leak into the next one. t.Setenv handles
// restoration on test end, but *unset* isn't its model, so we set to
// empty and let buildRelayCredentials's "empty string" branch cover it.
func clearRelayEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"KUBEBOLT_HUBBLE_RELAY_CA_FILE",
		"KUBEBOLT_HUBBLE_RELAY_CERT_FILE",
		"KUBEBOLT_HUBBLE_RELAY_KEY_FILE",
		"KUBEBOLT_HUBBLE_RELAY_SERVER_NAME",
	} {
		t.Setenv(k, "")
	}
}
