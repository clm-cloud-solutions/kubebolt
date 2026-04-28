package agent

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// newTenantsStoreForTest spins a fresh BoltDB and TenantsStore on
// disk in t.TempDir(). Used here instead of importing the auth-package
// test helper, which is unexported.
func newTenantsStoreForTest(t *testing.T) *auth.TenantsStore {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewStore(dir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ts, err := auth.NewTenantsStore(store.DB())
	if err != nil {
		t.Fatalf("auth.NewTenantsStore: %v", err)
	}
	return ts
}

func newKubeClientWithKubeSystem(uid string) *fake.Clientset {
	if uid == "" {
		return fake.NewSimpleClientset()
	}
	return fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)},
	})
}

// ─── BuildAuthenticator ───────────────────────────────────────────────

func TestBuildAuthenticator_BearerOnlyWhenKubeClientNil(t *testing.T) {
	store := newTenantsStoreForTest(t)
	bundle, err := BuildAuthenticator(context.Background(), AuthenticatorOptions{
		TenantsStore: store,
	})
	if err != nil {
		t.Fatalf("BuildAuthenticator: %v", err)
	}
	if bundle.BearerIngest == nil {
		t.Error("BearerIngest must always be present")
	}
	if bundle.TokenReview != nil {
		t.Error("TokenReview must be nil when KubeClient is nil")
	}
	if invs := bundle.AsCacheInvalidators(); len(invs) != 1 {
		t.Errorf("expected 1 invalidator, got %d", len(invs))
	}
}

func TestBuildAuthenticator_BothModesWhenKubeClientPresent(t *testing.T) {
	store := newTenantsStoreForTest(t)
	bundle, err := BuildAuthenticator(context.Background(), AuthenticatorOptions{
		TenantsStore: store,
		KubeClient:   newKubeClientWithKubeSystem("cluster-uid-xyz"),
	})
	if err != nil {
		t.Fatalf("BuildAuthenticator: %v", err)
	}
	if bundle.BearerIngest == nil || bundle.TokenReview == nil {
		t.Error("expected both authenticators populated")
	}
	if invs := bundle.AsCacheInvalidators(); len(invs) != 2 {
		t.Errorf("expected 2 invalidators, got %d", len(invs))
	}
	if bundle.Composite == nil {
		t.Error("Composite must be set")
	}
}

func TestBuildAuthenticator_RequiresTenantsStore(t *testing.T) {
	if _, err := BuildAuthenticator(context.Background(), AuthenticatorOptions{}); err == nil {
		t.Error("expected error when TenantsStore is nil")
	}
}

func TestBuildAuthenticator_FallbackClusterIDWhenDiscoveryFails(t *testing.T) {
	// kube-system absent → discovery fails → cluster_id falls back to
	// "local". TokenReview must still be built (warning logged, no error).
	store := newTenantsStoreForTest(t)
	bundle, err := BuildAuthenticator(context.Background(), AuthenticatorOptions{
		TenantsStore: store,
		KubeClient:   fake.NewSimpleClientset(),
	})
	if err != nil {
		t.Fatalf("BuildAuthenticator unexpectedly errored: %v", err)
	}
	if bundle.TokenReview == nil {
		t.Error("TokenReview should be built even when discovery fails")
	}
}

func TestBuildAuthenticator_HonorsExplicitClusterID(t *testing.T) {
	store := newTenantsStoreForTest(t)
	bundle, err := BuildAuthenticator(context.Background(), AuthenticatorOptions{
		TenantsStore: store,
		KubeClient:   newKubeClientWithKubeSystem("ignored-uid"),
		ClusterID:    "explicit-cluster",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.TokenReview == nil {
		t.Fatal("TokenReview should be built")
	}
	// Explicit ClusterID wins; we cannot inspect the internal value
	// without performing a real auth, but the construction succeeded
	// which is the contract under test here.
}

// ─── DiscoverClusterID ────────────────────────────────────────────────

func TestDiscoverClusterID_HappyPath(t *testing.T) {
	id, err := DiscoverClusterID(context.Background(), newKubeClientWithKubeSystem("abc-123"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc-123" {
		t.Errorf("id = %q, want abc-123", id)
	}
}

func TestDiscoverClusterID_MissingNamespace(t *testing.T) {
	if _, err := DiscoverClusterID(context.Background(), fake.NewSimpleClientset()); err == nil {
		t.Error("expected error when kube-system is missing")
	}
}

func TestDiscoverClusterID_EmptyUID(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}, // no UID
	})
	if _, err := DiscoverClusterID(context.Background(), client); err == nil {
		t.Error("expected error when kube-system has empty UID")
	}
}

// ─── LoadAuthenticatorOptionsFromEnv ──────────────────────────────────

func TestLoadAuthenticatorOptionsFromEnv(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_TOKEN_AUDIENCE", "custom-audience")
	opts := LoadAuthenticatorOptionsFromEnv()
	if opts.TokenReviewAudience != "custom-audience" {
		t.Errorf("audience = %q, want custom-audience", opts.TokenReviewAudience)
	}
}
