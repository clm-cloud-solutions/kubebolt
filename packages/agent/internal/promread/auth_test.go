package promread

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
