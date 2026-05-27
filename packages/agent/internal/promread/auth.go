package promread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
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
	AuthAwsSigV4              AuthMode = "awsSigV4"
	AuthAzureWorkloadIdentity AuthMode = "azureWorkloadIdentity"
)

// gcpScopeMonitoringRead is the OAuth scope required to query GMP
// via /api/v1/query_range. Pre-flight Session 10 (2026-05-26)
// confirmed this is the scope `gcloud auth print-access-token`
// requests against the metadata server in a Workload Identity-
// bound pod.
const gcpScopeMonitoringRead = "https://www.googleapis.com/auth/monitoring.read"

// awsServiceAps is the AWS service identifier for Amazon Managed
// Prometheus. Required as a canonical-request segment in SigV4.
const awsServiceAps = "aps"

// emptyPayloadSHA256 is the SHA-256 hex digest of the empty byte
// string. SigV4 requires this in the canonical request for any
// request without a body — which is every promread query_range
// call (all GETs, no body).
const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// azurePrometheusScope is the OAuth scope required to query Azure
// Monitor managed Prometheus. Pre-flight Session 09 (2026-05-27)
// confirmed this is what `az account get-access-token --resource
// "https://prometheus.monitor.azure.com"` requests against the
// federation endpoint.
const azurePrometheusScope = "https://prometheus.monitor.azure.com/.default"

// AuthConfig is the operator-set auth block, mirrors helm values
// `agent.promRead.auth.*`. Only one credential set is honored per
// Mode — extras are ignored by NewProvider.
//
// Mode-specific fields:
//   - basicAuth: BasicAuthUsername (required), BasicAuthPassword
//   - bearer:    BearerToken (required)
//   - gcpIam:    no fields — ambient via Workload Identity / ADC
//   - awsSigV4:  AwsRegion (required) — credentials ambient via
//                IRSA / env / instance profile
type AuthConfig struct {
	Mode AuthMode

	BasicAuthUsername string
	BasicAuthPassword string
	BearerToken       string
	AwsRegion         string
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
	case AuthAwsSigV4:
		if cfg.AwsRegion == "" {
			return nil, errors.New("awsSigV4 requires AwsRegion")
		}
		return newAwsSigV4Provider(context.Background(), cfg.AwsRegion)
	case AuthAzureWorkloadIdentity:
		return newAzureWorkloadIdentityProvider()
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

// awsSigV4Provider signs outgoing HTTP requests with AWS SigV4 for
// the Amazon Managed Service for Prometheus (`aps`) service.
//
// Credential chain via aws-sdk-go-v2's LoadDefaultConfig:
//
//  1. Env vars (AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY — dev path)
//  2. Shared credentials file (~/.aws/credentials — local dev)
//  3. **IAM Roles for Service Accounts (IRSA)** — the production
//     path inside an EKS pod with KSA annotated with
//     `eks.amazonaws.com/role-arn=...`. The webhook injects
//     AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE; the SDK chains
//     STS:AssumeRoleWithWebIdentity transparently. Verified
//     in vivo Session 08 (2026-05-27).
//  4. EC2 IMDS instance profile (legacy / non-EKS EC2 path)
//
// SigV4 requires per-request signing (not a long-lived token like
// gcpIam / azureWorkloadIdentity). The Apply call:
//   - Retrieves current credentials (auto-refreshed by the SDK)
//   - Hashes the request body (empty SHA for GET requests)
//   - Adds Authorization + X-Amz-Date + X-Amz-Security-Token
//     headers to the request
//
// Promread only fires GET /api/v1/query_range, so body is always
// nil and emptyPayloadSHA256 is the correct hash.
type awsSigV4Provider struct {
	creds  aws.CredentialsProvider
	signer awsRequestSigner
	region string
}

// awsRequestSigner is the minimal interface the provider needs from
// v4.Signer. Exposed so unit tests can stub the signer without
// holding a real *v4.Signer (which can sign anything but needs no
// state for testability of OUR call site).
type awsRequestSigner interface {
	SignHTTP(ctx context.Context, creds aws.Credentials, r *http.Request, payloadHash, service, region string, signingTime time.Time) error
}

// newAwsSigV4Provider constructs a Provider that signs SigV4 against
// AMP. Returns an error when LoadDefaultConfig can't resolve any
// credential chain — fail loud at boot rather than silently emit
// unsigned requests every poll.
func newAwsSigV4Provider(ctx context.Context, region string) (*awsSigV4Provider, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("awsSigV4: load default config: %w", err)
	}
	return &awsSigV4Provider{
		creds:  cfg.Credentials,
		signer: sdkSignerAdapter{s: v4.NewSigner()},
		region: region,
	}, nil
}

// sdkSignerAdapter wraps *v4.Signer so it satisfies awsRequestSigner.
// The SDK's signature ends with variadic SignerOptions; we don't use
// any so a tiny shim keeps our interface clean for testing.
type sdkSignerAdapter struct {
	s *v4.Signer
}

func (a sdkSignerAdapter) SignHTTP(ctx context.Context, creds aws.Credentials, r *http.Request, payloadHash, service, region string, signingTime time.Time) error {
	return a.s.SignHTTP(ctx, creds, r, payloadHash, service, region, signingTime)
}

// azureWorkloadIdentityProvider mints Azure Entra ID access tokens
// via the Workload Identity federation flow. Inside an AKS pod with
// label `azure.workload.identity/use: "true"` and a KSA annotated
// with `azure.workload.identity/client-id`, the WI webhook auto-
// injects four env vars (AZURE_CLIENT_ID, AZURE_TENANT_ID,
// AZURE_FEDERATED_TOKEN_FILE, AZURE_AUTHORITY_HOST). The Azure SDK's
// NewWorkloadIdentityCredential reads them and handles the federated
// JWT exchange transparently — same chain that
// `az login --service-principal --federated-token` exercises (verified
// in vivo Session 09 2026-05-27).
//
// Tokens are cached + refreshed inside the TokenCredential; our
// Apply call is a thin shim.
type azureWorkloadIdentityProvider struct {
	cred azureTokenCredential
}

// azureTokenCredential is the minimal interface the provider needs
// from azcore.TokenCredential. Stub-able for unit tests so we don't
// have to dial the Entra ID token endpoint or read a fake token
// file off disk just to verify Apply's wiring.
type azureTokenCredential interface {
	GetToken(ctx context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error)
}

// newAzureWorkloadIdentityProvider constructs a Provider via the
// Azure SDK's WI credential. Returns an error when the WI env vars
// aren't present (e.g. running outside an AKS pod with the webhook
// label) — fail loud at boot rather than silently emit 401s every
// poll.
func newAzureWorkloadIdentityProvider() (*azureWorkloadIdentityProvider, error) {
	cred, err := azidentity.NewWorkloadIdentityCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azureWorkloadIdentity: new credential: %w", err)
	}
	return &azureWorkloadIdentityProvider{cred: cred}, nil
}

// newAzureWorkloadIdentityProviderWithCred is the test seam — lets
// unit tests inject a stub TokenCredential.
func newAzureWorkloadIdentityProviderWithCred(cred azureTokenCredential) *azureWorkloadIdentityProvider {
	return &azureWorkloadIdentityProvider{cred: cred}
}

func (p *azureWorkloadIdentityProvider) Apply(req *http.Request) error {
	tok, err := p.cred.GetToken(req.Context(), policy.TokenRequestOptions{
		Scopes: []string{azurePrometheusScope},
	})
	if err != nil {
		return fmt.Errorf("azureWorkloadIdentity: get token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	return nil
}

func (*azureWorkloadIdentityProvider) Mode() AuthMode { return AuthAzureWorkloadIdentity }

// newAwsSigV4ProviderWithDeps is the test seam — lets unit tests
// inject stub credential + signer impls without dialing AWS.
func newAwsSigV4ProviderWithDeps(creds aws.CredentialsProvider, signer awsRequestSigner, region string) *awsSigV4Provider {
	return &awsSigV4Provider{creds: creds, signer: signer, region: region}
}

func (p *awsSigV4Provider) Apply(req *http.Request) error {
	ctx := req.Context()
	creds, err := p.creds.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("awsSigV4: retrieve credentials: %w", err)
	}
	payloadHash, err := awsPayloadHash(req)
	if err != nil {
		return fmt.Errorf("awsSigV4: hash payload: %w", err)
	}
	if err := p.signer.SignHTTP(ctx, creds, req, payloadHash, awsServiceAps, p.region, time.Now()); err != nil {
		return fmt.Errorf("awsSigV4: sign: %w", err)
	}
	return nil
}

func (*awsSigV4Provider) Mode() AuthMode { return AuthAwsSigV4 }

// awsPayloadHash returns the SHA-256 hex digest of the request body.
// For GET requests (the only shape promread emits) body is nil and
// we return the well-known empty-string digest. For non-GET shapes
// the body is read into memory, hashed, and restored — keeps the
// Apply call body-agnostic in case future code introduces POST.
func awsPayloadHash(req *http.Request) (string, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return emptyPayloadSHA256, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return "", err
	}
	// Restore body so the underlying transport can re-read it.
	req.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
