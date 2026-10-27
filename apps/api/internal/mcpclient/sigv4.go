package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AWSMCPService is the SigV4 service name of the AWS MCP Server. Any other
// value is answered 401 -32001 "Credential should be scoped to correct
// service: 'aws-mcp'".
const AWSMCPService = "aws-mcp"

// MaxSessionPolicyChars is STS's limit on an inline session policy's
// plaintext; a longer one fails AssumeRole with PackedPolicyTooLarge.
const MaxSessionPolicyChars = 2048

// httpSigner is the part of *v4.Signer the SigV4 signer uses; a seam for
// tests to record what gets signed.
type httpSigner interface {
	SignHTTP(ctx context.Context, creds aws.Credentials, r *http.Request, payloadHash, service, region string, signingTime time.Time, optFns ...func(*v4.SignerOptions)) error
}

type sigV4Signer struct {
	creds   aws.CredentialsProvider
	signer  httpSigner
	service string
	region  string
	now     func() time.Time
}

// NewSigV4Signer signs every request with AWS Signature V4. service is
// AWSMCPService for the AWS MCP Server and region is the HOST region of the
// endpoint (us-east-1 or eu-central-1), not the region the calls target — that
// travels in _meta.AWS_REGION. creds is wrapped in an aws.CredentialsCache
// unless it already is one, so assumed-role credentials are fetched once per
// lifetime rather than once per request.
func NewSigV4Signer(creds aws.CredentialsProvider, service, region string) Signer {
	return newSigV4Signer(creds, v4.NewSigner(), service, region)
}

func newSigV4Signer(creds aws.CredentialsProvider, signer httpSigner, service, region string) *sigV4Signer {
	if creds != nil {
		if _, cached := creds.(*aws.CredentialsCache); !cached {
			creds = aws.NewCredentialsCache(creds)
		}
	}
	return &sigV4Signer{creds: creds, signer: signer, service: service, region: region, now: time.Now}
}

func (s *sigV4Signer) Sign(ctx context.Context, req *http.Request, body []byte) error {
	if s.creds == nil {
		return errors.New("sigv4: no credentials provider")
	}
	creds, err := s.creds.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("sigv4: retrieve credentials: %w", err)
	}
	sum := sha256.Sum256(body)
	// Connection is hop-by-hop: an intermediary may rewrite it, which would
	// break a signature that covered it. The official proxy drops it too.
	req.Header.Del("Connection")
	if err := s.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), s.service, s.region, s.now()); err != nil {
		return fmt.Errorf("sigv4: sign: %w", err)
	}
	return nil
}

var (
	reAWSProfile      = regexp.MustCompile(`^[A-Za-z0-9_.+=,@-]{1,64}$`)
	reRoleSessionName = regexp.MustCompile(`^[\w+=,.@-]{2,64}$`)
	reRoleARN         = regexp.MustCompile(`^arn:aws[a-z-]*:iam::\d{12}:role/[\w+=,.@/-]{1,512}$`)
	// reExternalID is STS's own character set for ExternalId (its 2-1224
	// length is checked beside it; Go's regexp caps repeats at 1000). It is
	// checked here because STS answers a violation with "Value '<the id>' at
	// 'externalId' failed to satisfy constraint", and that error would carry
	// the secret into logs and the connector's lastError.
	reExternalID = regexp.MustCompile(`^[\w+=,.@:/-]+$`)
)

// LoadAWSConfig resolves KubeBolt's own AWS identity through the SDK default
// chain — environment, shared files, IRSA web identity, container and EC2
// instance roles — optionally pinned to a named shared-config profile. It is
// the base identity that assumes customer roles, and on single-tenant
// installs the ambient identity itself.
func LoadAWSConfig(ctx context.Context, region, profile string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if profile != "" {
		if !reAWSProfile.MatchString(profile) {
			return aws.Config{}, fmt.Errorf("aws: invalid profile name")
		}
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("aws: load config: %w", err)
	}
	return cfg, nil
}

// AssumeRoleCredentials returns cached credentials for roleARN, assumed from
// the base identity with the customer's ExternalId (the confused-deputy guard
// that ties the role to this org) and an inline session policy that narrows
// the role to what one consumer may read. externalID and sessionPolicy may be
// empty; sessionName defaults to "kubebolt-mcp".
func AssumeRoleCredentials(base aws.Config, roleARN, externalID, sessionPolicy, sessionName string) (aws.CredentialsProvider, error) {
	if !reRoleARN.MatchString(roleARN) {
		return nil, errors.New("aws: role ARN must look like arn:aws:iam::<account>:role/<name>")
	}
	if externalID != "" && (len(externalID) < 2 || len(externalID) > 1224 || !reExternalID.MatchString(externalID)) {
		// Never echo the value: the external ID is the org's secret.
		return nil, errors.New("aws: external ID must be 2-1224 characters of letters, digits and +=,.@:/-_")
	}
	if len(sessionPolicy) > MaxSessionPolicyChars {
		return nil, fmt.Errorf("aws: session policy is %d characters, over the STS limit of %d", len(sessionPolicy), MaxSessionPolicyChars)
	}
	if sessionName == "" {
		sessionName = "kubebolt-mcp"
	}
	if !reRoleSessionName.MatchString(sessionName) {
		return nil, errors.New("aws: invalid role session name")
	}
	provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(base), roleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = sessionName
		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
		if sessionPolicy != "" {
			o.Policy = aws.String(sessionPolicy)
		}
	})
	return aws.NewCredentialsCache(provider, func(o *aws.CredentialsCacheOptions) {
		// Refresh a minute early so a request signed at the edge of the
		// lifetime does not reach AWS already expired.
		o.ExpiryWindow = time.Minute
	}), nil
}
