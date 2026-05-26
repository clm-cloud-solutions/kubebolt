package promread

import (
	"errors"
	"fmt"
	"net/http"
)

// AuthMode enumerates the provider implementations. S1 ships three;
// S2 of the 1.13 cycle adds three managed-cloud variants
// (AwsSigV4, AzureWorkloadIdentity, GcpIam).
type AuthMode string

const (
	AuthNone      AuthMode = "none"
	AuthBasicAuth AuthMode = "basicAuth"
	AuthBearer    AuthMode = "bearer"
)

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
