package promread

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"golang.org/x/oauth2"
)

// stubAwsCredentialsProvider implements aws.CredentialsProvider for
// unit tests — returns a fixed set of credentials (or a fixed error)
// without dialing STS / IMDS.
type stubAwsCredentialsProvider struct {
	creds aws.Credentials
	err   error
}

func (s stubAwsCredentialsProvider) Retrieve(_ context.Context) (aws.Credentials, error) {
	if s.err != nil {
		return aws.Credentials{}, s.err
	}
	return s.creds, nil
}

// recordingAwsSigner satisfies awsRequestSigner and captures the
// arguments it received — lets tests assert that Apply forwarded the
// right service / region / payload hash without verifying signature
// bytes (which is the SDK's responsibility, not ours).
type recordingAwsSigner struct {
	called      bool
	gotCreds    aws.Credentials
	gotPayload  string
	gotService  string
	gotRegion   string
	gotTime     time.Time
	gotRequest  *http.Request
	returnError error
}

func (r *recordingAwsSigner) SignHTTP(_ context.Context, creds aws.Credentials, req *http.Request, payloadHash, service, region string, signingTime time.Time) error {
	r.called = true
	r.gotCreds = creds
	r.gotPayload = payloadHash
	r.gotService = service
	r.gotRegion = region
	r.gotTime = signingTime
	r.gotRequest = req
	if r.returnError != nil {
		return r.returnError
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 fake-sig")
	return nil
}

// stubTokenSource is a fake oauth2.TokenSource for unit-testing
// providers that wrap one (gcpIam today; azure provider in S2.4
// uses a similar pattern via azidentity but with different shape).
type stubTokenSource struct {
	token string
	err   error
}

func (s stubTokenSource) Token() (*oauth2.Token, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &oauth2.Token{AccessToken: s.token, TokenType: "Bearer"}, nil
}

func TestNewProvider_EmptyModeFallsBackToNone(t *testing.T) {
	p, err := NewProvider(AuthConfig{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.Mode() != AuthNone {
		t.Errorf("expected none, got %q", p.Mode())
	}
}

func TestNewProvider_UnknownModeRejected(t *testing.T) {
	if _, err := NewProvider(AuthConfig{Mode: "supersecret"}); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestNewProvider_BasicAuthRequiresUsername(t *testing.T) {
	if _, err := NewProvider(AuthConfig{Mode: AuthBasicAuth}); err == nil {
		t.Fatal("expected error when username missing")
	}
	p, err := NewProvider(AuthConfig{Mode: AuthBasicAuth, BasicAuthUsername: "u"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.Mode() != AuthBasicAuth {
		t.Errorf("expected basicAuth, got %q", p.Mode())
	}
}

func TestNewProvider_BearerRequiresToken(t *testing.T) {
	if _, err := NewProvider(AuthConfig{Mode: AuthBearer}); err == nil {
		t.Fatal("expected error when token missing")
	}
	p, err := NewProvider(AuthConfig{Mode: AuthBearer, BearerToken: "abc"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.Mode() != AuthBearer {
		t.Errorf("expected bearer, got %q", p.Mode())
	}
}

func TestNoneProvider_AppliesNothing(t *testing.T) {
	p, _ := NewProvider(AuthConfig{Mode: AuthNone})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := p.Apply(req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("none should not set Authorization")
	}
}

func TestBasicAuthProvider_AppliesHeader(t *testing.T) {
	p, _ := NewProvider(AuthConfig{Mode: AuthBasicAuth, BasicAuthUsername: "alice", BasicAuthPassword: "s3cret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := p.Apply(req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	user, pass, ok := req.BasicAuth()
	if !ok {
		t.Fatal("BasicAuth not set")
	}
	if user != "alice" || pass != "s3cret" {
		t.Errorf("got user=%q pass=%q, want alice/s3cret", user, pass)
	}
}

func TestGcpIamProvider_AppliesBearerHeader(t *testing.T) {
	p := newGcpIamProviderWithSource(stubTokenSource{token: "abc123-from-gcp-metadata"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := p.Apply(req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := req.Header.Get("Authorization")
	if got != "Bearer abc123-from-gcp-metadata" {
		t.Errorf("got Authorization=%q, want Bearer abc123-from-gcp-metadata", got)
	}
}

func TestGcpIamProvider_PropagatesTokenSourceError(t *testing.T) {
	// If the metadata server is unreachable or returns 5xx,
	// Apply must surface the error so the caller (Reader.Collect)
	// can log + skip the cycle rather than emit unsigned requests.
	p := newGcpIamProviderWithSource(stubTokenSource{err: errors.New("metadata server unreachable")})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	err := p.Apply(req)
	if err == nil {
		t.Fatal("expected error from token source")
	}
	if !strings.Contains(err.Error(), "metadata server unreachable") {
		t.Errorf("error must wrap original cause, got: %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("Authorization header must NOT be set on token-source failure")
	}
}

func TestGcpIamProvider_ModeReturnsGcpIam(t *testing.T) {
	p := newGcpIamProviderWithSource(stubTokenSource{token: "x"})
	if p.Mode() != AuthGcpIam {
		t.Errorf("Mode: got %q want %q", p.Mode(), AuthGcpIam)
	}
}

func TestNewProvider_GcpIamDispatch(t *testing.T) {
	// Calls google.DefaultTokenSource for real. On most CI / dev
	// environments without GOOGLE_APPLICATION_CREDENTIALS or gcloud
	// ADC, this returns an error (no creds found). Either outcome is
	// acceptable — we're asserting that the dispatch happens and
	// returns SOME result, not that the metadata server is reachable.
	p, err := NewProvider(AuthConfig{Mode: AuthGcpIam})
	if err == nil {
		// Some creds were found (machine has gcloud ADC or
		// GOOGLE_APPLICATION_CREDENTIALS set). Provider should
		// be a gcpIamProvider regardless.
		if p.Mode() != AuthGcpIam {
			t.Errorf("expected gcpIam, got %q", p.Mode())
		}
	} else {
		// Most common case in CI/dev — no Google creds discoverable.
		// Error must mention gcpIam so operators can see what failed.
		if !strings.Contains(err.Error(), "gcpIam") {
			t.Errorf("error must mention gcpIam, got: %v", err)
		}
	}
}

func TestAwsSigV4Provider_AppliesSignedHeader(t *testing.T) {
	signer := &recordingAwsSigner{}
	p := newAwsSigV4ProviderWithDeps(
		stubAwsCredentialsProvider{creds: aws.Credentials{
			AccessKeyID:     "AKIA-test",
			SecretAccessKey: "secret-test",
			SessionToken:    "session-test",
			Source:          "test",
		}},
		signer,
		"us-east-1",
	)
	req := httptest.NewRequest(http.MethodGet, "https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-x/api/v1/query?query=up", nil)

	if err := p.Apply(req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !signer.called {
		t.Fatal("signer.SignHTTP was not called")
	}
	if signer.gotService != awsServiceAps {
		t.Errorf("service: got %q want %q", signer.gotService, awsServiceAps)
	}
	if signer.gotRegion != "us-east-1" {
		t.Errorf("region: got %q want us-east-1", signer.gotRegion)
	}
	if signer.gotCreds.AccessKeyID != "AKIA-test" {
		t.Errorf("creds AccessKeyID not propagated: got %q", signer.gotCreds.AccessKeyID)
	}
	if signer.gotPayload != emptyPayloadSHA256 {
		t.Errorf("payload hash for nil-body GET should be empty SHA, got %q", signer.gotPayload)
	}
	if req.Header.Get("Authorization") == "" {
		t.Error("signed request must have Authorization header set by the signer")
	}
}

func TestAwsSigV4Provider_PropagatesCredentialError(t *testing.T) {
	// IRSA chain failure (e.g. STS unreachable) must surface — caller
	// will log + skip the cycle rather than fire unsigned requests.
	signer := &recordingAwsSigner{}
	p := newAwsSigV4ProviderWithDeps(
		stubAwsCredentialsProvider{err: errors.New("STS unreachable")},
		signer,
		"us-east-1",
	)
	req := httptest.NewRequest(http.MethodGet, "https://x/api/v1/query?q=up", nil)

	err := p.Apply(req)
	if err == nil {
		t.Fatal("expected error from credential provider")
	}
	if !strings.Contains(err.Error(), "STS unreachable") {
		t.Errorf("error must wrap original cause, got: %v", err)
	}
	if signer.called {
		t.Error("signer must NOT be called when credentials retrieval fails")
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("no auth header on credential failure")
	}
}

func TestAwsSigV4Provider_PropagatesSignerError(t *testing.T) {
	signer := &recordingAwsSigner{returnError: errors.New("malformed request")}
	p := newAwsSigV4ProviderWithDeps(
		stubAwsCredentialsProvider{creds: aws.Credentials{AccessKeyID: "k", SecretAccessKey: "s"}},
		signer,
		"us-east-1",
	)
	req := httptest.NewRequest(http.MethodGet, "https://x/api/v1/query?q=up", nil)

	err := p.Apply(req)
	if err == nil {
		t.Fatal("expected error from signer")
	}
	if !strings.Contains(err.Error(), "malformed request") {
		t.Errorf("error must wrap signer error, got: %v", err)
	}
}

func TestAwsSigV4Provider_ModeReturnsAwsSigV4(t *testing.T) {
	p := newAwsSigV4ProviderWithDeps(stubAwsCredentialsProvider{}, &recordingAwsSigner{}, "us-east-1")
	if p.Mode() != AuthAwsSigV4 {
		t.Errorf("Mode: got %q want awsSigV4", p.Mode())
	}
}

func TestNewProvider_AwsSigV4RequiresRegion(t *testing.T) {
	_, err := NewProvider(AuthConfig{Mode: AuthAwsSigV4})
	if err == nil {
		t.Fatal("expected error when AwsRegion missing")
	}
	if !strings.Contains(err.Error(), "AwsRegion") {
		t.Errorf("error must mention AwsRegion, got: %v", err)
	}
}

func TestAwsPayloadHash(t *testing.T) {
	t.Run("nil body returns empty SHA", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		got, err := awsPayloadHash(req)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != emptyPayloadSHA256 {
			t.Errorf("got %q want %s", got, emptyPayloadSHA256)
		}
	})
	t.Run("non-nil body restored after hash", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
		_, err := awsPayloadHash(req)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		// Body must be re-readable after hashing — would break the
		// underlying http.Transport otherwise.
		body, _ := io.ReadAll(req.Body)
		if string(body) != "hello" {
			t.Errorf("body not restored, got %q want hello", body)
		}
	})
}

func TestBearerProvider_AppliesHeader(t *testing.T) {
	p, _ := NewProvider(AuthConfig{Mode: AuthBearer, BearerToken: "deadbeef"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := p.Apply(req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := req.Header.Get("Authorization")
	if !strings.HasPrefix(got, "Bearer ") || !strings.HasSuffix(got, "deadbeef") {
		t.Errorf("expected Bearer deadbeef, got %q", got)
	}
}
