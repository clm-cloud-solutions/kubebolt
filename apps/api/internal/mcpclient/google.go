package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// CloudPlatformScope is the OAuth scope for Google's MCP endpoints. The
// per-service scopes they advertise are mostly read-write, so a narrower
// scope would not be a read-only barrier anyway; IAM and the gateway's
// allowlist are.
const CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// maxCredentialJSON bounds a credential document: a service account key is
// about 2.3 KB.
const maxCredentialJSON = 64 << 10

// tokenFetchTimeout bounds one token fetch (JWT exchange, STS exchange,
// impersonation). Sign has no context to hand the token source — the oauth2
// TokenSource interface takes none — so without it a token endpoint that
// hangs would hold a tool call past its own timeout, on http.DefaultClient,
// which has none.
const tokenFetchTimeout = 30 * time.Second

// withTokenHTTPClient gives the token source's context an HTTP client with a
// timeout, unless the caller already put one there.
func withTokenHTTPClient(ctx context.Context) context.Context {
	if _, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: tokenFetchTimeout})
}

type bearerSigner struct {
	ts          oauth2.TokenSource
	userProject string
}

// NewBearerSigner authenticates every request with an OAuth2 access token
// from ts and, when userProject is set, bills and quotas it against that
// project (x-goog-user-project) — mandatory for user credentials and advisable
// for every other kind. Tokens are cached and refreshed before expiry.
func NewBearerSigner(ts oauth2.TokenSource, userProject string) Signer {
	if ts != nil {
		ts = oauth2.ReuseTokenSource(nil, ts)
	}
	return &bearerSigner{ts: ts, userProject: userProject}
}

func (s *bearerSigner) Sign(_ context.Context, req *http.Request, _ []byte) error {
	if s.ts == nil {
		return errors.New("google: no token source")
	}
	tok, err := s.ts.Token()
	if err != nil {
		return fmt.Errorf("google: get token: %w", err)
	}
	if tok.AccessToken == "" {
		return errors.New("google: empty access token")
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if s.userProject != "" {
		req.Header.Set("x-goog-user-project", s.userProject)
	}
	return nil
}

// googleCredentialFile is what GoogleTokenSourceFromJSON inspects before the
// library sees the document.
type googleCredentialFile struct {
	Type           string `json:"type"`
	UniverseDomain string `json:"universe_domain"`

	// service_account
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`

	// external_account (workload identity federation)
	Audience                       string                     `json:"audience"`
	TokenURL                       string                     `json:"token_url"`
	TokenInfoURL                   string                     `json:"token_info_url"`
	ServiceAccountImpersonationURL string                     `json:"service_account_impersonation_url"`
	CredentialSource               map[string]json.RawMessage `json:"credential_source"`
}

// GoogleTokenSourceFromJSON turns a credential document an org uploaded into a
// cloud-platform token source, after validating it — the library's own
// warning is that an unvalidated external credential config can make the
// host read local files, run programs or call arbitrary URLs with its own
// identity.
//
// Accepted: a service account key, or a workload identity federation
// (external_account) config whose token exchange and impersonation endpoints
// are Google's and whose credential_source reads nothing from this machine
// (no file, no executable, no URL). Everything else — user credentials,
// impersonated_service_account, gdch keys — is refused.
//
// The token source keeps a context for every future refresh; its cancellation
// is dropped here so a request-scoped ctx cannot kill the connector later, and
// each fetch is bounded by tokenFetchTimeout instead.
func GoogleTokenSourceFromJSON(ctx context.Context, data []byte) (oauth2.TokenSource, error) {
	credType, err := validateGoogleCredential(data)
	if err != nil {
		return nil, err
	}
	creds, err := google.CredentialsFromJSONWithType(withTokenHTTPClient(context.WithoutCancel(ctx)), data, credType, CloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("google: load credentials: %w", err)
	}
	return creds.TokenSource, nil
}

// GoogleDefaultTokenSource is the ambient identity of the machine KubeBolt runs
// on (Application Default Credentials: GKE workload identity, the metadata
// server, gcloud's ADC file). Only single-tenant installs may use it: in SaaS
// that identity is KubeBolt's, not the org's.
func GoogleDefaultTokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	ts, err := google.DefaultTokenSource(withTokenHTTPClient(context.WithoutCancel(ctx)), CloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("google: application default credentials: %w", err)
	}
	return ts, nil
}

// credentialSourceRefusals explains, per field, why a credential_source key
// is refused. Any key not listed here or in credentialSourceAllowed is
// refused too: new source kinds must be reviewed before they are accepted.
var credentialSourceRefusals = map[string]string{
	"file":                     "reads a file from the KubeBolt host",
	"executable":               "runs a program on the KubeBolt host",
	"url":                      "calls a URL from the KubeBolt host",
	"headers":                  "only applies to URL-sourced credentials",
	"format":                   "only applies to file- or URL-sourced credentials",
	"region_url":               "calls a URL from the KubeBolt host",
	"imdsv2_session_token_url": "calls a URL from the KubeBolt host",
}

// validateGoogleCredential checks a credential document and returns its type.
// Errors name the offending field, never its value.
func validateGoogleCredential(data []byte) (google.CredentialsType, error) {
	if len(data) == 0 {
		return "", errors.New("google: credential JSON is empty")
	}
	if len(data) > maxCredentialJSON {
		return "", fmt.Errorf("google: credential JSON is larger than %d KiB", maxCredentialJSON>>10)
	}
	var f googleCredentialFile
	if err := json.Unmarshal(data, &f); err != nil {
		return "", errors.New("google: credential is not a valid JSON document")
	}
	if f.UniverseDomain != "" && f.UniverseDomain != "googleapis.com" {
		return "", errors.New("google: universe_domain must be googleapis.com")
	}
	switch f.Type {
	case string(google.ServiceAccount):
		if f.ClientEmail == "" || f.PrivateKey == "" {
			return "", errors.New("google: service account key is missing client_email or private_key")
		}
		if f.TokenURI != "" {
			// Keys issued before oauth2.googleapis.com existed still name
			// the old endpoint, and still work.
			if err := checkGoogleURL(f.TokenURI, "token_uri", "accounts.google.com"); err != nil {
				return "", err
			}
		}
		return google.ServiceAccount, nil

	case string(google.ExternalAccount):
		if f.Audience == "" {
			return "", errors.New("google: external_account config is missing audience")
		}
		if f.TokenURL == "" {
			return "", errors.New("google: external_account config is missing token_url")
		}
		if err := checkGoogleURL(f.TokenURL, "token_url"); err != nil {
			return "", err
		}
		for _, u := range []struct{ field, value string }{
			{"service_account_impersonation_url", f.ServiceAccountImpersonationURL},
			{"token_info_url", f.TokenInfoURL},
		} {
			if u.value != "" {
				if err := checkGoogleURL(u.value, u.field); err != nil {
					return "", err
				}
			}
		}
		if err := checkCredentialSource(f.CredentialSource); err != nil {
			return "", err
		}
		return google.ExternalAccount, nil

	case "":
		return "", errors.New("google: credential JSON has no type")
	default:
		return "", fmt.Errorf("google: credential type %q is not accepted; use a service account key or a workload identity federation config", f.Type)
	}
}

// checkCredentialSource admits only a source that needs nothing from the
// KubeBolt host beyond its environment: AWS (environment_id "aws1") with the
// regional verification URL, which is signed and embedded in the subject
// token rather than called.
func checkCredentialSource(cs map[string]json.RawMessage) error {
	if len(cs) == 0 {
		return errors.New("google: external_account config is missing credential_source")
	}
	keys := make([]string, 0, len(cs))
	for k := range cs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error for a document with several problems
	for _, k := range keys {
		switch k {
		case "environment_id":
			var v string
			if json.Unmarshal(cs[k], &v) != nil || !strings.HasPrefix(strings.ToLower(v), "aws") {
				return errors.New("google: credential_source.environment_id is not supported")
			}
		case "regional_cred_verification_url":
			var v string
			if json.Unmarshal(cs[k], &v) != nil {
				return errors.New("google: credential_source.regional_cred_verification_url must be a string")
			}
			if err := checkAWSSTSURL(v); err != nil {
				return err
			}
		default:
			if why, ok := credentialSourceRefusals[k]; ok {
				return fmt.Errorf("google: credential_source.%s is not accepted: it %s", k, why)
			}
			return fmt.Errorf("google: credential_source.%s is not accepted", k)
		}
	}
	if _, ok := cs["environment_id"]; !ok {
		return errors.New("google: credential_source must be an AWS environment source; file, URL and executable sources are not accepted")
	}
	return nil
}

// checkGoogleURL requires an https URL on a *.googleapis.com host, the only
// place a token exchange or impersonation for Google Cloud happens, or on one
// of the exact hosts in also.
func checkGoogleURL(raw, field string, also ...string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return fmt.Errorf("google: %s must be an https URL", field)
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("google: %s must use the default https port", field)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if !strings.HasSuffix(host, ".googleapis.com") && !slices.Contains(also, host) {
		return fmt.Errorf("google: %s must point at a googleapis.com host", field)
	}
	return nil
}

// checkAWSSTSURL requires the AWS STS GetCallerIdentity endpoint the library
// templates by region.
func checkAWSSTSURL(raw string) error {
	u, err := url.Parse(strings.ReplaceAll(raw, "{region}", "region"))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return errors.New("google: credential_source.regional_cred_verification_url must be an https URL")
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasPrefix(host, "sts.") || !(strings.HasSuffix(host, ".amazonaws.com") || strings.HasSuffix(host, ".amazonaws.com.cn")) {
		return errors.New("google: credential_source.regional_cred_verification_url must point at AWS STS")
	}
	return nil
}
