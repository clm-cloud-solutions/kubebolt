package mcpclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type countingTokenSource struct {
	n   atomic.Int32
	tok *oauth2.Token
	err error
}

func (s *countingTokenSource) Token() (*oauth2.Token, error) {
	s.n.Add(1)
	return s.tok, s.err
}

func TestBearerSigner(t *testing.T) {
	ts := &countingTokenSource{tok: &oauth2.Token{AccessToken: "ya29.test", Expiry: time.Now().Add(time.Hour)}}
	s := NewBearerSigner(ts, "my-project")
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, "https://sqladmin.googleapis.com/mcp", nil)
		if err := s.Sign(t.Context(), req, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "Bearer ya29.test" || req.Header.Get("x-goog-user-project") != "my-project" {
			t.Errorf("headers = %v", req.Header)
		}
	}
	if n := ts.n.Load(); n != 1 {
		t.Errorf("token fetched %d times, want 1 (cached until expiry)", n)
	}

	req, _ := http.NewRequest(http.MethodPost, "https://x.googleapis.com/mcp", nil)
	if err := NewBearerSigner(&countingTokenSource{tok: &oauth2.Token{AccessToken: "t"}}, "").Sign(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("x-goog-user-project") != "" {
		t.Error("no user project must mean no header")
	}
	for name, s := range map[string]Signer{
		"nil source":  NewBearerSigner(nil, "p"),
		"token error": NewBearerSigner(&countingTokenSource{err: errors.New("reauth needed")}, "p"),
		"empty token": NewBearerSigner(&countingTokenSource{tok: &oauth2.Token{}}, "p"),
	} {
		if err := s.Sign(t.Context(), req, nil); err == nil {
			t.Errorf("%s: signed anyway", name)
		}
	}
}

const secretMarker = "SECRET-MATERIAL-MUST-NOT-LEAK"

func serviceAccountJSON(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	m := map[string]any{
		"type":           "service_account",
		"project_id":     "p",
		"private_key_id": secretMarker,
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "kubebolt@p.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      "https://oauth2.googleapis.com/token",
	}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func externalAccountJSON(mutate func(m map[string]any)) []byte {
	m := map[string]any{
		"type":                              "external_account",
		"audience":                          "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/kb/providers/aws",
		"subject_token_type":                "urn:ietf:params:aws:token-type:aws4_request",
		"token_url":                         "https://sts.googleapis.com/v1/token",
		"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/kb@p.iam.gserviceaccount.com:generateAccessToken",
		"credential_source": map[string]any{
			"environment_id":                 "aws1",
			"regional_cred_verification_url": "https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15",
		},
		"client_secret": secretMarker,
	}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func setSource(field string, value any) func(m map[string]any) {
	return func(m map[string]any) {
		cs := m["credential_source"].(map[string]any)
		cs[field] = value
	}
}

func TestGoogleTokenSourceFromJSONAccepts(t *testing.T) {
	for name, doc := range map[string][]byte{
		"service_account":              serviceAccountJSON(t, nil),
		"service_account no token_uri": serviceAccountJSON(t, func(m map[string]any) { delete(m, "token_uri") }),
		"service_account legacy token_uri": serviceAccountJSON(t, func(m map[string]any) {
			m["token_uri"] = "https://accounts.google.com/o/oauth2/token"
		}),
		"external_account aws": externalAccountJSON(nil),
		"external_account no impersonation": externalAccountJSON(func(m map[string]any) {
			delete(m, "service_account_impersonation_url")
		}),
	} {
		ts, err := GoogleTokenSourceFromJSON(t.Context(), doc)
		if err != nil || ts == nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}

func TestGoogleTokenSourceFromJSONRefuses(t *testing.T) {
	cases := map[string][]byte{
		"empty":         nil,
		"not json":      []byte("{" + secretMarker),
		"too large":     []byte(`{"type":"service_account","x":"` + strings.Repeat("a", maxCredentialJSON) + `"}`),
		"no type":       []byte(`{"client_email":"x"}`),
		"user creds":    []byte(`{"type":"authorized_user","client_id":"x","client_secret":"` + secretMarker + `","refresh_token":"r"}`),
		"impersonated":  []byte(`{"type":"impersonated_service_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/x","source_credentials":{"type":"authorized_user"}}`),
		"gdch":          []byte(`{"type":"gdch_service_account"}`),
		"external user": []byte(`{"type":"external_account_authorized_user","audience":"a","refresh_token":"` + secretMarker + `","token_url":"https://sts.googleapis.com/v1/oauthtoken"}`),

		"sa missing key":        serviceAccountJSON(t, func(m map[string]any) { delete(m, "private_key") }),
		"sa token_uri metadata": serviceAccountJSON(t, func(m map[string]any) { m["token_uri"] = "http://169.254.169.254/computeMetadata/v1/token" }),
		"sa token_uri evil":     serviceAccountJSON(t, func(m map[string]any) { m["token_uri"] = "https://oauth2.googleapis.com.evil.com/token" }),
		"sa token_uri legacy lookalike": serviceAccountJSON(t, func(m map[string]any) {
			m["token_uri"] = "https://accounts.google.com.evil.com/o/oauth2/token"
		}),
		"ea token_url legacy host": externalAccountJSON(func(m map[string]any) { m["token_url"] = "https://accounts.google.com/o/oauth2/token" }),
		"sa token_uri port":        serviceAccountJSON(t, func(m map[string]any) { m["token_uri"] = "https://oauth2.googleapis.com:8443/token" }),
		"sa token_uri userinfo":    serviceAccountJSON(t, func(m map[string]any) { m["token_uri"] = "https://u:p@oauth2.googleapis.com/token" }),
		"sa universe":              serviceAccountJSON(t, func(m map[string]any) { m["universe_domain"] = "evil.example" }),

		"ea file":           externalAccountJSON(setSource("file", "/var/run/secrets/kubernetes.io/serviceaccount/token")),
		"ea url":            externalAccountJSON(setSource("url", "http://169.254.169.254/metadata/identity/oauth2/token")),
		"ea executable":     externalAccountJSON(setSource("executable", map[string]any{"command": "/bin/sh -c id"})),
		"ea headers":        externalAccountJSON(setSource("headers", map[string]any{"Metadata": "true"})),
		"ea region_url":     externalAccountJSON(setSource("region_url", "http://169.254.169.254/latest/meta-data/placement/availability-zone")),
		"ea imdsv2":         externalAccountJSON(setSource("imdsv2_session_token_url", "http://169.254.169.254/latest/api/token")),
		"ea format":         externalAccountJSON(setSource("format", map[string]any{"type": "text"})),
		"ea unknown source": externalAccountJSON(setSource("certificate", map[string]any{"use_default_certificate_config": true})),
		"ea azure env":      externalAccountJSON(setSource("environment_id", "azure1")),
		"ea verification url": externalAccountJSON(setSource("regional_cred_verification_url",
			"https://169.254.169.254/?Action=GetCallerIdentity")),
		"ea no source":         externalAccountJSON(func(m map[string]any) { delete(m, "credential_source") }),
		"ea file only":         externalAccountJSON(func(m map[string]any) { m["credential_source"] = map[string]any{"file": "/etc/passwd"} }),
		"ea no token_url":      externalAccountJSON(func(m map[string]any) { delete(m, "token_url") }),
		"ea token_url evil":    externalAccountJSON(func(m map[string]any) { m["token_url"] = "https://sts.evil.example/v1/token" }),
		"ea token_url http":    externalAccountJSON(func(m map[string]any) { m["token_url"] = "http://sts.googleapis.com/v1/token" }),
		"ea impersonation url": externalAccountJSON(func(m map[string]any) { m["service_account_impersonation_url"] = "https://attacker.example/x" }),
		"ea token_info_url":    externalAccountJSON(func(m map[string]any) { m["token_info_url"] = "http://10.0.0.1/introspect" }),
		"ea universe":          externalAccountJSON(func(m map[string]any) { m["universe_domain"] = "evil.example" }),
		"ea no audience":       externalAccountJSON(func(m map[string]any) { delete(m, "audience") }),
	}
	for name, doc := range cases {
		ts, err := GoogleTokenSourceFromJSON(t.Context(), doc)
		if err == nil {
			t.Errorf("%s accepted (ts=%v)", name, ts)
			continue
		}
		if strings.Contains(err.Error(), secretMarker) {
			t.Errorf("%s: error leaks credential material: %v", name, err)
		}
	}
	// The refusal says why, in terms an admin can act on.
	_, err := GoogleTokenSourceFromJSON(t.Context(), cases["ea file"])
	if err == nil || !strings.Contains(err.Error(), "credential_source.file") || !strings.Contains(err.Error(), "reads a file") {
		t.Errorf("file refusal = %v", err)
	}
}

// TestTokenFetchIsBounded: the token source outlives the request that built
// it and Sign cannot hand it a context, so its fetches need their own timeout.
func TestTokenFetchIsBounded(t *testing.T) {
	hc, ok := withTokenHTTPClient(t.Context()).Value(oauth2.HTTPClient).(*http.Client)
	if !ok || hc.Timeout <= 0 {
		t.Fatalf("token fetch client = %+v, want one with a timeout", hc)
	}
	mine := &http.Client{}
	ctx := context.WithValue(t.Context(), oauth2.HTTPClient, mine)
	if got := withTokenHTTPClient(ctx).Value(oauth2.HTTPClient); got != mine {
		t.Error("a client the caller put in the context must be kept")
	}
}

func TestGoogleDefaultTokenSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, serviceAccountJSON(t, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	ts, err := GoogleDefaultTokenSource(t.Context())
	if err != nil || ts == nil {
		t.Fatalf("ADC from GOOGLE_APPLICATION_CREDENTIALS: %v", err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	if _, err := GoogleDefaultTokenSource(t.Context()); err == nil {
		t.Error("a missing ADC file was accepted")
	}
}
