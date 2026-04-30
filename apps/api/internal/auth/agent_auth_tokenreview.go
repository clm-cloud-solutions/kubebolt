package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TokenReviewer is the slice of the K8s authentication API that
// TokenReviewAuth needs. Extracted as an interface so unit tests can
// stub it without spinning up a fake clientset (the fake's TokenReview
// reactor changes shape across client-go releases — easier to mock at
// our boundary).
type TokenReviewer interface {
	Review(ctx context.Context, token, audience string) (*TokenReviewResult, error)
}

// TokenReviewResult is the trimmed-down view of authv1.TokenReviewStatus
// our code needs.
type TokenReviewResult struct {
	Authenticated bool
	Username      string
	UID           string
	Groups        []string
	Audiences     []string
	Error         string
}

// kubeTokenReviewer is the production implementation backed by a real
// clientset against the agent's origin apiserver.
type kubeTokenReviewer struct {
	client kubernetes.Interface
}

// NewKubeTokenReviewer wraps a clientset for production use. Pass the
// in-cluster client of the cluster the agents live in — for SaaS this
// mode is not available, so callers there construct BearerIngestAuth
// instead.
func NewKubeTokenReviewer(client kubernetes.Interface) TokenReviewer {
	return &kubeTokenReviewer{client: client}
}

func (r *kubeTokenReviewer) Review(ctx context.Context, token, audience string) (*TokenReviewResult, error) {
	review := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{
			Token:     token,
			Audiences: []string{audience},
		},
	}
	out, err := r.client.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("tokenreview create: %w", err)
	}
	return &TokenReviewResult{
		Authenticated: out.Status.Authenticated,
		Username:      out.Status.User.Username,
		UID:           out.Status.User.UID,
		Groups:        out.Status.User.Groups,
		Audiences:     out.Status.Audiences,
		Error:         out.Status.Error,
	}, nil
}

// TokenReviewAuth validates Kubernetes ServiceAccount tokens via the
// apiserver's TokenReview API. Used in self-hosted single-cluster setups
// where the backend lives inside the same cluster as the agents.
//
// SaaS / cross-cluster does NOT use this mode — the backend cannot
// authenticate tokens issued by the customer's apiserver. Those agents
// use BearerIngestAuth with a backend-issued ingest token.
//
// The audience guard rejects tokens minted for any other service even
// if their signature is valid — projected SA tokens carry an audience
// list and the apiserver echoes back which entries it accepted.
type TokenReviewAuth struct {
	reviewer  TokenReviewer
	audience  string
	tenantID  string // resolved at startup (default tenant for self-hosted)
	clusterID string // canonical cluster id (kube-system UID)
	cache     *authCache
	nowFn     func() time.Time
}

// NewTokenReviewAuth wires the authenticator. tenantID/clusterID are
// resolved at server startup (commit 5) and held constant for this
// process — self-hosted single-cluster has exactly one of each.
// cacheTTL=0 disables caching (tests); production uses 5*time.Minute.
func NewTokenReviewAuth(reviewer TokenReviewer, audience, tenantID, clusterID string, cacheTTL time.Duration) *TokenReviewAuth {
	return &TokenReviewAuth{
		reviewer:  reviewer,
		audience:  audience,
		tenantID:  tenantID,
		clusterID: clusterID,
		cache:     newAuthCache(cacheTTL),
		nowFn:     func() time.Time { return time.Now().UTC() },
	}
}

// Mode satisfies AgentAuthenticator.
func (a *TokenReviewAuth) Mode() AgentAuthMode { return ModeTokenReview }

// Authenticate validates the SA token via TokenReview, then guards the
// audience and unpacks the SA name/namespace from the username field.
func (a *TokenReviewAuth) Authenticate(ctx context.Context, md metadata.MD, p *peer.Peer) (*AgentIdentity, error) {
	plaintext, err := extractBearer(md)
	if err != nil {
		return nil, err
	}
	hash := hashToken(plaintext)
	if cached, ok := a.cache.get(hash); ok {
		return cached, nil
	}

	res, err := a.reviewer.Review(ctx, plaintext, a.audience)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	if !res.Authenticated {
		if res.Error != "" {
			return nil, fmt.Errorf("%w: %s", ErrTokenInvalid, res.Error)
		}
		return nil, ErrTokenInvalid
	}
	// The apiserver returns the audiences it actually accepted. A
	// missing entry means the token is for a different service — reject
	// even though the signature was valid.
	if !slices.Contains(res.Audiences, a.audience) {
		return nil, fmt.Errorf("%w: audience mismatch (got %v, want %q)", ErrTokenInvalid, res.Audiences, a.audience)
	}
	saName, saNamespace, err := parseServiceAccountUsername(res.Username)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	identity := &AgentIdentity{
		Mode:        ModeTokenReview,
		TenantID:    a.tenantID,
		ClusterID:   a.clusterID,
		SAName:      saName,
		SANamespace: saNamespace,
		TLSVerified: peerHasVerifiedClientCert(p),
		AuthedAt:    a.nowFn(),
	}
	a.cache.put(hash, identity)
	return identity, nil
}

// InvalidateCache clears every cached identity. Self-hosted has no
// strong rotation API (the kubelet rotates SA tokens at its own pace,
// not on operator command), so this exists primarily for symmetry with
// BearerIngestAuth and for tests.
func (a *TokenReviewAuth) InvalidateCache() { a.cache.invalidate() }

// parseServiceAccountUsername splits the canonical SA token subject
// "system:serviceaccount:<namespace>:<name>" into its parts. Returns
// (name, namespace, error).
func parseServiceAccountUsername(username string) (string, string, error) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return "", "", fmt.Errorf("not a service account token (username %q)", username)
	}
	rest := strings.TrimPrefix(username, prefix)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("malformed service account username %q", username)
	}
	return parts[1], parts[0], nil
}
