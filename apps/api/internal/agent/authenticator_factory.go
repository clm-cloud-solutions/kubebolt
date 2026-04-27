package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// AuthenticatorOptions assembles every dependency the factory needs.
// Zero value yields BearerIngestAuth-only; pass KubeClient to enable
// TokenReview mode.
type AuthenticatorOptions struct {
	TenantsStore *auth.TenantsStore

	// KubeClient enables TokenReview mode. Pass the in-cluster client
	// when KubeBolt runs inside the same cluster as the agents. Nil
	// means TokenReview is unavailable — only BearerIngestAuth is built.
	KubeClient kubernetes.Interface

	// ClusterID is the canonical identifier the backend stamps on
	// TokenReview-authenticated identities. When empty and KubeClient
	// is set, the factory tries to resolve it from kube-system UID.
	ClusterID string

	// TokenReviewAudience is the audience the backend asks the apiserver
	// to confirm. Defaults to "kubebolt-backend".
	TokenReviewAudience string

	// BearerCacheTTL caches successful BearerIngestAuth lookups in
	// memory. Defaults to 5 minutes.
	BearerCacheTTL time.Duration

	// TokenReviewCacheTTL caches successful TokenReview results.
	// Defaults to 5 minutes.
	TokenReviewCacheTTL time.Duration
}

// AuthenticatorBundle is what the factory returns: the composite
// authenticator (which the interceptor consumes) plus the underlying
// authenticators so callers can wire them as cache invalidators on the
// admin handlers.
type AuthenticatorBundle struct {
	Composite    auth.AgentAuthenticator
	BearerIngest *auth.BearerIngestAuth
	TokenReview  *auth.TokenReviewAuth // nil when KubeClient is nil
}

// AsCacheInvalidators returns every authenticator that maintains a
// token cache, ready to be plumbed into auth.NewTenantHandlers.
func (b *AuthenticatorBundle) AsCacheInvalidators() []auth.CacheInvalidator {
	out := []auth.CacheInvalidator{}
	if b.BearerIngest != nil {
		out = append(out, b.BearerIngest)
	}
	if b.TokenReview != nil {
		out = append(out, b.TokenReview)
	}
	return out
}

// BuildAuthenticator constructs the authenticator(s) and composes them.
// Returns an error if mandatory dependencies are missing — the caller
// should fail loud rather than fall back silently.
//
// ClusterID resolution precedence:
//
//	opts.ClusterID (explicit)  >  kube-system UID  >  "local"
//
// Discovery failure is logged but does not abort: TokenReview still
// works, only the cluster_id label on identities falls back to "local".
// Operators who care about the canonical cluster id can set ClusterID
// directly.
func BuildAuthenticator(ctx context.Context, opts AuthenticatorOptions) (*AuthenticatorBundle, error) {
	if opts.TenantsStore == nil {
		return nil, errors.New("authenticator factory: TenantsStore is required")
	}
	if opts.BearerCacheTTL == 0 {
		opts.BearerCacheTTL = 5 * time.Minute
	}
	if opts.TokenReviewCacheTTL == 0 {
		opts.TokenReviewCacheTTL = 5 * time.Minute
	}
	if opts.TokenReviewAudience == "" {
		opts.TokenReviewAudience = "kubebolt-backend"
	}

	bearer := auth.NewBearerIngestAuth(opts.TenantsStore, opts.BearerCacheTTL)
	bundle := &AuthenticatorBundle{BearerIngest: bearer}
	authers := []auth.AgentAuthenticator{bearer}
	slog.Info("agent auth: BearerIngest mode enabled",
		slog.Duration("cache_ttl", opts.BearerCacheTTL),
	)

	if opts.KubeClient != nil {
		clusterID := opts.ClusterID
		if clusterID == "" {
			id, err := DiscoverClusterID(ctx, opts.KubeClient)
			if err != nil {
				slog.Warn(`agent auth: cluster_id discovery failed; falling back to "local"`,
					slog.String("error", err.Error()),
				)
				clusterID = "local"
			} else {
				clusterID = id
			}
		}

		defaultTenant, err := opts.TenantsStore.GetDefaultTenant()
		if err != nil {
			return nil, fmt.Errorf("default tenant lookup: %w", err)
		}

		reviewer := auth.NewKubeTokenReviewer(opts.KubeClient)
		tr := auth.NewTokenReviewAuth(reviewer, opts.TokenReviewAudience, defaultTenant.ID, clusterID, opts.TokenReviewCacheTTL)
		bundle.TokenReview = tr
		authers = append(authers, tr)

		slog.Info("agent auth: TokenReview mode enabled",
			slog.String("audience", opts.TokenReviewAudience),
			slog.String("cluster_id", clusterID),
			slog.String("default_tenant_id", defaultTenant.ID),
		)
	} else {
		slog.Info("agent auth: TokenReview mode disabled (no in-cluster client)")
	}

	bundle.Composite = auth.NewCompositeAuth(authers...)
	return bundle, nil
}

// DiscoverClusterID returns the kube-system namespace UID, the de-facto
// canonical cluster identifier in K8s. The agent computes the same UID
// independently so backend + agent converge on a single value without
// coordination.
func DiscoverClusterID(ctx context.Context, client kubernetes.Interface) (string, error) {
	ns, err := client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get kube-system: %w", err)
	}
	if ns.UID == "" {
		return "", errors.New("kube-system namespace has empty UID")
	}
	return string(ns.UID), nil
}

// NewInClusterKubeClient builds a clientset against the local apiserver.
// Returns the rest.ErrNotInCluster when KubeBolt is not running as a
// Pod — main.go uses that to detect "TokenReview unavailable".
func NewInClusterKubeClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// LoadAuthenticatorOptionsFromEnv reads the env vars relevant to the
// factory. The KubeClient is left nil — main.go fills it via
// NewInClusterKubeClient when appropriate.
func LoadAuthenticatorOptionsFromEnv() AuthenticatorOptions {
	return AuthenticatorOptions{
		TokenReviewAudience: os.Getenv("KUBEBOLT_AGENT_TOKEN_AUDIENCE"),
	}
}
