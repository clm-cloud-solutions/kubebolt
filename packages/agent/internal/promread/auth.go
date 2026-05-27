package promread

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// AuthMode enumerates the provider implementations. S1 ships three
// (none/basicAuth/bearer); S2 of the 1.13 cycle adds three managed-
// cloud variants (gcpIam, awsSigV4, azureWorkloadIdentity).
type AuthMode string

const (
	AuthNone                  AuthMode = "none"
	AuthBasicAuth             AuthMode = "basicAuth"
	AuthBearer                AuthMode = "bearer"
	AuthGcpIam                AuthMode = "gcpIam"
)

// gcpScopeMonitoringRead is the OAuth scope required to query GMP
// via /api/v1/query_range. Pre-flight Session 10 (2026-05-26)
// confirmed this is the scope `gcloud auth print-access-token`
// requests against the metadata server in a Workload Identity-
// bound pod.
const gcpScopeMonitoringRead = "https://www.googleapis.com/auth/monitoring.read"

// AuthConfig is the operator-set auth block, mirrors helm values
// `agent.promRead.auth.*`. Only one credential set is honored per
// Mode — extras are ignored by NewProvider.
type AuthConfig struct {
	Mode AuthMode

	BasicAuthUsername string
	BasicAuthPassword string
	BearerToken       string
}

// Provider applies the auth method to an outgoing HTTP request.
// Implementations are pure — no I/O, no token refresh — so a
// single Provider instance is safe to share across concurrent
// requests. Token refresh (needed for SigV4 / cloud-managed
// providers) belongs in a wrapper added during S2.
type Provider interface {
	Apply(req *http.Request) error
	Mode() AuthMode
}

// NewProvider returns the Provider matching cfg.Mode. An empty Mode
// behaves as AuthNone for forward compat with chart defaults — the
// helm value can be omitted to mean "no auth" without operators
// needing to type `mode: none` explicitly.
func NewProvider(cfg AuthConfig) (Provider, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = AuthNone
	}
	switch mode {
	case AuthNone:
		return noneProvider{}, nil
	case AuthBasicAuth:
		if cfg.BasicAuthUsername == "" {
			return nil, errors.New("basicAuth requires username")
		}
		return basicAuthProvider{user: cfg.BasicAuthUsername, pass: cfg.BasicAuthPassword}, nil
	case AuthBearer:
		if cfg.BearerToken == "" {
			return nil, errors.New("bearer requires token")
		}
		return bearerProvider{token: cfg.BearerToken}, nil
	case AuthGcpIam:
		return newGcpIamProvider(context.Background())
	default:
		return nil, fmt.Errorf("unknown auth mode: %q", mode)
	}
}

type noneProvider struct{}

func (noneProvider) Apply(_ *http.Request) error { return nil }
func (noneProvider) Mode() AuthMode              { return AuthNone }

type basicAuthProvider struct {
	user string
	pass string
}

func (p basicAuthProvider) Apply(req *http.Request) error {
	req.SetBasicAuth(p.user, p.pass)
	return nil
}
func (basicAuthProvider) Mode() AuthMode { return AuthBasicAuth }

type bearerProvider struct {
	token string
}

func (p bearerProvider) Apply(req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+p.token)
	return nil
}
func (bearerProvider) Mode() AuthMode { return AuthBearer }

// gcpIamProvider mints Google IAM access tokens via the ambient
// credential chain. In production this resolves to:
//
//   1. GOOGLE_APPLICATION_CREDENTIALS env (service account key JSON
//      — dev-only path, NOT recommended for production)
//   2. gcloud user creds at ~/.config/gcloud/... (local dev)
//   3. GCE/GKE metadata server (the production path inside a pod
//      with KSA bound to a GSA via Workload Identity — verified
//      in vivo Session 10 2026-05-26)
//
// The TokenSource handles cache + refresh internally; tokens are
// minted on first Apply() and refreshed transparently before
// expiry (default ~1h lifetime).
type gcpIamProvider struct {
	ts oauth2.TokenSource
}

// newGcpIamProvider constructs a Provider using the ambient Google
// credential chain. Returns an error when no credentials are
// discoverable (which on a dev laptop without gcloud login or
// inside a non-GKE cluster means the operator misconfigured the
// promRead auth mode — fail loud at boot rather than silently emit
// 401s every poll).
func newGcpIamProvider(ctx context.Context) (*gcpIamProvider, error) {
	ts, err := google.DefaultTokenSource(ctx, gcpScopeMonitoringRead)
	if err != nil {
		return nil, fmt.Errorf("gcpIam: default token source: %w", err)
	}
	return &gcpIamProvider{ts: ts}, nil
}

// newGcpIamProviderWithSource is a test seam — lets unit tests
// inject a stub TokenSource without dialing the metadata server.
// Not exported because production callers should always go through
// newGcpIamProvider so the ambient discovery happens.
func newGcpIamProviderWithSource(ts oauth2.TokenSource) *gcpIamProvider {
	return &gcpIamProvider{ts: ts}
}

func (p *gcpIamProvider) Apply(req *http.Request) error {
	tok, err := p.ts.Token()
	if err != nil {
		return fmt.Errorf("gcpIam: get token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return nil
}

func (*gcpIamProvider) Mode() AuthMode { return AuthGcpIam }
