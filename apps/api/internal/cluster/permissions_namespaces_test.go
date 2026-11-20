package cluster

import (
	"errors"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func fastNamespaceDiscovery(t *testing.T) {
	t.Helper()
	prevB, prevA := nsDiscoveryBackoff, nsDiscoveryAttempts
	nsDiscoveryBackoff, nsDiscoveryAttempts = time.Millisecond, 3
	t.Cleanup(func() { nsDiscoveryBackoff, nsDiscoveryAttempts = prevB, prevA })
}

// failNamespaceList makes the first `times` namespace LISTs fail with err.
func failNamespaceList(cs *fake.Clientset, times int, err error) *int {
	calls := 0
	cs.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls <= times {
			return true, nil, err
		}
		return false, nil, nil
	})
	return &calls
}

func clusterWithNamespaces(names ...string) *fake.Clientset {
	var objs []runtime.Object
	for _, n := range names {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}})
	}
	return fake.NewSimpleClientset(objs...)
}

func TestDiscoverAccessibleNamespaces(t *testing.T) {
	fastNamespaceDiscovery(t)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", errors.New("no"))
	timeout := errors.New("agent-proxy: timeout after 30s")

	t.Run("the list works", func(t *testing.T) {
		got, err := discoverAccessibleNamespaces(clusterWithNamespaces("a", "b", "c"))
		if err != nil || len(got) != 3 {
			t.Fatalf("got %v, %v; want the 3 namespaces", got, err)
		}
	})
	t.Run("a ServiceAccount that may not list namespaces gets the common ones", func(t *testing.T) {
		cs := clusterWithNamespaces("a")
		failNamespaceList(cs, 99, forbidden)
		got, err := discoverAccessibleNamespaces(cs)
		if err != nil || len(got) != len(commonNamespaces) {
			t.Fatalf("got %v, %v; want the common namespaces", got, err)
		}
	})
	t.Run("a transient error is retried", func(t *testing.T) {
		cs := clusterWithNamespaces("a", "b")
		calls := failNamespaceList(cs, 1, timeout)
		got, err := discoverAccessibleNamespaces(cs)
		if err != nil || len(got) != 2 || *calls != 2 {
			t.Fatalf("got %v, %v after %d calls; want the 2 namespaces on the second call", got, err, *calls)
		}
	})
	t.Run("an error that persists is an error, never a guess", func(t *testing.T) {
		cs := clusterWithNamespaces("a")
		calls := failNamespaceList(cs, 99, timeout)
		got, err := discoverAccessibleNamespaces(cs)
		if err == nil || got != nil || *calls != 3 {
			t.Fatalf("got %v, %v after %d calls; want an error after 3 attempts and no namespaces", got, err, *calls)
		}
	})
}

// allowSSAR answers every SelfSubjectAccessReview: allowed decides per
// (resource, namespace); "" is the cluster-wide check.
func allowSSAR(cs *fake.Clientset, allowed func(resource, namespace string) bool) {
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		ra := r.Spec.ResourceAttributes
		r.Status.Allowed = allowed(ra.Resource, ra.Namespace)
		return true, r, nil
	})
}

// When namespace discovery keeps failing, a type denied cluster-wide stays
// denied: it is never scoped to namespaces nobody checked.
func TestProbePermissions_ANamespaceDiscoveryFailureScopesNothing(t *testing.T) {
	fastNamespaceDiscovery(t)
	cs := clusterWithNamespaces("default", "app-1", "app-2")
	failNamespaceList(cs, 99, errors.New("agent-proxy: timeout after 30s"))
	allowSSAR(cs, func(resource, ns string) bool {
		return resource != "pods" || ns != "" // pods: denied cluster-wide, allowed in any namespace
	})
	perms := probePermissions(cs)
	if p := perms["pods"]; p == nil || p.CanList || p.NamespaceScoped || len(p.Namespaces) != 0 {
		t.Fatalf("pods = %+v; want denied, not scoped to guessed namespaces", p)
	}
}

// A namespace-scoped ServiceAccount (it may not list namespaces) still finds
// its RoleBinding in a common namespace.
func TestProbePermissions_ANamespaceScopedServiceAccountStillFindsItsNamespace(t *testing.T) {
	fastNamespaceDiscovery(t)
	cs := clusterWithNamespaces("default")
	failNamespaceList(cs, 99, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", errors.New("no")))
	allowSSAR(cs, func(resource, ns string) bool { return ns == "default" })
	perms := probePermissions(cs)
	p := perms["pods"]
	if p == nil || !p.CanList || !p.NamespaceScoped || len(p.Namespaces) != 1 || p.Namespaces[0] != "default" {
		t.Fatalf("pods = %+v; want namespace-scoped to default", p)
	}
}
