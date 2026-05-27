package promread

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

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
