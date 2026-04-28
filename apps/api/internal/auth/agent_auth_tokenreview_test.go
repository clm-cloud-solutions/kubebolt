package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubReviewer mocks TokenReviewer. Tests that use it can inspect the
// last call args + how many times it was invoked (for cache hit
// assertions).
type stubReviewer struct {
	res          *TokenReviewResult
	err          error
	callCount    int
	lastToken    string
	lastAudience string
}

func (s *stubReviewer) Review(_ context.Context, token, audience string) (*TokenReviewResult, error) {
	s.callCount++
	s.lastToken = token
	s.lastAudience = audience
	return s.res, s.err
}

func saResult(name, namespace string, audiences ...string) *TokenReviewResult {
	return &TokenReviewResult{
		Authenticated: true,
		Username:      "system:serviceaccount:" + namespace + ":" + name,
		UID:           "uid-" + name,
		Audiences:     audiences,
	}
}

func TestTokenReviewAuth_HappyPath(t *testing.T) {
	rev := &stubReviewer{res: saResult("kubebolt-agent", "kubebolt-system", "kubebolt-backend")}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "tenant-default", "cluster-uid-xyz", time.Minute)

	md := mdWith(MetadataAuthorization, "Bearer eyJfake.sa.token")
	id, err := auth.Authenticate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Mode != ModeTokenReview {
		t.Errorf("Mode = %s, want %s", id.Mode, ModeTokenReview)
	}
	if id.TenantID != "tenant-default" || id.ClusterID != "cluster-uid-xyz" {
		t.Errorf("identity tenant/cluster = %s/%s, want tenant-default/cluster-uid-xyz", id.TenantID, id.ClusterID)
	}
	if id.SAName != "kubebolt-agent" || id.SANamespace != "kubebolt-system" {
		t.Errorf("SA = %s/%s, want kubebolt-system/kubebolt-agent", id.SANamespace, id.SAName)
	}
	if rev.lastAudience != "kubebolt-backend" {
		t.Errorf("audience passed to reviewer = %q, want %q", rev.lastAudience, "kubebolt-backend")
	}
	if rev.lastToken != "eyJfake.sa.token" {
		t.Errorf("token passed to reviewer = %q, want eyJfake.sa.token", rev.lastToken)
	}
}

func TestTokenReviewAuth_NotAuthenticated(t *testing.T) {
	rev := &stubReviewer{res: &TokenReviewResult{Authenticated: false, Error: "token expired"}}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)
	_, err := auth.Authenticate(context.Background(), mdWith(MetadataAuthorization, "Bearer fake"), nil)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestTokenReviewAuth_AudienceMismatch(t *testing.T) {
	// Apiserver authenticated the token but for a different service.
	// Reject — this is the cross-service replay guard.
	rev := &stubReviewer{res: saResult("kubebolt-agent", "kubebolt-system", "some-other-service")}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)
	_, err := auth.Authenticate(context.Background(), mdWith(MetadataAuthorization, "Bearer fake"), nil)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid for audience mismatch, got %v", err)
	}
}

func TestTokenReviewAuth_AudienceEmpty(t *testing.T) {
	// Apiserver returned empty audiences (token had none) → reject.
	rev := &stubReviewer{res: saResult("kubebolt-agent", "kubebolt-system" /* no audiences */)}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)
	_, err := auth.Authenticate(context.Background(), mdWith(MetadataAuthorization, "Bearer fake"), nil)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid when audience list is empty, got %v", err)
	}
}

func TestTokenReviewAuth_NotServiceAccountToken(t *testing.T) {
	// Authenticated, audience matches, but the subject is a human user
	// — not what we accept on the agent channel.
	rev := &stubReviewer{
		res: &TokenReviewResult{
			Authenticated: true,
			Username:      "alice@example.com",
			Audiences:     []string{"kubebolt-backend"},
		},
	}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)
	_, err := auth.Authenticate(context.Background(), mdWith(MetadataAuthorization, "Bearer fake"), nil)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid for non-SA token, got %v", err)
	}
}

func TestTokenReviewAuth_ReviewerError(t *testing.T) {
	rev := &stubReviewer{err: errors.New("apiserver unreachable")}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)
	_, err := auth.Authenticate(context.Background(), mdWith(MetadataAuthorization, "Bearer fake"), nil)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid wrapping reviewer error, got %v", err)
	}
}

func TestTokenReviewAuth_MissingToken(t *testing.T) {
	rev := &stubReviewer{}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)
	if _, err := auth.Authenticate(context.Background(), mdWith(), nil); !errors.Is(err, ErrMissingToken) {
		t.Errorf("expected ErrMissingToken, got %v", err)
	}
	if rev.callCount != 0 {
		t.Errorf("reviewer must not be called without a token, got %d invocations", rev.callCount)
	}
}

func TestTokenReviewAuth_CacheHitsSkipReviewer(t *testing.T) {
	rev := &stubReviewer{res: saResult("agent", "kubebolt-system", "kubebolt-backend")}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)
	md := mdWith(MetadataAuthorization, "Bearer same.token")

	for i := 0; i < 3; i++ {
		if _, err := auth.Authenticate(context.Background(), md, nil); err != nil {
			t.Fatalf("auth #%d: %v", i, err)
		}
	}
	if rev.callCount != 1 {
		t.Errorf("expected 1 reviewer call (rest cached), got %d", rev.callCount)
	}
	auth.InvalidateCache()
	if _, err := auth.Authenticate(context.Background(), md, nil); err != nil {
		t.Fatal(err)
	}
	if rev.callCount != 2 {
		t.Errorf("expected 2 reviewer calls after InvalidateCache, got %d", rev.callCount)
	}
}

func TestTokenReviewAuth_CacheNotShared_AcrossDifferentTokens(t *testing.T) {
	// Two distinct tokens must each hit the reviewer.
	rev := &stubReviewer{res: saResult("agent", "kubebolt-system", "kubebolt-backend")}
	auth := NewTokenReviewAuth(rev, "kubebolt-backend", "t1", "c1", time.Minute)

	if _, err := auth.Authenticate(context.Background(), mdWith(MetadataAuthorization, "Bearer token-A"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(context.Background(), mdWith(MetadataAuthorization, "Bearer token-B"), nil); err != nil {
		t.Fatal(err)
	}
	if rev.callCount != 2 {
		t.Errorf("expected 2 reviewer calls for 2 distinct tokens, got %d", rev.callCount)
	}
}

func TestParseServiceAccountUsername(t *testing.T) {
	cases := []struct {
		username string
		wantName string
		wantNS   string
		wantErr  bool
	}{
		{"system:serviceaccount:kubebolt-system:agent", "agent", "kubebolt-system", false},
		{"system:serviceaccount:default:default", "default", "default", false},
		// Permissive: SplitN with N=2 keeps trailing colons in the name part.
		// In practice K8s names cannot contain colons, but this branch
		// pins parser behavior for trustworthy authenticated input.
		{"system:serviceaccount:ns:weird:name", "weird:name", "ns", false},
		{"alice@example.com", "", "", true},
		{"system:serviceaccount:onlyone", "", "", true},
		{"system:serviceaccount:", "", "", true},
		{"system:serviceaccount:ns:", "", "", true}, // empty name
		{"system:serviceaccount::name", "", "", true}, // empty namespace
		{"", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.username, func(t *testing.T) {
			name, ns, err := parseServiceAccountUsername(c.username)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if name != c.wantName || ns != c.wantNS {
				t.Errorf("parts = %s/%s, want %s/%s", ns, name, c.wantNS, c.wantName)
			}
		})
	}
}
